package helpers

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"io"
	"math/big"
	"time"

	"github.com/holiman/uint256"
	"github.com/stretchr/testify/require"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/params"
	"github.com/ethereum/go-ethereum/rlp"

	altda "github.com/ethereum-optimism/optimism/op-alt-da"
	"github.com/ethereum-optimism/optimism/op-batcher/batcher"
	"github.com/ethereum-optimism/optimism/op-batcher/compressor"
	batcherFlags "github.com/ethereum-optimism/optimism/op-batcher/flags"
	"github.com/ethereum-optimism/optimism/op-e2e/e2eutils"
	"github.com/ethereum-optimism/optimism/op-node/rollup"
	"github.com/ethereum-optimism/optimism/op-node/rollup/derive"
	derive_params "github.com/ethereum-optimism/optimism/op-node/rollup/derive/params"
	"github.com/ethereum-optimism/optimism/op-service/eth"
	"github.com/ethereum-optimism/optimism/op-service/txmgr"
)

type SyncStatusAPI interface {
	SyncStatus(ctx context.Context) (*eth.SyncStatus, error)
}

type BlocksAPI interface {
	BlockByNumber(ctx context.Context, number *big.Int) (*types.Block, error)
}

type L1TxAPI interface {
	PendingNonceAt(ctx context.Context, account common.Address) (uint64, error)
	HeaderByNumber(ctx context.Context, number *big.Int) (*types.Header, error)
	SendTransaction(ctx context.Context, tx *types.Transaction) error
	BlobBaseFee(ctx context.Context) (*big.Int, error)
}

type AltDAInputSetter interface {
	SetInput(ctx context.Context, img []byte) (altda.CommitmentData, error)
}

type BatcherCfg struct {
	// Limit the size of txs
	MinL1TxSize uint64
	MaxL1TxSize uint64

	BatcherKey *ecdsa.PrivateKey

	GarbageCfg *GarbageChannelCfg

	ForceSubmitSingularBatch bool
	ForceSubmitSpanBatch     bool
	UseAltDA                 bool

	DataAvailabilityType batcherFlags.DataAvailabilityType
	AltDA                AltDAInputSetter

	EnableCellProofs bool
}

func DefaultBatcherCfg(dp *e2eutils.DeployParams) *BatcherCfg {
	return &BatcherCfg{
		MinL1TxSize:          0,
		MaxL1TxSize:          128_000,
		BatcherKey:           dp.Secrets.Batcher,
		DataAvailabilityType: batcherFlags.CalldataType,
		EnableCellProofs:     false, // TODO change to true when Osaka activates on L1
	}
}

func DefaultBatcherCfgSafeDb(dp *e2eutils.DeployParams) *BatcherCfg {
	return &BatcherCfg{
		MinL1TxSize:              0,
		MaxL1TxSize:              128_000,
		BatcherKey:               dp.Secrets.Batcher,
		DataAvailabilityType:     batcherFlags.BlobsType,
		ForceSubmitSingularBatch: true,
		EnableCellProofs:         true, // TODO change to true when Osaka activates on L1
	}
}

func MantleDefaultBatcherCfg(dp *e2eutils.DeployParams) *BatcherCfg {
	return &BatcherCfg{
		MinL1TxSize:              0,
		MaxL1TxSize:              128_000,
		ForceSubmitSingularBatch: false,
		BatcherKey:               dp.Secrets.Batcher,
		DataAvailabilityType:     batcherFlags.CalldataType,
		EnableCellProofs:         true,
	}
}

func MantleSpanBatcherCfg(dp *e2eutils.DeployParams) *BatcherCfg {
	return &BatcherCfg{
		MinL1TxSize:          0,
		MaxL1TxSize:          128_000,
		ForceSubmitSpanBatch: true,
		BatcherKey:           dp.Secrets.Batcher,
		DataAvailabilityType: batcherFlags.CalldataType,
		EnableCellProofs:     true,
	}
}

func MantleSingularBatcherCfg(dp *e2eutils.DeployParams) *BatcherCfg {
	return &BatcherCfg{
		MinL1TxSize:              0,
		MaxL1TxSize:              128_000,
		ForceSubmitSingularBatch: true,
		BatcherKey:               dp.Secrets.Batcher,
		DataAvailabilityType:     batcherFlags.CalldataType,
		EnableCellProofs:         true,
	}
}

func MantleBlobsSpanBatcherCfg(dp *e2eutils.DeployParams) *BatcherCfg {
	return &BatcherCfg{
		MinL1TxSize:          0,
		MaxL1TxSize:          128_000,
		ForceSubmitSpanBatch: true,
		BatcherKey:           dp.Secrets.Batcher,
		DataAvailabilityType: batcherFlags.BlobsType,
		EnableCellProofs:     true,
	}
}

func MantleBlobsSingularBatcherCfg(dp *e2eutils.DeployParams) *BatcherCfg {
	return &BatcherCfg{
		MinL1TxSize:              0,
		MaxL1TxSize:              128_000,
		ForceSubmitSingularBatch: true,
		BatcherKey:               dp.Secrets.Batcher,
		DataAvailabilityType:     batcherFlags.BlobsType,
		EnableCellProofs:         true,
	}
}

func AltDABatcherCfg(dp *e2eutils.DeployParams, altDA AltDAInputSetter) *BatcherCfg {
	return &BatcherCfg{
		MinL1TxSize:          0,
		MaxL1TxSize:          128_000,
		BatcherKey:           dp.Secrets.Batcher,
		DataAvailabilityType: batcherFlags.CalldataType,
		AltDA:                altDA,
		UseAltDA:             true,
	}
}

type L2BlockRefs interface {
	L2BlockRefByHash(ctx context.Context, hash common.Hash) (eth.L2BlockRef, error)
}

// L2Batcher buffers and submits L2 batches to L1.
//
// TODO: note the batcher shares little logic/state with actual op-batcher,
// tests should only use this actor to build batch contents for rollup node actors to consume,
// until the op-batcher is refactored and can be covered better.
type L2Batcher struct {
	log log.Logger

	rollupCfg *rollup.Config

	syncStatusAPI SyncStatusAPI
	l2            BlocksAPI
	l1            L1TxAPI
	engCl         L2BlockRefs

	l1Signer types.Signer

	L2ChannelOut     ChannelOutIface
	l2Submitting     bool // when the channel out is being submitted, and not safe to write to without resetting
	L2BufferedBlock  eth.L2BlockRef
	l2SubmittedBlock eth.L2BlockRef
	l2BatcherCfg     *BatcherCfg
	BatcherAddr      common.Address

	LastSubmitted *types.Transaction
}

func NewL2Batcher(log log.Logger, rollupCfg *rollup.Config, batcherCfg *BatcherCfg, api SyncStatusAPI, l1 L1TxAPI, l2 BlocksAPI, engCl L2BlockRefs) *L2Batcher {
	return &L2Batcher{
		log:           log,
		rollupCfg:     rollupCfg,
		syncStatusAPI: api,
		l1:            l1,
		l2:            l2,
		engCl:         engCl,
		l2BatcherCfg:  batcherCfg,
		l1Signer:      types.LatestSignerForChainID(rollupCfg.L1ChainID),
		BatcherAddr:   crypto.PubkeyToAddress(batcherCfg.BatcherKey.PublicKey),
	}
}

// SubmittingData indicates if the actor is submitting buffer data.
// All data must be submitted before it can safely continue buffering more L2 blocks.
func (s *L2Batcher) SubmittingData() bool {
	return s.l2Submitting
}

// Reset the batcher state, clearing any buffered data.
func (s *L2Batcher) Reset() {
	s.L2ChannelOut = nil
	s.l2Submitting = false
	s.L2BufferedBlock = eth.L2BlockRef{}
	s.l2SubmittedBlock = eth.L2BlockRef{}
}

// ActL2BatchBuffer adds the next L2 block to the batch buffer.
// If the buffer is being submitted, the buffer is wiped.
func (s *L2Batcher) ActL2BatchBuffer(t Testing, opts ...BufferOption) {
	require.NoError(t, s.Buffer(t, opts...), "failed to add block to channel")
}

// ActCreateChannel creates a channel if we don't have one yet.
func (s *L2Batcher) ActCreateChannel(t Testing, useSpanChannelOut bool, spanChannelOutOpts ...derive.SpanChannelOutOption) {
	var err error
	if s.L2ChannelOut == nil {
		var ch ChannelOutIface
		if s.l2BatcherCfg.GarbageCfg != nil {
			ch, err = NewGarbageChannelOut(s.l2BatcherCfg.GarbageCfg)
		} else {
			target := batcher.MaxDataSize(1, s.l2BatcherCfg.MaxL1TxSize)
			c, e := compressor.NewShadowCompressor(compressor.Config{
				TargetOutputSize: target,
				CompressionAlgo:  derive.Zlib,
			})
			require.NoError(t, e, "failed to create compressor")

			if s.l2BatcherCfg.ForceSubmitSingularBatch && s.l2BatcherCfg.ForceSubmitSpanBatch {
				t.Fatalf("ForceSubmitSingularBatch and ForceSubmitSpanBatch cannot be set to true at the same time")
			} else {
				chainSpec := rollup.NewChainSpec(s.rollupCfg)
				// use span batch if we're forcing it or if we're at/beyond delta
				if s.l2BatcherCfg.ForceSubmitSpanBatch || useSpanChannelOut {
					ch, err = derive.NewSpanChannelOut(target, derive.Zlib, chainSpec, spanChannelOutOpts...)
					// use singular batches in all other cases
				} else {
					ch, err = derive.NewSingularChannelOut(c, chainSpec)
				}
			}
		}
		require.NoError(t, err, "failed to create channel")
		s.L2ChannelOut = ch
	}
}

type bufferOptions struct {
	blockModifiers   []BlockModifier
	channelModifiers []derive.SpanChannelOutOption
}

type BlockModifier = func(block *types.Block) *types.Block

type BufferOption = func(*bufferOptions)

func WithBlockModifier(modifier BlockModifier) BufferOption {
	return func(opts *bufferOptions) {
		opts.blockModifiers = append(opts.blockModifiers, modifier)
	}
}

func WithChannelModifier(modifier derive.SpanChannelOutOption) BufferOption {
	return func(opts *bufferOptions) {
		opts.channelModifiers = append(opts.channelModifiers, modifier)
	}
}

func BlockLogger(t e2eutils.TestingBase) BlockModifier {
	f := func(block *types.Block) *types.Block {
		t.Log("added block", "num", block.Number(), "txs", block.Transactions(), "time", block.Time())
		return block
	}
	return f
}

func (s *L2Batcher) Buffer(t Testing, bufferOpts ...BufferOption) error {
	options := bufferOptions{}
	for _, opt := range bufferOpts {
		opt(&options)
	}

	if s.l2Submitting { // break ongoing submitting work if necessary
		s.L2ChannelOut = nil
		s.l2Submitting = false
	}
	syncStatus, err := s.syncStatusAPI.SyncStatus(t.Ctx())
	require.NoError(t, err, "no sync status error")
	// If we just started, start at safe-head
	if s.l2SubmittedBlock == (eth.L2BlockRef{}) {
		s.log.Info("Starting batch-submitter work at safe-head", "safe", syncStatus.SafeL2)
		s.l2SubmittedBlock = syncStatus.SafeL2
		s.L2BufferedBlock = syncStatus.SafeL2
		s.L2ChannelOut = nil
	}
	if s.l2SubmittedBlock.Number < syncStatus.SafeL2.Number {
		s.log.Info("Safe head progressed, batch submission will continue from the new safe head now", "last", s.l2SubmittedBlock, "safe", syncStatus.SafeL2)
		s.l2SubmittedBlock = syncStatus.SafeL2
		s.L2BufferedBlock = syncStatus.SafeL2
		s.L2ChannelOut = nil
	}
	// Add the next unsafe block to the channel
	if s.L2BufferedBlock.Number >= syncStatus.UnsafeL2.Number {
		if s.L2BufferedBlock.Number > syncStatus.UnsafeL2.Number || s.L2BufferedBlock.Hash != syncStatus.UnsafeL2.Hash {
			s.log.Error("detected a reorg in L2 chain vs previous buffered information, resetting to safe head now", "safe_head", syncStatus.SafeL2)
			s.l2SubmittedBlock = syncStatus.SafeL2
			s.L2BufferedBlock = syncStatus.SafeL2
			s.L2ChannelOut = nil
		} else {
			s.log.Info("nothing left to submit")
			return nil
		}
	}

	block, err := s.l2.BlockByNumber(t.Ctx(), big.NewInt(int64(s.L2BufferedBlock.Number+1)))
	require.NoError(t, err, "need l2 block %d from sync status", s.l2SubmittedBlock.Number+1)
	if block.ParentHash() != s.L2BufferedBlock.Hash {
		s.log.Error("detected a reorg in L2 chain vs previous submitted information, resetting to safe head now", "safe_head", syncStatus.SafeL2)
		s.l2SubmittedBlock = syncStatus.SafeL2
		s.L2BufferedBlock = syncStatus.SafeL2
		s.L2ChannelOut = nil
	}

	// Apply modifications to the block
	for _, f := range options.blockModifiers {
		if f != nil {
			block = f(block)
		}
	}

	// Determine batch type based on configuration flags.
	// This mimics the real op-batcher behavior where batch type is determined by static configuration,
	// not by dynamic fork activation status.
	//
	// Mantle Arsia fork note:
	// In Mantle, Arsia fork activates Delta (SpanBatch support) along with other OP Stack forks.
	// However, the batcher should still respect ForceSubmitSingularBatch flag to allow testing
	// SingularBatch behavior even after Arsia activation, which is critical for batch equivalence tests.
	//
	// Priority:
	// 1. If ForceSubmitSingularBatch is true, always use SingularBatch (even if Delta is active)
	// 2. If ForceSubmitSpanBatch is true, always use SpanBatch
	// 3. Otherwise, auto-select based on Delta activation status
	useSpanChannelOut := false
	if s.l2BatcherCfg.ForceSubmitSpanBatch {
		useSpanChannelOut = true
	} else if !s.l2BatcherCfg.ForceSubmitSingularBatch {
		// Only auto-select based on Delta activation when no force flag is set
		useSpanChannelOut = s.rollupCfg.IsDelta(block.Time())
	}

	s.ActCreateChannel(t, useSpanChannelOut, options.channelModifiers...)

	if _, err := s.L2ChannelOut.AddBlock(s.rollupCfg, block); err != nil {
		return err
	}
	ref, err := s.engCl.L2BlockRefByHash(t.Ctx(), block.Hash())
	require.NoError(t, err, "failed to get L2BlockRef")
	s.L2BufferedBlock = ref
	return nil
}

// ActAddBlockByNumber causes the batcher to pull the block with the provided
// number, and add it to its ChannelOut.
func (s *L2Batcher) ActAddBlockByNumber(t Testing, blockNumber int64, opts ...BlockModifier) {
	block, err := s.l2.BlockByNumber(t.Ctx(), big.NewInt(blockNumber))
	require.NoError(t, err)
	require.NotNil(t, block)

	// cache block hash before we modify the block
	blockHash := block.Hash()

	// Apply modifications to the block
	for _, f := range opts {
		if f != nil {
			block = f(block)
		}
	}

	_, err = s.L2ChannelOut.AddBlock(s.rollupCfg, block)
	require.NoError(t, err)
	ref, err := s.engCl.L2BlockRefByHash(t.Ctx(), blockHash)
	require.NoError(t, err, "failed to get L2BlockRef")
	s.L2BufferedBlock = ref
}

func (s *L2Batcher) ActL2ChannelClose(t Testing) {
	// Don't run this action if there's no data to submit
	if s.L2ChannelOut == nil {
		t.InvalidAction("need to buffer data first, cannot batch submit with empty buffer")
		return
	}
	require.NoError(t, s.L2ChannelOut.Close(), "must close channel before submitting it")
}

func (s *L2Batcher) ReadNextOutputFrame(t Testing) []byte {
	// Don't run this action if there's no data to submit
	if s.L2ChannelOut == nil {
		t.InvalidAction("need to buffer data first, cannot batch submit with empty buffer")
		return nil
	}
	// Collect the output frame
	data := new(bytes.Buffer)
	data.WriteByte(derive_params.DerivationVersion0)
	// subtract one, to account for the version byte
	if _, err := s.L2ChannelOut.OutputFrame(data, s.l2BatcherCfg.MaxL1TxSize-1); err == io.EOF {
		s.L2ChannelOut = nil
		s.l2Submitting = false
	} else if err != nil {
		s.l2Submitting = false
		t.Fatalf("failed to output channel data to frame: %v", err)
	}

	return data.Bytes()
}

// ActL2BatchSubmit constructs a batch tx from previous buffered L2 blocks, and submits it to L1
func (s *L2Batcher) ActL2BatchSubmit(t Testing, txOpts ...func(tx *types.DynamicFeeTx)) {
	s.ActL2BatchSubmitRaw(t, s.ReadNextOutputFrame(t), txOpts...)
}

func (s *L2Batcher) ActL2BatchSubmitRaw(t Testing, payload []byte, txOpts ...func(tx *types.DynamicFeeTx)) {
	if s.l2BatcherCfg.UseAltDA {
		comm, err := s.l2BatcherCfg.AltDA.SetInput(t.Ctx(), payload)
		require.NoError(t, err, "failed to set input for altda")
		payload = comm.TxData()
	}

	nonce, err := s.l1.PendingNonceAt(t.Ctx(), s.BatcherAddr)
	require.NoError(t, err, "need batcher nonce")

	gasTipCap := big.NewInt(2 * params.GWei)
	pendingHeader, err := s.l1.HeaderByNumber(t.Ctx(), big.NewInt(-1))
	require.NoError(t, err, "need l1 pending header for gas price estimation")
	gasFeeCap := new(big.Int).Add(gasTipCap, new(big.Int).Mul(pendingHeader.BaseFee, big.NewInt(2)))

	var txData types.TxData
	if s.l2BatcherCfg.DataAvailabilityType == batcherFlags.CalldataType {
		rawTx := &types.DynamicFeeTx{
			ChainID:   s.rollupCfg.L1ChainID,
			Nonce:     nonce,
			To:        &s.rollupCfg.BatchInboxAddress,
			GasTipCap: gasTipCap,
			GasFeeCap: gasFeeCap,
			Data:      payload,
		}
		for _, opt := range txOpts {
			opt(rawTx)
		}

		gas, err := core.FloorDataGas(params.Rules{IsAmsterdam: true}, rawTx.Data, nil)
		require.NoError(t, err, "need to compute floor data gas")
		rawTx.Gas = gas
		txData = rawTx
	} else if s.l2BatcherCfg.DataAvailabilityType == batcherFlags.BlobsType {
		var b eth.Blob
		require.NoError(t, b.FromData(payload), "must turn data into blob")
		sidecar, blobHashes, err := txmgr.MakeSidecar([]*eth.Blob{&b}, s.l2BatcherCfg.EnableCellProofs)
		require.NoError(t, err)
		require.NotNil(t, pendingHeader.ExcessBlobGas, "need L1 header with 4844 properties")
		blobBaseFee, err := s.l1.BlobBaseFee(t.Ctx())
		require.NoError(t, err, "need blob base fee")
		blobFeeCap := new(uint256.Int).Mul(uint256.NewInt(2), uint256.MustFromBig(blobBaseFee))
		if blobFeeCap.Lt(uint256.NewInt(params.GWei)) { // ensure we meet 1 gwei geth tx-pool minimum
			blobFeeCap = uint256.NewInt(params.GWei)
		}
		txData = &types.BlobTx{
			To:         s.rollupCfg.BatchInboxAddress,
			Data:       nil,
			Gas:        params.TxGas, // intrinsic gas only
			BlobHashes: blobHashes,
			Sidecar:    sidecar,
			ChainID:    uint256.MustFromBig(s.rollupCfg.L1ChainID),
			GasTipCap:  uint256.MustFromBig(gasTipCap),
			GasFeeCap:  uint256.MustFromBig(gasFeeCap),
			BlobFeeCap: blobFeeCap,
			Value:      uint256.NewInt(0),
			Nonce:      nonce,
		}
	} else {
		t.Fatalf("unrecognized DA type: %q", string(s.l2BatcherCfg.DataAvailabilityType))
	}

	tx, err := types.SignNewTx(s.l2BatcherCfg.BatcherKey, s.l1Signer, txData)
	require.NoError(t, err, "need to sign tx")

	err = s.l1.SendTransaction(t.Ctx(), tx)
	require.NoError(t, err, "need to send tx")
	s.LastSubmitted = tx
}

// ActL2BatchSubmitMantleRaw submits a batch transaction to L1 using Mantle's blob encoding format.
// For Mantle (before Arsia activation), it wraps the payload in an RLP-encoded frame array.
// For Arsia and later, it uses the standard OP Stack format (single frame per blob).
func (s *L2Batcher) ActL2BatchSubmitMantleRaw(t Testing, payload []byte, txOpts ...func(tx *types.DynamicFeeTx)) {
	// Handle AltDA if enabled
	if s.l2BatcherCfg.UseAltDA {
		comm, err := s.l2BatcherCfg.AltDA.SetInput(t.Ctx(), payload)
		require.NoError(t, err, "failed to set input for altda")
		payload = comm.TxData()
	}

	// Get nonce and gas prices
	nonce, err := s.l1.PendingNonceAt(t.Ctx(), s.BatcherAddr)
	require.NoError(t, err, "need batcher nonce")

	gasTipCap := big.NewInt(2 * params.GWei)
	pendingHeader, err := s.l1.HeaderByNumber(t.Ctx(), big.NewInt(-1))
	require.NoError(t, err, "need l1 pending header for gas price estimation")
	gasFeeCap := new(big.Int).Add(gasTipCap, new(big.Int).Mul(pendingHeader.BaseFee, big.NewInt(2)))

	var txData types.TxData
	if s.l2BatcherCfg.DataAvailabilityType == batcherFlags.CalldataType {
		// Calldata path
		rawTx := &types.DynamicFeeTx{
			ChainID:   s.rollupCfg.L1ChainID,
			Nonce:     nonce,
			To:        &s.rollupCfg.BatchInboxAddress,
			GasTipCap: gasTipCap,
			GasFeeCap: gasFeeCap,
			Data:      payload,
		}
		for _, opt := range txOpts {
			opt(rawTx)
		}
		gas, err := core.FloorDataGas(params.Rules{IsAmsterdam: true}, rawTx.Data, nil)
		require.NoError(t, err, "need to compute floor data gas")
		rawTx.Gas = gas
		txData = rawTx
	} else if s.l2BatcherCfg.DataAvailabilityType == batcherFlags.BlobsType {
		var blobs []*eth.Blob

		// Check if we need RLP encoding (before Arsia activation)
		// Use time.Now() to match the real batcher's behavior
		if !s.rollupCfg.IsMantleArsia(uint64(time.Now().Unix())) {
			// Before Arsia: Use Mantle's RLP-encoded frame array format
			// payload is already [version_byte][frame_data]
			// Wrap it in an array and RLP encode: RLP([[version_byte][frame_data]])
			frameDataArray := []eth.Data{payload}
			wholeBlobData, err := rlp.EncodeToBytes(frameDataArray)
			require.NoError(t, err, "failed to RLP encode frame array for Mantle blob format")

			// Convert RLP-encoded data to blob
			var blob eth.Blob
			require.NoError(t, blob.FromData(wholeBlobData), "must turn RLP-encoded data into blob")
			blobs = []*eth.Blob{&blob}
		} else {
			// After Arsia: Use standard OP Stack format (single frame per blob)
			var blob eth.Blob
			require.NoError(t, blob.FromData(payload), "must turn data into blob")
			blobs = []*eth.Blob{&blob}
		}

		// Create blob transaction
		sidecar, blobHashes, err := txmgr.MakeSidecar(blobs, s.l2BatcherCfg.EnableCellProofs)
		require.NoError(t, err)
		require.NotNil(t, pendingHeader.ExcessBlobGas, "need L1 header with 4844 properties")
		blobBaseFee, err := s.l1.BlobBaseFee(t.Ctx())
		require.NoError(t, err, "need blob base fee")
		blobFeeCap := new(uint256.Int).Mul(uint256.NewInt(2), uint256.MustFromBig(blobBaseFee))
		if blobFeeCap.Lt(uint256.NewInt(params.GWei)) {
			blobFeeCap = uint256.NewInt(params.GWei)
		}
		txData = &types.BlobTx{
			To:         s.rollupCfg.BatchInboxAddress,
			Data:       nil,
			Gas:        params.TxGas,
			BlobHashes: blobHashes,
			Sidecar:    sidecar,
			ChainID:    uint256.MustFromBig(s.rollupCfg.L1ChainID),
			GasTipCap:  uint256.MustFromBig(gasTipCap),
			GasFeeCap:  uint256.MustFromBig(gasFeeCap),
			BlobFeeCap: blobFeeCap,
			Value:      uint256.NewInt(0),
			Nonce:      nonce,
		}
	} else {
		t.Fatalf("unrecognized DA type: %q", string(s.l2BatcherCfg.DataAvailabilityType))
	}

	// Sign and send transaction
	tx, err := types.SignNewTx(s.l2BatcherCfg.BatcherKey, s.l1Signer, txData)
	require.NoError(t, err, "need to sign tx")

	err = s.l1.SendTransaction(t.Ctx(), tx)
	require.NoError(t, err, "need to send tx")
	s.LastSubmitted = tx
}

// ActL2BatchSubmitMantle is a convenience wrapper that reads the next frame and submits it using Mantle encoding.
func (s *L2Batcher) ActL2BatchSubmitMantle(t Testing, txOpts ...func(tx *types.DynamicFeeTx)) {
	s.ActL2BatchSubmitMantleRaw(t, s.ReadNextOutputFrame(t), txOpts...)
}

func (s *L2Batcher) ActL2BatchSubmitMantleAtTime(t Testing, l1BlockTime uint64, txOpts ...func(tx *types.DynamicFeeTx)) {
	s.ActL2BatchSubmitMantleRawAtTime(t, s.ReadNextOutputFrame(t), l1BlockTime, txOpts...)
}

// ActL2BatchSubmitMantleRawAtTime submits a batch transaction to L1 using Mantle's blob encoding format.
// Unlike ActL2BatchSubmitMantleRaw which uses time.Now(), this method uses the provided l1BlockTime
// to determine the correct format:
// - Before Arsia activation: RLP-encoded frame array format (MantleBlobs)
// - After Arsia activation: Standard OP Stack format (Blobs)
//
// This is essential for testing Arsia activation boundaries where the format must match
// the data source selection logic which is based on L1 block time.
func (s *L2Batcher) ActL2BatchSubmitMantleRawAtTime(t Testing, payload []byte, l1BlockTime uint64, txOpts ...func(tx *types.DynamicFeeTx)) {
	// Handle AltDA if enabled
	if s.l2BatcherCfg.UseAltDA {
		comm, err := s.l2BatcherCfg.AltDA.SetInput(t.Ctx(), payload)
		require.NoError(t, err, "failed to set input for altda")
		payload = comm.TxData()
	}

	// Get nonce and gas prices
	nonce, err := s.l1.PendingNonceAt(t.Ctx(), s.BatcherAddr)
	require.NoError(t, err, "need batcher nonce")

	gasTipCap := big.NewInt(2 * params.GWei)
	pendingHeader, err := s.l1.HeaderByNumber(t.Ctx(), big.NewInt(-1))
	require.NoError(t, err, "need l1 pending header for gas price estimation")
	gasFeeCap := new(big.Int).Add(gasTipCap, new(big.Int).Mul(pendingHeader.BaseFee, big.NewInt(2)))

	var txData types.TxData
	if s.l2BatcherCfg.DataAvailabilityType == batcherFlags.CalldataType {
		// Calldata path
		rawTx := &types.DynamicFeeTx{
			ChainID:   s.rollupCfg.L1ChainID,
			Nonce:     nonce,
			To:        &s.rollupCfg.BatchInboxAddress,
			GasTipCap: gasTipCap,
			GasFeeCap: gasFeeCap,
			Data:      payload,
		}
		for _, opt := range txOpts {
			opt(rawTx)
		}
		gas, err := core.FloorDataGas(params.Rules{IsAmsterdam: true}, rawTx.Data, nil)
		require.NoError(t, err, "need to compute floor data gas")
		rawTx.Gas = gas
		txData = rawTx
	} else if s.l2BatcherCfg.DataAvailabilityType == batcherFlags.BlobsType {
		var blobs []*eth.Blob

		// Use the provided l1BlockTime to determine format (not time.Now())
		// This ensures the batcher format matches the data source selection in derivation
		if !s.rollupCfg.IsMantleArsia(l1BlockTime) {
			// Before Arsia: Use Mantle's RLP-encoded frame array format
			// payload is already [version_byte][frame_data]
			// Wrap it in an array and RLP encode: RLP([[version_byte][frame_data]])
			frameDataArray := []eth.Data{payload}
			wholeBlobData, err := rlp.EncodeToBytes(frameDataArray)
			require.NoError(t, err, "failed to RLP encode frame array for Mantle blob format")

			// Convert RLP-encoded data to blob
			var blob eth.Blob
			require.NoError(t, blob.FromData(wholeBlobData), "must turn RLP-encoded data into blob")
			blobs = []*eth.Blob{&blob}
		} else {
			// After Arsia: Use standard OP Stack format (single frame per blob)
			var blob eth.Blob
			require.NoError(t, blob.FromData(payload), "must turn data into blob")
			blobs = []*eth.Blob{&blob}
		}

		// Create blob transaction
		sidecar, blobHashes, err := txmgr.MakeSidecar(blobs, s.l2BatcherCfg.EnableCellProofs)
		require.NoError(t, err)
		require.NotNil(t, pendingHeader.ExcessBlobGas, "need L1 header with 4844 properties")
		blobBaseFee, err := s.l1.BlobBaseFee(t.Ctx())
		require.NoError(t, err, "need blob base fee")
		blobFeeCap := new(uint256.Int).Mul(uint256.NewInt(2), uint256.MustFromBig(blobBaseFee))
		if blobFeeCap.Lt(uint256.NewInt(params.GWei)) {
			blobFeeCap = uint256.NewInt(params.GWei)
		}
		txData = &types.BlobTx{
			To:         s.rollupCfg.BatchInboxAddress,
			Data:       nil,
			Gas:        params.TxGas,
			BlobHashes: blobHashes,
			Sidecar:    sidecar,
			ChainID:    uint256.MustFromBig(s.rollupCfg.L1ChainID),
			GasTipCap:  uint256.MustFromBig(gasTipCap),
			GasFeeCap:  uint256.MustFromBig(gasFeeCap),
			BlobFeeCap: blobFeeCap,
			Value:      uint256.NewInt(0),
			Nonce:      nonce,
		}
	} else {
		t.Fatalf("unrecognized DA type: %q", string(s.l2BatcherCfg.DataAvailabilityType))
	}

	// Sign and send transaction
	tx, err := types.SignNewTx(s.l2BatcherCfg.BatcherKey, s.l1Signer, txData)
	require.NoError(t, err, "need to sign tx")

	err = s.l1.SendTransaction(t.Ctx(), tx)
	require.NoError(t, err, "need to send tx")
	s.LastSubmitted = tx
}
func (s *L2Batcher) ActL2BatchSubmitMultiBlob(t Testing, numBlobs int) {
	// Update to Prague if L1 changes to Prague and we need more blobs in multi-blob tests.
	maxBlobsPerBlock := params.DefaultCancunBlobConfig.Max
	if s.l2BatcherCfg.DataAvailabilityType != batcherFlags.BlobsType {
		t.InvalidAction("ActL2BatchSubmitMultiBlob only available for Blobs DA type")
		return
	} else if numBlobs > maxBlobsPerBlock || numBlobs < 1 {
		t.InvalidAction("invalid number of blobs %d, must be within [1,%d]", numBlobs, maxBlobsPerBlock)
	}

	// Don't run this action if there's no data to submit
	if s.L2ChannelOut == nil {
		t.InvalidAction("need to buffer data first, cannot batch submit with empty buffer")
		return
	}

	// Collect the output frames into blobs
	blobs := make([]*eth.Blob, numBlobs)
	for i := 0; i < numBlobs; i++ {
		data := new(bytes.Buffer)
		data.WriteByte(derive_params.DerivationVersion0)
		// write only a few bytes to all but the last blob
		l := uint64(derive.FrameV0OverHeadSize + 4) // 4 bytes content
		if i == numBlobs-1 {
			// write remaining channel to last frame
			// subtract one, to account for the version byte
			l = s.l2BatcherCfg.MaxL1TxSize - 1
		}
		if _, err := s.L2ChannelOut.OutputFrame(data, l); err == io.EOF {
			s.l2Submitting = false
			if i < numBlobs-1 {
				t.Fatalf("failed to fill up %d blobs, only filled %d", numBlobs, i+1)
			}
			s.L2ChannelOut = nil
		} else if err != nil {
			s.l2Submitting = false
			t.Fatalf("failed to output channel data to frame: %v", err)
		}

		blobs[i] = new(eth.Blob)
		require.NoError(t, blobs[i].FromData(data.Bytes()), "must turn data into blob")
	}

	nonce, err := s.l1.PendingNonceAt(t.Ctx(), s.BatcherAddr)
	require.NoError(t, err, "need batcher nonce")

	gasTipCap := big.NewInt(2 * params.GWei)
	pendingHeader, err := s.l1.HeaderByNumber(t.Ctx(), big.NewInt(-1))
	require.NoError(t, err, "need l1 pending header for gas price estimation")
	gasFeeCap := new(big.Int).Add(gasTipCap, new(big.Int).Mul(pendingHeader.BaseFee, big.NewInt(2)))

	sidecar, blobHashes, err := txmgr.MakeSidecar(blobs, s.l2BatcherCfg.EnableCellProofs)
	require.NoError(t, err)
	blobBaseFee, err := s.l1.BlobBaseFee(t.Ctx())
	require.NoError(t, err, "need blob base fee")
	blobFeeCap := new(uint256.Int).Mul(uint256.NewInt(2), uint256.MustFromBig(blobBaseFee))
	if blobFeeCap.Lt(uint256.NewInt(params.GWei)) { // ensure we meet 1 gwei geth tx-pool minimum
		blobFeeCap = uint256.NewInt(params.GWei)
	}
	txData := &types.BlobTx{
		To:         s.rollupCfg.BatchInboxAddress,
		Data:       nil,
		Gas:        params.TxGas, // intrinsic gas only
		BlobHashes: blobHashes,
		Sidecar:    sidecar,
		ChainID:    uint256.MustFromBig(s.rollupCfg.L1ChainID),
		GasTipCap:  uint256.MustFromBig(gasTipCap),
		GasFeeCap:  uint256.MustFromBig(gasFeeCap),
		BlobFeeCap: blobFeeCap,
		Value:      uint256.NewInt(0),
		Nonce:      nonce,
	}

	tx, err := types.SignNewTx(s.l2BatcherCfg.BatcherKey, s.l1Signer, txData)
	require.NoError(t, err, "need to sign tx")

	err = s.l1.SendTransaction(t.Ctx(), tx)
	require.NoError(t, err, "need to send tx")
	s.LastSubmitted = tx
}

// ActL2BatchSubmitGarbage constructs a malformed channel frame and submits it to the
// batch inbox. This *should* cause the batch inbox to reject the blocks
// encoded within the frame, even if the blocks themselves are valid.
func (s *L2Batcher) ActL2BatchSubmitGarbage(t Testing, kind GarbageKind) {
	outputFrame := s.ReadNextOutputFrame(t)
	s.ActL2BatchSubmitGarbageRaw(t, outputFrame, kind)
}

// ActL2BatchSubmitGarbageRaw constructs a malformed channel frame from `outputFrame` and submits it to the
// batch inbox. This *should* cause the batch inbox to reject the blocks
// encoded within the frame, even if the blocks themselves are valid.
func (s *L2Batcher) ActL2BatchSubmitGarbageRaw(t Testing, outputFrame []byte, kind GarbageKind) {
	// Malform the output frame
	switch kind {
	// Strip the derivation version byte from the output frame
	case STRIP_VERSION:
		outputFrame = outputFrame[1:]
	// Replace the output frame with random bytes of length [1, 512]
	case RANDOM:
		i, err := rand.Int(rand.Reader, big.NewInt(512))
		require.NoError(t, err, "error generating random bytes length")
		buf := make([]byte, i.Int64()+1)
		_, err = rand.Read(buf)
		require.NoError(t, err, "error generating random bytes")
		outputFrame = buf
	// Remove 4 bytes from the tail end of the output frame
	case TRUNCATE_END:
		outputFrame = outputFrame[:len(outputFrame)-4]
	// Append 4 garbage bytes to the end of the output frame
	case DIRTY_APPEND:
		outputFrame = append(outputFrame, []byte{0xBA, 0xD0, 0xC0, 0xDE}...)
	case INVALID_COMPRESSION:
		// Do nothing post frame encoding- the `GarbageChannelOut` used for this case is modified to
		// use gzip compression rather than zlib, which is invalid.
		break
	case MALFORM_RLP:
		// Do nothing post frame encoding- the `GarbageChannelOut` used for this case is modified to
		// write malformed RLP each time a block is added to the channel.
		break
	default:
		t.Fatalf("Unexpected garbage kind: %v", kind)
	}

	s.ActL2BatchSubmitRaw(t, outputFrame)
}

// ActL2BatchSubmitMantleGarbageRaw submits garbage data for Mantle's blob encoding format.
// This method handles both Limb and Arsia formats:
//
// Limb format (before Arsia):
//
//	Blob = RLP([[version_byte][frame1_data], [version_byte][frame2_data], ...])
//
// Arsia format (after Arsia activation):
//
//	Blob = [version_byte][frame_data]  (same as OP Stack)
//
// The method automatically detects which format to use based on the current L1 block time
// and generates appropriately malformed data for each format.
//
// This is different from OP Stack's ActL2BatchSubmitGarbageRaw because:
// 1. It needs to handle two different blob formats (Limb vs Arsia)
// 2. It directly submits blob transactions to avoid double RLP encoding
// 3. It uses L1 block time (not time.Now()) to determine the correct format
func (s *L2Batcher) ActL2BatchSubmitMantleGarbageRaw(t Testing, outputFrame []byte, kind GarbageKind) {
	// Get current L1 block time to determine which format to use
	// This is critical because the format must match what the derivation pipeline expects
	pendingHeader, err := s.l1.HeaderByNumber(t.Ctx(), big.NewInt(-1))
	require.NoError(t, err, "need l1 pending header")
	l1BlockTime := pendingHeader.Time

	// Check if Arsia is active at this L1 block time
	// - Before Arsia: Use Limb format (RLP-encoded frame array)
	// - After Arsia: Use OP Stack format (single frame per blob)
	isArsia := s.rollupCfg.IsMantleArsia(l1BlockTime)

	var finalData []byte

	switch kind {
	// STRIP_VERSION: Remove the derivation version byte from the frame
	// Expected error: "invalid derivation format byte" when derivation tries to read version
	case STRIP_VERSION:
		// Remove the version byte (first byte)
		strippedFrame := outputFrame[1:]

		if isArsia {
			// Arsia format: Use stripped frame directly (no RLP encoding)
			// Blob = [frame_data_without_version]
			finalData = strippedFrame
		} else {
			// Limb format: Wrap stripped frame in RLP array
			// Blob = RLP([[frame_data_without_version]])
			frameArray := []eth.Data{strippedFrame}
			rlpData, err := rlp.EncodeToBytes(frameArray)
			require.NoError(t, err, "failed to RLP encode stripped frame")
			finalData = rlpData
		}

	// RANDOM: Generate completely random data that is not valid for either format
	// Expected error: "rlp: expected input list" (Limb) or "invalid frame data" (Arsia)
	case RANDOM:
		// Generate random length between 1 and 512
		i, err := rand.Int(rand.Reader, big.NewInt(512))
		require.NoError(t, err, "error generating random bytes length")

		// Generate random bytes
		buf := make([]byte, i.Int64()+1)
		_, err = rand.Read(buf)
		require.NoError(t, err, "error generating random bytes")

		// Random data is invalid for both Limb and Arsia
		finalData = buf

	// TRUNCATE_END: Truncate data from the end to create incomplete data
	// Expected error: "rlp: unexpected EOF" (Limb) or "incomplete frame" (Arsia)
	case TRUNCATE_END:
		if isArsia {
			// Arsia format: Truncate the raw frame directly
			// This creates an incomplete frame that can't be parsed
			if len(outputFrame) > 4 {
				finalData = outputFrame[:len(outputFrame)-4]
			} else {
				finalData = []byte{}
			}
		} else {
			// Limb format: First RLP encode, then truncate
			// This creates incomplete RLP data
			frameArray := []eth.Data{outputFrame}
			rlpData, err := rlp.EncodeToBytes(frameArray)
			require.NoError(t, err, "failed to RLP encode frame for truncation")

			if len(rlpData) > 4 {
				finalData = rlpData[:len(rlpData)-4]
			} else {
				finalData = []byte{}
			}
		}

	// DIRTY_APPEND: Append garbage bytes to the end
	// Expected error: "rlp: input list has too many elements" (Limb) or "trailing bytes" (Arsia)
	case DIRTY_APPEND:
		if isArsia {
			// Arsia format: Append garbage bytes directly to the frame
			// This creates extra trailing data after the frame
			finalData = append(outputFrame, []byte{0xBA, 0xD0, 0xC0, 0xDE}...)
		} else {
			// Limb format: First RLP encode, then append garbage bytes
			// This creates RLP data with extra trailing bytes
			frameArray := []eth.Data{outputFrame}
			rlpData, err := rlp.EncodeToBytes(frameArray)
			require.NoError(t, err, "failed to RLP encode frame for dirty append")

			finalData = append(rlpData, []byte{0xBA, 0xD0, 0xC0, 0xDE}...)
		}

	// MALFORM_RLP: Create malformed RLP structure with invalid length field
	// Expected error: "rlp: invalid list length" or "rlp: size overflow"
	// Note: This is primarily for Limb (which uses RLP), but we generate it for both
	case MALFORM_RLP:
		// RLP list encoding:
		// - 0xc0-0xf7: short list (length 0-55), length = byte - 0xc0
		// - 0xf8-0xff: long list (length > 55), next bytes specify length

		// Create a long list marker (0xf9) with incorrect length
		// 0xf9 means: "long list, next 2 bytes are the length"
		// We claim the length is 0x1000 (4096 bytes) but provide much less data
		malformedRLP := []byte{
			0xf9,       // Long list marker (2-byte length follows)
			0x10, 0x00, // Claim length is 4096 bytes
		}

		// Append the actual frame data (which is much shorter than 4096)
		malformedRLP = append(malformedRLP, outputFrame...)

		// Use the same malformed RLP for both Arsia and Limb
		// - For Limb: RLP decoder will fail on invalid length
		// - For Arsia: Frame parser will fail on invalid data
		finalData = malformedRLP

	// INVALID_COMPRESSION: For testing invalid compression
	// This should be handled at the channel level by GarbageChannelOut
	// Here we just encode the frame normally
	case INVALID_COMPRESSION:
		if isArsia {
			// Arsia format: Use frame directly (no RLP encoding)
			// The compression issue is handled by GarbageChannelOut
			finalData = outputFrame
		} else {
			// Limb format: RLP encode the frame
			// The compression issue is handled by GarbageChannelOut
			frameArray := []eth.Data{outputFrame}
			rlpData, err := rlp.EncodeToBytes(frameArray)
			require.NoError(t, err, "failed to RLP encode frame for invalid compression")
			finalData = rlpData
		}

	default:
		t.Fatalf("Unexpected garbage kind: %v", kind)
	}

	// Submit the malformed blob data directly
	// We don't use ActL2BatchSubmitMantleRaw because:
	// 1. It would RLP encode again for Limb (double encoding)
	// 2. We've already prepared the exact blob data we want to submit
	// Instead, we directly create and submit the blob transaction

	// Get nonce for the batcher account
	nonce, err := s.l1.PendingNonceAt(t.Ctx(), s.BatcherAddr)
	require.NoError(t, err, "need batcher nonce")

	// Calculate gas prices
	gasTipCap := big.NewInt(2 * params.GWei)
	gasFeeCap := new(big.Int).Add(gasTipCap, new(big.Int).Mul(pendingHeader.BaseFee, big.NewInt(2)))

	// Convert finalData directly to blob (no additional encoding)
	// This is the raw blob data that will be submitted to L1
	var blob eth.Blob
	require.NoError(t, blob.FromData(finalData), "must turn garbage data into blob")
	blobs := []*eth.Blob{&blob}

	// Create blob transaction sidecar
	// This contains the blob data and KZG commitments
	sidecar, blobHashes, err := txmgr.MakeSidecar(blobs, s.l2BatcherCfg.EnableCellProofs)
	require.NoError(t, err, "failed to create sidecar")
	require.NotNil(t, pendingHeader.ExcessBlobGas, "need L1 header with 4844 properties")

	// Calculate blob fee cap (2x current blob base fee, minimum 1 gwei)
	blobBaseFee, err := s.l1.BlobBaseFee(t.Ctx())
	require.NoError(t, err, "need blob base fee")
	blobFeeCap := new(uint256.Int).Mul(uint256.NewInt(2), uint256.MustFromBig(blobBaseFee))
	if blobFeeCap.Lt(uint256.NewInt(params.GWei)) {
		blobFeeCap = uint256.NewInt(params.GWei)
	}

	// Create blob transaction (EIP-4844)
	txData := &types.BlobTx{
		To:         s.rollupCfg.BatchInboxAddress, // Batch inbox contract
		Data:       nil,                           // No calldata (data is in blobs)
		Gas:        params.TxGas,                  // 21000 gas for simple transfer
		BlobHashes: blobHashes,                    // KZG commitments to blobs
		Sidecar:    sidecar,                       // Blob data and proofs
		ChainID:    uint256.MustFromBig(s.rollupCfg.L1ChainID),
		GasTipCap:  uint256.MustFromBig(gasTipCap),
		GasFeeCap:  uint256.MustFromBig(gasFeeCap),
		BlobFeeCap: blobFeeCap,
		Value:      uint256.NewInt(0), // No ETH transfer
		Nonce:      nonce,
	}

	// Sign the transaction with the batcher's private key
	tx, err := types.SignNewTx(s.l2BatcherCfg.BatcherKey, s.l1Signer, txData)
	require.NoError(t, err, "need to sign tx")

	// Send the transaction to L1
	err = s.l1.SendTransaction(t.Ctx(), tx)
	require.NoError(t, err, "need to send tx")

	// Store the transaction for later reference (e.g., for inclusion in L1 blocks)
	s.LastSubmitted = tx
}
func (s *L2Batcher) ActBufferAll(t Testing) {
	stat, err := s.syncStatusAPI.SyncStatus(t.Ctx())
	require.NoError(t, err)
	s.log.Debug("ActBufferAll starting", "buffered", s.L2BufferedBlock.Number, "unsafe", stat.UnsafeL2.Number)
	for s.L2BufferedBlock.Number < stat.UnsafeL2.Number {
		s.ActL2BatchBuffer(t)
	}
	s.log.Debug("ActBufferAll finished", "buffered", s.L2BufferedBlock.Number, "channel_nil", s.L2ChannelOut == nil)
}

func (s *L2Batcher) ActSubmitAll(t Testing) {
	s.ActBufferAll(t)
	s.ActL2ChannelClose(t)
	s.ActL2BatchSubmit(t)
}

func (s *L2Batcher) ActSubmitAllMultiBlobs(t Testing, numBlobs int) {
	s.ActBufferAll(t)
	s.ActL2ChannelClose(t)
	s.ActL2BatchSubmitMultiBlob(t, numBlobs)
}

// ActSubmitSetCodeTx submits a SetCodeTx to the batch inbox. This models a malicious
// batcher and is only used to tests the derivation pipeline follows spec and ignores
// the SetCodeTx.
func (s *L2Batcher) ActSubmitSetCodeTx(t Testing) {
	chainId := *uint256.MustFromBig(s.rollupCfg.L1ChainID)

	nonce, err := s.l1.PendingNonceAt(t.Ctx(), s.BatcherAddr)
	require.NoError(t, err, "need batcher nonce")

	tx, err := PrepareSignedSetCodeTx(chainId, s.l2BatcherCfg.BatcherKey, s.l1Signer, nonce, s.rollupCfg.BatchInboxAddress, s.ReadNextOutputFrame(t))
	require.NoError(t, err, "need to sign tx")

	t.Log("submitting EIP 7702 Set Code Batcher Transaction...")
	err = s.l1.SendTransaction(t.Ctx(), tx)
	require.NoError(t, err, "need to send tx")
	s.LastSubmitted = tx
}

func PrepareSignedSetCodeTx(chainId uint256.Int, privateKey *ecdsa.PrivateKey, signer types.Signer, nonce uint64, to common.Address, data []byte) (*types.Transaction, error) {

	setCodeAuthorization := types.SetCodeAuthorization{
		ChainID: chainId,
		Address: common.HexToAddress("0xab"), // arbitrary nonzero address
		Nonce:   nonce,
	}

	signedAuth, err := types.SignSetCode(privateKey, setCodeAuthorization)
	if err != nil {
		return nil, err
	}

	txData := &types.SetCodeTx{
		ChainID:    &chainId,
		Nonce:      nonce,
		To:         to,
		Value:      uint256.NewInt(0),
		Data:       data,
		AccessList: types.AccessList{},
		AuthList:   []types.SetCodeAuthorization{signedAuth},
		Gas:        1_000_000,
		GasFeeCap:  uint256.NewInt(1_000_000_000),
	}

	return types.SignNewTx(privateKey, signer, txData)
}
