package fakepos

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math/big"
	"math/rand"
	"sync"
	"time"

	"github.com/ethereum-optimism/optimism/op-service/eth"
	opeth "github.com/ethereum-optimism/optimism/op-service/eth"
	"github.com/ethereum-optimism/optimism/op-service/testutils"
	"github.com/ethereum-optimism/optimism/op-test-sequencer/sequencer/backend/work"
	"github.com/ethereum-optimism/optimism/op-test-sequencer/sequencer/seqtypes"
	"github.com/ethereum/go-ethereum/beacon/engine"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/rpc"
)

type FakePoSEnvelope struct {
	engine.ExecutionPayloadEnvelope
}

func (e *FakePoSEnvelope) ID() eth.BlockID {
	return eth.BlockID{Hash: e.ExecutionPayload.BlockHash, Number: e.ExecutionPayload.Number}
}

func (e *FakePoSEnvelope) String() string {
	return e.ID().String()
}

type Job struct {
	id     seqtypes.BuildJobID
	mu     sync.Mutex
	logger log.Logger

	b *Builder

	parent   common.Hash
	envelope engine.ExecutionPayloadEnvelope

	head      *types.Header
	safe      *types.Header
	finalized *types.Header

	parentBeaconBlockRoot common.Hash

	// fork flags for the block being built; computed in Open, reused in Seal to
	// select the engine-API version for newPayload/forkchoiceUpdated.
	isCancun    bool
	isPrague    bool
	isAmsterdam bool
}

func (j *Job) ID() seqtypes.BuildJobID {
	return j.id
}

func (j *Job) Cancel(ctx context.Context) error {
	return nil
}

func (j *Job) setHeadSafeAndFinalized() {
	var err error
	j.head, err = j.b.blockchain.HeaderByNumber(context.Background(), nil)
	if err != nil {
		panic("chain head not found")
	}
	if j.parent != (common.Hash{}) {
		j.head, err = j.b.blockchain.HeaderByHash(context.Background(), j.parent) // override head if parent is set
		if err != nil {
			panic("chain head's parent not found")
		}
	}

	j.finalized, err = j.b.blockchain.HeaderByNumber(context.Background(), new(big.Int).SetInt64(int64(rpc.FinalizedBlockNumber)))
	if err != nil { // fallback to genesis if nothing is finalized
		j.finalized = j.b.genesis
	}
	j.safe, err = j.b.blockchain.HeaderByNumber(context.Background(), new(big.Int).SetInt64(int64(rpc.SafeBlockNumber)))
	if err != nil { // fallback to finalized if nothing is safe
		j.safe = j.finalized
	}

	if j.head.Number.Uint64() > j.b.finalizedDistance { // progress finalized block, if we can
		j.finalized, err = j.b.blockchain.HeaderByNumber(context.Background(), new(big.Int).SetUint64(j.head.Number.Uint64()-j.b.finalizedDistance))
		if err != nil {
			panic("no block found finalizedDistance behind head")
		}
	}
	if j.head.Number.Uint64() > j.b.safeDistance { // progress safe block, if we can
		j.safe, err = j.b.blockchain.HeaderByNumber(context.Background(), new(big.Int).SetUint64(j.head.Number.Uint64()-j.b.safeDistance))
		if err != nil {
			panic("no block found safeDistance behind head")
		}
	}

	j.parentBeaconBlockRoot = fakeBeaconBlockRoot(j.head.Time) // parent beacon block root
}

func (j *Job) Open(ctx context.Context) error {
	j.mu.Lock()
	defer j.mu.Unlock()

	j.logger.Info("Open job", "id", j.id)

	j.setHeadSafeAndFinalized()

	newBlockTime := j.head.Time + j.b.blockTime

	// Select the fork for the block we are about to build. This must run for every
	// path (including reorg rebuilds), so Seal picks the matching newPayload /
	// forkchoiceUpdated version and Open sets the right payload attributes.
	nextHeight := new(big.Int).SetUint64(j.head.Number.Uint64() + 1)
	cfg := j.b.l1ChainConfig
	j.isCancun = cfg.IsCancun(nextHeight, newBlockTime)
	j.isPrague = cfg.IsPrague(nextHeight, newBlockTime)
	isOsaka := cfg.IsOsaka(nextHeight, newBlockTime)
	j.isAmsterdam = cfg.IsAmsterdam(nextHeight, newBlockTime)

	envelope, rebuilt := j.b.envelopes[j.head.Hash()]
	// Build a fresh block when we have not built on this parent yet, or when we
	// need a different post-Amsterdam competing block for a reorg. The gas-bump
	// rehash in the else branch cannot recompute the EIP-7928 block-access-list
	// hash (nor re-key it in the engine's BAL map), so post-Amsterdam we always
	// rebuild via the engine, using a distinct fee recipient to diverge from the
	// original block.
	if !rebuilt || j.isAmsterdam {
		feeRecipient := j.head.Coinbase
		if rebuilt {
			// The seed MUST vary per rebuild, not just per parent. Seeding on
			// newBlockTime alone (= parent.Time + blockTime) is constant for a given
			// parent, so the 2nd and 3rd competing blocks at the same height would get
			// the SAME fee recipient. With the timestamp, parent, and prevRandao also
			// identical, the only remaining source of divergence would be
			// randomWithdrawals -- which returns an EMPTY list 1 time in 4, so two
			// successive rebuilds would produce a byte-identical block roughly 1 time
			// in 16 and the "reorg" would silently not fork. Mixing in a monotonic
			// rebuild counter makes each competing block distinct by construction.
			j.b.rebuilds++
			feeRecipient = testutils.RandomAddress(rand.New(rand.NewSource(int64(newBlockTime) + int64(j.b.rebuilds))))
		}

		attrs := &engine.PayloadAttributes{
			Timestamp:             newBlockTime,
			Random:                common.Hash{},
			SuggestedFeeRecipient: feeRecipient,
			Withdrawals:           randomWithdrawals(j.b.withdrawalsIndex),
		}
		if j.isCancun {
			attrs.BeaconRoot = &j.parentBeaconBlockRoot
		}
		if j.isAmsterdam {
			// Amsterdam (EIP-7843/EIP-7928): geth's forkchoiceUpdatedV4 rejects the
			// build request (STATUS_INVALID) unless both slotNumber and
			// targetGasLimit are present in the payload attributes.
			slotNumber := (newBlockTime - j.b.genesis.Time) / j.b.blockTime
			attrs.SlotNumber = &slotNumber
			targetGasLimit := j.head.GasLimit
			attrs.GasLimit = &targetGasLimit
		}
		fcState := engine.ForkchoiceStateV1{
			HeadBlockHash:      j.head.Hash(),
			SafeBlockHash:      j.safe.Hash(),
			FinalizedBlockHash: j.finalized.Hash(),
		}
		j.logger.Info("ForkchoiceUpdated (start build)", "fcState", fcState, "isOsaka", isOsaka, "isAmsterdam", j.isAmsterdam)

		var res engine.ForkChoiceResponse
		var err error
		if j.isAmsterdam {
			res, err = j.b.engine.ForkchoiceUpdatedV4(ctx, fcState, attrs)
		} else if j.isCancun {
			res, err = j.b.engine.ForkchoiceUpdatedV3(ctx, fcState, attrs)
		} else {
			res, err = j.b.engine.ForkchoiceUpdatedV2(ctx, fcState, attrs)
		}
		if err != nil {
			j.logger.Error("failed to start building L1 block", "err", err)
			return err
		}
		if err := payloadStatusErr(res.PayloadStatus); err != nil {
			j.logger.Error("engine rejected L1 forkchoice update (start build)", "err", err)
			return err
		}
		if res.PayloadID == nil {
			j.logger.Error("failed to start block building", "res", res)
			return errors.New("failed to start block building")
		}

		j.logger.Info("got res.payloadID", "res.payloadID", res.PayloadID)

		// wait for the block building to finish
		time.Sleep(100 * time.Millisecond)

		if j.isAmsterdam {
			envelope, err = j.b.engine.GetPayloadV6(*res.PayloadID)
		} else if isOsaka {
			envelope, err = j.b.engine.GetPayloadV5(*res.PayloadID)
		} else if j.isPrague {
			envelope, err = j.b.engine.GetPayloadV4(*res.PayloadID)
		} else if j.isCancun {
			envelope, err = j.b.engine.GetPayloadV3(*res.PayloadID)
		} else {
			envelope, err = j.b.engine.GetPayloadV2(*res.PayloadID)
		}
		if err != nil {
			j.logger.Error("failed to finish building L1 block", "err", err)
			return err
		}

		j.b.envelopes[envelope.ExecutionPayload.ParentHash] = envelope
	} else {
		j.logger.Warn("already had a block with that parent", "parent", j.head.Hash(), "number", j.head.Number.Uint64(), "fee_recipient", envelope.ExecutionPayload.FeeRecipient)

		j.logger.Warn("updating block hash", "pre", envelope.ExecutionPayload.BlockHash, "fee_recipient", envelope.ExecutionPayload.FeeRecipient, "txs", len(envelope.ExecutionPayload.Transactions))

		// modify gas limit so that we get a different block
		envelope.ExecutionPayload.GasLimit = envelope.ExecutionPayload.GasLimit + 100

		block, err := engine.ExecutableDataToBlockNoHash(*envelope.ExecutionPayload, make([]common.Hash, 0), &j.parentBeaconBlockRoot, make([][]byte, 0), j.b.config)
		if err != nil {
			j.logger.Error("failed to convert executable data to block", "err", err)
			return err
		}
		envelope.ExecutionPayload.BlockHash = block.Hash()

		j.logger.Warn("updated block hash", "post", envelope.ExecutionPayload.BlockHash, "fee_recipient", envelope.ExecutionPayload.FeeRecipient, "txs", len(envelope.ExecutionPayload.Transactions))
	}

	j.logger.Info("final envelope", "envelope", envelope)

	return nil
}

func (j *Job) Seal(ctx context.Context) (work.Block, error) {
	j.mu.Lock()
	defer j.mu.Unlock()

	envelope, ok := j.b.envelopes[j.head.Hash()]
	if !ok { // we haven't build a block with this parent yet, so we need to build one
		return nil, errors.New("no envelope found")
	}

	blobHashes := make([]common.Hash, 0) // must be non-nil even when empty, due to geth engine API checks
	if envelope.BlobsBundle != nil {
		for _, commitment := range envelope.BlobsBundle.Commitments {
			if len(commitment) != 48 {
				j.logger.Error("got malformed kzg commitment from engine", "commitment", commitment)
				break
			}
			blobHashes = append(blobHashes, opeth.KZGToVersionedHash(*(*[48]byte)(commitment)))
		}
		if len(blobHashes) != len(envelope.BlobsBundle.Commitments) {
			j.logger.Error("invalid or incomplete blob data", "collected", len(blobHashes), "engine", len(envelope.BlobsBundle.Commitments))
			return nil, errors.New("invalid or incomplete blob data")
		}
	}

	j.logger.Info("about to insert payload into the chain", "envelope-hash", envelope.ExecutionPayload.BlockHash, "txs", len(envelope.ExecutionPayload.Transactions))

	var payloadStatus engine.PayloadStatusV1
	var err error
	if j.isAmsterdam {
		payloadStatus, err = j.b.engine.NewPayloadV5(ctx, *envelope.ExecutionPayload, blobHashes, &j.parentBeaconBlockRoot, make([]hexutil.Bytes, 0))
	} else if j.isPrague {
		payloadStatus, err = j.b.engine.NewPayloadV4(ctx, *envelope.ExecutionPayload, blobHashes, &j.parentBeaconBlockRoot, make([]hexutil.Bytes, 0))
	} else if j.isCancun {
		payloadStatus, err = j.b.engine.NewPayloadV3(ctx, *envelope.ExecutionPayload, blobHashes, &j.parentBeaconBlockRoot)
	} else {
		payloadStatus, err = j.b.engine.NewPayloadV2(ctx, *envelope.ExecutionPayload)
	}
	if err != nil {
		j.logger.Error("failed to insert built L1 block", "err", err)
		return nil, err
	}
	if err := payloadStatusErr(payloadStatus); err != nil {
		j.logger.Error("engine rejected built L1 payload", "err", err, "block", envelope.ExecutionPayload.BlockHash)
		return nil, err
	}

	if envelope.BlobsBundle != nil {
		slot := (envelope.ExecutionPayload.Timestamp - j.b.genesis.Time) / j.b.blockTime
		if j.b.beacon == nil {
			j.logger.Error("no blobs storage available")
			return nil, errors.New("no blobs storage available")
		}
		if err := j.b.beacon.StoreBlobsBundle(slot, envelope.BlobsBundle); err != nil {
			j.logger.Error("failed to persist blobs-bundle of block, not making block canonical now", "err", err)
			return nil, err
		}
	}

	j.logger.Info("about to forkchoice update", "safe", j.safe.Hash(), "finalized", j.finalized.Hash(), "head", envelope.ExecutionPayload.BlockHash)

	finalFcState := engine.ForkchoiceStateV1{
		HeadBlockHash:      envelope.ExecutionPayload.BlockHash,
		SafeBlockHash:      j.safe.Hash(),
		FinalizedBlockHash: j.finalized.Hash(),
	}
	var fcRes engine.ForkChoiceResponse
	if j.isAmsterdam {
		fcRes, err = j.b.engine.ForkchoiceUpdatedV4(ctx, finalFcState, nil)
	} else {
		fcRes, err = j.b.engine.ForkchoiceUpdatedV3(ctx, finalFcState, nil)
	}
	if err != nil {
		j.logger.Error("failed to make built L1 block canonical", "err", err)
		return nil, err
	}
	if err := payloadStatusErr(fcRes.PayloadStatus); err != nil {
		j.logger.Error("engine rejected final L1 forkchoice update", "err", err)
		return nil, err
	}

	j.b.withdrawalsIndex += uint64(len(envelope.ExecutionPayload.Withdrawals))

	j.logger.Info("incrementing withdrawals index", "index", j.b.withdrawalsIndex)

	j.envelope = *envelope

	return &FakePoSEnvelope{ExecutionPayloadEnvelope: j.envelope}, nil
}

func (job *Job) String() string {
	return job.id.String()
}

func (job *Job) Close() {
}

func (job *Job) IncludeTx(ctx context.Context, tx hexutil.Bytes) error {
	return errors.New("not implemented")
}

var _ work.BuildJob = (*Job)(nil)

func payloadStatusErr(status engine.PayloadStatusV1) error {
	if status.Status == engine.VALID {
		return nil
	}
	validationErr := "<nil>"
	if status.ValidationError != nil {
		validationErr = *status.ValidationError
	}
	return fmt.Errorf("engine payload status %q, latestValidHash=%v, validationError=%s", status.Status, status.LatestValidHash, validationErr)
}

func fakeBeaconBlockRoot(time uint64) common.Hash {
	var dat [8]byte
	binary.LittleEndian.PutUint64(dat[:], time)
	return crypto.Keccak256Hash(dat[:])
}

func randomWithdrawals(startIndex uint64) []*types.Withdrawal {
	r := rand.New(rand.NewSource(time.Now().UnixNano()))
	withdrawals := make([]*types.Withdrawal, r.Intn(4))
	for i := 0; i < len(withdrawals); i++ {
		withdrawals[i] = &types.Withdrawal{
			Index:     startIndex + uint64(i),
			Validator: r.Uint64() % 100_000_000, // 100 million fake validators
			Address:   testutils.RandomAddress(r),
			Amount:    uint64(r.Intn(50_000_000_000) + 1),
		}
	}
	return withdrawals
}
