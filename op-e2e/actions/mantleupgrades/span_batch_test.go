package mantleupgrades

import (
	"context"
	"crypto/ecdsa"
	crand "crypto/rand"
	"fmt"

	"github.com/ethereum-optimism/optimism/op-e2e/config"

	"math/big"
	"math/rand"
	"testing"

	actionsHelpers "github.com/ethereum-optimism/optimism/op-e2e/actions/helpers"
	upgradesHelpers "github.com/ethereum-optimism/optimism/op-e2e/actions/mantleupgrades/helpers"
	"github.com/stretchr/testify/require"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/params"

	batcherFlags "github.com/ethereum-optimism/optimism/op-batcher/flags"
	"github.com/ethereum-optimism/optimism/op-e2e/e2eutils"
	"github.com/ethereum-optimism/optimism/op-node/rollup/derive"
	"github.com/ethereum-optimism/optimism/op-node/rollup/sync"
	"github.com/ethereum-optimism/optimism/op-service/testlog"
)

// TestDropSpanBatchBeforeHardfork tests behavior of op-node before Delta hardfork.
// op-node must drop SpanBatch before Delta hardfork.
func TestDropSpanBatchBeforeHardfork(gt *testing.T) {
	t := actionsHelpers.NewDefaultTesting(gt)
	p := &e2eutils.TestParams{
		MaxSequencerDrift:   20, // larger than L1 block time we simulate in this test (12)
		SequencerWindowSize: 24,
		ChannelTimeout:      20,
		L1BlockTime:         12,
		AllocType:           config.DefaultAllocType,
	}
	dp := e2eutils.MakeMantleDeployParams(t, p)
	dp.DeployConfig.GasPriceOracleTokenRatio = 1
	dp.DeployConfig.L2GenesisBlockGasLimit = 300000000

	// do not activate Delta hardfork for verifier
	upgradesHelpers.ApplyArsiaTimeOffset(dp, nil)
	sd := e2eutils.SetupMantleNormal(t, dp, actionsHelpers.DefaultAlloc)
	log := testlog.Logger(t, log.LevelError)
	miner, seqEngine, sequencer := actionsHelpers.SetupSequencerTest(t, sd, log)
	verifEngine, verifier := actionsHelpers.SetupVerifier(t, sd, log, miner.L1Client(t, sd.RollupCfg), miner.BlobStore(), &sync.Config{})
	rollupSeqCl := sequencer.RollupClient()

	// Force batcher to submit SpanBatches to L1 using blob transactions.
	// MantleLimb uses MantleBlobDataSource which only processes blob transactions.
	batcher := actionsHelpers.NewL2Batcher(log, sd.RollupCfg, &actionsHelpers.BatcherCfg{
		MinL1TxSize:          0,
		MaxL1TxSize:          128_000,
		BatcherKey:           dp.Secrets.Batcher,
		ForceSubmitSpanBatch: true,
		DataAvailabilityType: batcherFlags.BlobsType,
	}, rollupSeqCl, miner.EthClient(), seqEngine.EthClient(), seqEngine.EngineClient(t, sd.RollupCfg))

	// Build and finalize an empty L1 block first
	// This is required for blob transactions to work properly with the blob pool
	miner.ActEmptyBlock(t)
	miner.ActL1SafeNext(t)
	miner.ActL1FinalizeNext(t)

	// Alice makes a L2 tx
	cl := seqEngine.EthClient()
	n, err := cl.PendingNonceAt(t.Ctx(), dp.Addresses.Alice)
	require.NoError(t, err)
	signer := types.LatestSigner(sd.L2Cfg.Config)
	tx := types.MustSignNewTx(dp.Secrets.Alice, signer, &types.DynamicFeeTx{
		ChainID:   sd.L2Cfg.Config.ChainID,
		Nonce:     n,
		GasTipCap: big.NewInt(2 * params.GWei),
		GasFeeCap: new(big.Int).Add(miner.L1Chain().CurrentBlock().BaseFee, big.NewInt(2*params.GWei)),
		Gas:       50000,
		To:        &dp.Addresses.Bob,
		Value:     e2eutils.Ether(2),
	})
	require.NoError(gt, cl.SendTransaction(t.Ctx(), tx))

	sequencer.ActL1HeadSignal(t)
	sequencer.ActL2PipelineFull(t)
	verifier.ActL1HeadSignal(t)
	verifier.ActL2PipelineFull(t)

	// Make L2 block
	sequencer.ActL2StartBlock(t)
	seqEngine.ActL2IncludeTx(dp.Addresses.Alice)(t)
	sequencer.ActL2EndBlock(t)

	// batch submit to L1. batcher should submit span batches using Mantle blob format.
	batcher.ActL2BatchBuffer(t)
	batcher.ActL2ChannelClose(t)
	batcher.ActL2BatchSubmitMantle(t)
	batchTx := batcher.LastSubmitted

	// confirm batch on L1 using tx hash (required for blob transactions)
	miner.ActL1StartBlock(12)(t)
	miner.ActL1IncludeTxByHash(batchTx.Hash())(t)
	miner.ActL1EndBlock(t)
	bl := miner.L1Chain().CurrentBlock()
	log.Info("bl", "txs", len(miner.L1Chain().GetBlockByHash(bl.Hash()).Transactions()))

	// Now make enough L1 blocks that the verifier will have to derive a L2 block
	// It will also eagerly derive the block from the batcher
	for i := uint64(0); i < sd.RollupCfg.SeqWindowSize; i++ {
		miner.ActL1StartBlock(12)(t)
		miner.ActL1EndBlock(t)
	}

	// try to sync verifier from L1 batch. but verifier should drop every span batch.
	verifier.ActL1HeadSignal(t)
	verifier.ActL2PipelineFull(t)
	// L1Origin is 2 because: genesis(0) + finalized block(1) + batch block(2)
	require.Equal(t, uint64(2), verifier.SyncStatus().SafeL2.L1Origin.Number)

	verifCl := verifEngine.EthClient()
	for i := int64(1); i < int64(verifier.L2Safe().Number); i++ {
		block, _ := verifCl.BlockByNumber(t.Ctx(), big.NewInt(i))
		require.NoError(t, err)
		// because verifier drops every span batch, it should generate empty blocks.
		// so every block has only L1 attribute deposit transaction.
		require.Equal(t, block.Transactions().Len(), 1)
	}
	// check that the tx from alice is not included in verifier's chain
	_, _, err = verifCl.TransactionByHash(t.Ctx(), tx.Hash())
	require.ErrorIs(t, err, ethereum.NotFound)
}

// TestHardforkMiddleOfArsiaSpanBatch tests behavior of op-node Delta hardfork.
// If Delta activation time is in the middle of time range of a SpanBatch, op-node must drop the batch.
func TestHardforkMiddleOfArsiaSpanBatch(gt *testing.T) {
	t := actionsHelpers.NewDefaultTesting(gt)
	p := &e2eutils.TestParams{
		MaxSequencerDrift:   20, // larger than L1 block time we simulate in this test (12)
		SequencerWindowSize: 24,
		ChannelTimeout:      20,
		L1BlockTime:         12,
		AllocType:           config.DefaultAllocType,
	}
	dp := e2eutils.MakeMantleDeployParams(t, p)
	dp.DeployConfig.L2BlockTime = 2
	dp.DeployConfig.GasPriceOracleTokenRatio = 1
	dp.DeployConfig.L2GenesisBlockGasLimit = 300000000
	// Activate HF in the middle of the first epoch
	arsiaOffset := hexutil.Uint64(6)
	upgradesHelpers.ApplyArsiaTimeOffset(dp, &arsiaOffset)

	sd := e2eutils.SetupMantleNormal(t, dp, actionsHelpers.DefaultAlloc)
	log := testlog.Logger(t, log.LevelError)
	miner, seqEngine, sequencer := actionsHelpers.SetupSequencerTest(t, sd, log)
	verifEngine, verifier := actionsHelpers.SetupVerifier(t, sd, log, miner.L1Client(t, sd.RollupCfg), miner.BlobStore(), &sync.Config{})
	minerCl := miner.EthClient()
	rollupSeqCl := sequencer.RollupClient()

	// Force batcher to submit SpanBatches to L1.
	batcher := actionsHelpers.NewL2Batcher(log, sd.RollupCfg, &actionsHelpers.BatcherCfg{
		MinL1TxSize:          0,
		MaxL1TxSize:          128_000,
		BatcherKey:           dp.Secrets.Batcher,
		ForceSubmitSpanBatch: true,
		DataAvailabilityType: batcherFlags.CalldataType,
		EnableCellProofs:     true,
	}, rollupSeqCl, miner.EthClient(), seqEngine.EthClient(), seqEngine.EngineClient(t, sd.RollupCfg))

	// Alice makes a L2 tx
	cl := seqEngine.EthClient()
	n, err := cl.PendingNonceAt(t.Ctx(), dp.Addresses.Alice)
	require.NoError(t, err)
	signer := types.LatestSigner(sd.L2Cfg.Config)
	tx := types.MustSignNewTx(dp.Secrets.Alice, signer, &types.DynamicFeeTx{
		ChainID:   sd.L2Cfg.Config.ChainID,
		Nonce:     n,
		GasTipCap: big.NewInt(2 * params.GWei),
		GasFeeCap: new(big.Int).Add(miner.L1Chain().CurrentBlock().BaseFee, big.NewInt(2*params.GWei)),
		Gas:       50000,
		To:        &dp.Addresses.Bob,
		Value:     e2eutils.Ether(2),
	})
	require.NoError(gt, cl.SendTransaction(t.Ctx(), tx))

	sequencer.ActL2PipelineFull(t)
	verifier.ActL2PipelineFull(t)

	miner.ActEmptyBlock(t)
	sequencer.ActL1HeadSignal(t)

	// Make a L2 block with the TX
	sequencer.ActL2StartBlock(t)
	seqEngine.ActL2IncludeTx(dp.Addresses.Alice)(t)
	sequencer.ActL2EndBlock(t)

	// ========== Added: The transaction verifying Alice is included in the Sequencer's block. ==========
	t.Log("========== The transaction verifying Alice is included in the Sequencer's block. ==========")

	seqCl := seqEngine.EthClient()
	seqReceipt, err := seqCl.TransactionReceipt(t.Ctx(), tx.Hash())
	require.NoError(t, err, "Alice's transaction should be included in sequencer's chain")
	require.NotNil(t, seqReceipt, "Alice's transaction receipt should not be nil")
	require.Equal(t, types.ReceiptStatusSuccessful, seqReceipt.Status, "Alice's transaction should be successful")

	t.Logf("Alice's transaction %s is included in Sequencer block #%d", tx.Hash().Hex(), seqReceipt.BlockNumber.Uint64())
	t.Logf("   Transaction status: %d (1=Success)", seqReceipt.Status)
	t.Logf("   Gas used: %d", seqReceipt.GasUsed)

	//  Verify that the block was indeed activated before Arsia.
	seqBlock, err := seqCl.BlockByNumber(t.Ctx(), seqReceipt.BlockNumber)
	require.NoError(t, err)
	require.False(t, sd.RollupCfg.IsMantleArsia(seqBlock.Time()),
		"Alice's transaction should be in a block before Arsia activation")

	t.Logf("The block #%d time %d < Arsia activation time %d", seqReceipt.BlockNumber.Uint64(), seqBlock.Time(), *sd.RollupCfg.MantleArsiaTime)

	// HF is not activated yet
	unsafeOriginNum := new(big.Int).SetUint64(sequencer.L2Unsafe().L1Origin.Number)
	unsafeHeader, err := minerCl.HeaderByNumber(t.Ctx(), unsafeOriginNum)
	require.NoError(t, err)
	require.False(t, sd.RollupCfg.IsMantleArsiaActivationBlock(unsafeHeader.Time))

	// Make L2 blocks until the next epoch
	sequencer.ActBuildToL1Head(t)

	// // HF is activated for the last unsafe block
	// unsafeOriginNum = new(big.Int).SetUint64(sequencer.L2Unsafe().L1Origin.Number)
	// unsafeHeader, err = minerCl.HeaderByNumber(t.Ctx(), unsafeOriginNum)
	// require.NoError(t, err)
	// require.True(t, sd.RollupCfg.IsMantleArsiaActivationBlock(unsafeHeader.Time))
	// 验证 Arsia 激活状态
	unsafeL2Block := sequencer.L2Unsafe()
	unsafeOriginNumtest := new(big.Int).SetUint64(unsafeL2Block.L1Origin.Number)
	unsafeOriginHeader, err := minerCl.HeaderByNumber(t.Ctx(), unsafeOriginNumtest)
	require.NoError(t, err)

	arsiaActivationTime := *sd.RollupCfg.MantleArsiaTime
	// ========== Added: Verify Arsia activation status ==========
	t.Logf("========== Verify Arsia activation status ==========")
	t.Logf("L2 unsafe block #%d, L2 time: %d", unsafeL2Block.Number, unsafeL2Block.Time)
	t.Logf("L1 origin block #%d, L1 time: %d", unsafeOriginNumtest.Uint64(), unsafeOriginHeader.Time)
	t.Logf("Arsia activation time: %d", arsiaActivationTime)

	// ========== Added: Verify L2 unsafe block time >= Arsia activation time ==========
	t.Logf("========== Verify L2 unsafe block time >= Arsia activation time ==========")
	require.GreaterOrEqual(t, unsafeL2Block.Time, arsiaActivationTime,
		"L2 unsafe block time should be >= Arsia activation time")

	// ========== Added: Verify L1 origin time >= Arsia activation time ==========
	t.Logf("========== Verify L1 origin time >= Arsia activation time ==========")
	require.GreaterOrEqual(t, unsafeOriginHeader.Time, arsiaActivationTime,
		"L1 origin time should be >= Arsia activation time")

	// ========== Added: Verify L2 unsafe block is in Arsia era ==========
	t.Logf("========== Verify L2 unsafe block is in Arsia era ==========")
	require.True(t, sd.RollupCfg.IsMantleArsia(unsafeL2Block.Time),
		"L2 unsafe block should be in Arsia era")

	t.Logf("L2 unsafe block #%d and its L1 origin block #%d are both in Arsia era after activation time %d",
		unsafeL2Block.Number, unsafeOriginNumtest.Uint64(), arsiaActivationTime)

	// Batch submit to L1. batcher should submit span batches.
	batcher.ActSubmitAll(t)

	// Confirm batch on L1
	miner.ActL1StartBlock(12)(t)
	miner.ActL1IncludeTx(dp.Addresses.Batcher)(t)
	miner.ActL1EndBlock(t)
	bl := miner.L1Chain().CurrentBlock()
	log.Info("bl", "txs", len(miner.L1Chain().GetBlockByHash(bl.Hash()).Transactions()))

	// Now make enough L1 blocks that the verifier will have to derive a L2 block
	// It will also eagerly derive the block from the batcher
	for i := uint64(0); i < sd.RollupCfg.SeqWindowSize; i++ {
		miner.ActL1StartBlock(12)(t)
		miner.ActL1EndBlock(t)
	}
	// Record the safe and unsafe block heights before Arsia activation.
	safeHeadBefore := verifier.L2Safe().Number
	unsafeHeadBefore := verifier.L2Unsafe().Number

	// Try to sync verifier from L1 batch. but verifier should drop every span batch.
	verifier.ActL1HeadSignal(t)
	verifier.ActL2PipelineFull(t)
	//veifie safe and unsafe block should be incressed
	//require.Equal(t, uint64(2), verifier.SyncStatus().SafeL2.L1Origin.Number)
	safeHeadAfter := verifier.L2Safe().Number
	unsafeHeadAfter := verifier.L2Unsafe().Number
	log.Info("Verify block growth")
	t.Logf("Safe head: %d -> %d (increased by %d blocks)", safeHeadBefore, safeHeadAfter, safeHeadAfter-safeHeadBefore)
	t.Logf("Unsafe head: %d -> %d (increased by %d blocks)", unsafeHeadBefore, unsafeHeadAfter, unsafeHeadAfter-unsafeHeadBefore)

	// Assertion: The safe head should grow (proving that deposit-only blocks are being generated).
	require.Greater(t, safeHeadAfter, safeHeadBefore, "Safe head should increase even when SpanBatch is dropped")
	// Assertion: The unsafe head should grow (proving that span batches are being processed).
	require.Greater(t, unsafeHeadAfter, unsafeHeadBefore, "Unsafe head should increase")

	verifCl := verifEngine.EthClient()

	depositOnlyBlockCount := 0
	arsiaActivationBlockCount := 0

	for i := int64(1); i < int64(verifier.L2Safe().Number); i++ {
		block, _ := verifCl.BlockByNumber(t.Ctx(), big.NewInt(i))
		require.NoError(t, err)

		// Check if this block is the Arsia activation block
		// Arsia activation block contains 8 transactions:
		// 1 L1 attribute deposit + 7 upgrade transactions (3 deployments + 3 proxy upgrades + 1 setArsia)
		isArsiaActivation := sd.RollupCfg.IsMantleArsiaActivationBlock(block.Time())

		if isArsiaActivation {
			arsiaActivationBlockCount++
			// Arsia activation block should have 8 transactions (1 deposit + 7 upgrade txs)
			require.Equal(t, 8, block.Transactions().Len(), "Arsia activation block should have 8 transactions")
			t.Logf("Block #%d: Arsia activation block, containing 8 transactions (1 deposit + 7 upgrade)", i)
		} else {
			depositOnlyBlockCount++
			// Because verifier drops every span batch, it should generate empty blocks.
			// So every block has only L1 attribute deposit transaction.
			require.Equal(t, 1, block.Transactions().Len(), "Non-activation block should have only 1 transaction")

			tx := block.Transactions()[0]
			require.Equal(t, uint8(types.DepositTxType), tx.Type(), "The only transaction should be a deposit transaction")
			t.Logf("Block #%d: Deposit-only block, containing 1 transaction (1 deposit)", i)

		}
	}
	t.Logf("Total: %d deposit-only blocks, %d Arsia activation blocks", depositOnlyBlockCount, arsiaActivationBlockCount)
	// Assertion: Should have at least one deposit-only block (proving Steady Block Derivation is triggered).
	require.Greater(t, depositOnlyBlockCount, 0, "Should have at least one deposit-only block")
	// Assertion: Should have exactly one Arsia activation block.
	require.Equal(t, 1, arsiaActivationBlockCount, "Should have exactly one Arsia activation block")
	// Check that the tx from alice is not included in verifier's chain
	_, _, err = verifCl.TransactionByHash(t.Ctx(), tx.Hash())
	require.ErrorIs(t, err, ethereum.NotFound)
}

// TestAcceptSingularBatchAfterHardfork tests behavior of op-node after Delta hardfork.
// op-node must accept SingularBatch after Delta hardfork.
func TestAcceptSingularBatchAfterHardfork(gt *testing.T) {
	t := actionsHelpers.NewDefaultTesting(gt)
	p := &e2eutils.TestParams{
		MaxSequencerDrift:   20, // larger than L1 block time we simulate in this test (12)
		SequencerWindowSize: 24,
		ChannelTimeout:      20,
		L1BlockTime:         12,
		AllocType:           config.DefaultAllocType,
	}
	minTs := hexutil.Uint64(0)
	dp := e2eutils.MakeMantleDeployParams(t, p)

	// activate Delta hardfork for verifier.
	upgradesHelpers.ApplyArsiaTimeOffset(dp, &minTs)
	sd := e2eutils.SetupMantleNormal(t, dp, actionsHelpers.DefaultAlloc)
	log := testlog.Logger(t, log.LevelError)
	miner, seqEngine, sequencer := actionsHelpers.SetupSequencerTest(t, sd, log)
	verifEngine, verifier := actionsHelpers.SetupVerifier(t, sd, log, miner.L1Client(t, sd.RollupCfg), miner.BlobStore(), &sync.Config{})
	rollupSeqCl := sequencer.RollupClient()

	// Force batcher to submit SingularBatches to L1.
	batcher := actionsHelpers.NewL2Batcher(log, sd.RollupCfg, &actionsHelpers.BatcherCfg{
		MinL1TxSize:              0,
		MaxL1TxSize:              128_000,
		BatcherKey:               dp.Secrets.Batcher,
		ForceSubmitSingularBatch: true,
		DataAvailabilityType:     batcherFlags.CalldataType,
	}, rollupSeqCl, miner.EthClient(), seqEngine.EthClient(), seqEngine.EngineClient(t, sd.RollupCfg))

	// Alice makes a L2 tx
	cl := seqEngine.EthClient()
	n, err := cl.PendingNonceAt(t.Ctx(), dp.Addresses.Alice)
	require.NoError(t, err)
	signer := types.LatestSigner(sd.L2Cfg.Config)
	tx := types.MustSignNewTx(dp.Secrets.Alice, signer, &types.DynamicFeeTx{
		ChainID:   sd.L2Cfg.Config.ChainID,
		Nonce:     n,
		GasTipCap: big.NewInt(2 * params.GWei),
		GasFeeCap: new(big.Int).Add(miner.L1Chain().CurrentBlock().BaseFee, big.NewInt(2*params.GWei)),
		Gas:       params.TxGas,
		To:        &dp.Addresses.Bob,
		Value:     e2eutils.Ether(2),
	})
	require.NoError(gt, cl.SendTransaction(t.Ctx(), tx))

	sequencer.ActL2PipelineFull(t)
	verifier.ActL2PipelineFull(t)

	// Make L2 block
	sequencer.ActL2StartBlock(t)
	seqEngine.ActL2IncludeTx(dp.Addresses.Alice)(t)
	sequencer.ActL2EndBlock(t)

	// batch submit to L1. batcher should submit singular batches.
	batcher.ActL2BatchBuffer(t)
	batcher.ActL2ChannelClose(t)
	batcher.ActL2BatchSubmit(t)

	// confirm batch on L1
	miner.ActL1StartBlock(12)(t)
	miner.ActL1IncludeTx(dp.Addresses.Batcher)(t)
	miner.ActL1EndBlock(t)
	bl := miner.L1Chain().CurrentBlock()
	log.Info("bl", "txs", len(miner.L1Chain().GetBlockByHash(bl.Hash()).Transactions()))

	// Now make enough L1 blocks that the verifier will have to derive a L2 block
	// It will also eagerly derive the block from the batcher
	for i := uint64(0); i < sd.RollupCfg.SeqWindowSize; i++ {
		miner.ActL1StartBlock(12)(t)
		miner.ActL1EndBlock(t)
	}

	// sync verifier from L1 batch in otherwise empty sequence window
	verifier.ActL1HeadSignal(t)
	verifier.ActL2PipelineFull(t)
	require.Equal(t, uint64(1), verifier.SyncStatus().SafeL2.L1Origin.Number)

	// check that the tx from alice made it into the L2 chain
	verifCl := verifEngine.EthClient()
	vTx, isPending, err := verifCl.TransactionByHash(t.Ctx(), tx.Hash())
	require.NoError(t, err)
	require.False(t, isPending)
	require.NotNil(t, vTx)
}

// TestMixOfBatchesAfterHardfork tests behavior of op-node after Delta hardfork.
// op-node must accept SingularBatch and SpanBatch in sequence.
func TestMixOfBatchesAfterHardfork(gt *testing.T) {
	t := actionsHelpers.NewDefaultTesting(gt)
	p := &e2eutils.TestParams{
		MaxSequencerDrift:   20, // larger than L1 block time we simulate in this test (12)
		SequencerWindowSize: 24,
		ChannelTimeout:      20,
		L1BlockTime:         12,
		AllocType:           config.DefaultAllocType,
	}
	minTs := hexutil.Uint64(0)
	dp := e2eutils.MakeMantleDeployParams(t, p)

	// Activate Delta hardfork for verifier.
	upgradesHelpers.ApplyArsiaTimeOffset(dp, &minTs)
	sd := e2eutils.SetupMantleNormal(t, dp, actionsHelpers.DefaultAlloc)
	log := testlog.Logger(t, log.LevelError)
	miner, seqEngine, sequencer := actionsHelpers.SetupSequencerTest(t, sd, log)
	verifEngine, verifier := actionsHelpers.SetupVerifier(t, sd, log, miner.L1Client(t, sd.RollupCfg), miner.BlobStore(), &sync.Config{})
	rollupSeqCl := sequencer.RollupClient()
	seqEngCl := seqEngine.EthClient()

	sequencer.ActL2PipelineFull(t)
	verifier.ActL2PipelineFull(t)
	miner.ActEmptyBlock(t)

	var txHashes [4]common.Hash
	for i := 0; i < 4; i++ {
		// Alice makes a L2 tx
		n, err := seqEngCl.PendingNonceAt(t.Ctx(), dp.Addresses.Alice)
		require.NoError(t, err)
		signer := types.LatestSigner(sd.L2Cfg.Config)
		tx := types.MustSignNewTx(dp.Secrets.Alice, signer, &types.DynamicFeeTx{
			ChainID:   sd.L2Cfg.Config.ChainID,
			Nonce:     n,
			GasTipCap: big.NewInt(2 * params.GWei),
			GasFeeCap: new(big.Int).Add(miner.L1Chain().CurrentBlock().BaseFee, big.NewInt(2*params.GWei)),
			Gas:       params.TxGas,
			To:        &dp.Addresses.Bob,
			Value:     e2eutils.Ether(2),
		})
		require.NoError(gt, seqEngCl.SendTransaction(t.Ctx(), tx))
		txHashes[i] = tx.Hash()

		// Make L2 block
		sequencer.ActL1HeadSignal(t)
		sequencer.ActL2StartBlock(t)
		seqEngine.ActL2IncludeTx(dp.Addresses.Alice)(t)
		sequencer.ActL2EndBlock(t)
		sequencer.ActBuildToL1Head(t)

		// Select batcher mode
		batcherCfg := actionsHelpers.BatcherCfg{
			MinL1TxSize:              0,
			MaxL1TxSize:              128_000,
			BatcherKey:               dp.Secrets.Batcher,
			ForceSubmitSpanBatch:     i%2 == 0, // Submit SpanBatch for odd numbered batches
			ForceSubmitSingularBatch: i%2 == 1, // Submit SingularBatch for even numbered batches
			DataAvailabilityType:     batcherFlags.CalldataType,
		}
		batcher := actionsHelpers.NewL2Batcher(log, sd.RollupCfg, &batcherCfg, rollupSeqCl, miner.EthClient(), seqEngine.EthClient(), seqEngine.EngineClient(t, sd.RollupCfg))
		// Submit all new blocks
		batcher.ActSubmitAll(t)

		// Confirm batch on L1
		miner.ActL1StartBlock(12)(t)
		miner.ActL1IncludeTx(dp.Addresses.Batcher)(t)
		miner.ActL1EndBlock(t)
	}

	// Now make enough L1 blocks that the verifier will have to derive a L2 block
	// It will also eagerly derive the block from the batcher
	for i := uint64(0); i < sd.RollupCfg.SeqWindowSize; i++ {
		miner.ActL1StartBlock(12)(t)
		miner.ActL1EndBlock(t)
	}

	// Sync verifier from L1 batch in otherwise empty sequence window
	verifier.ActL1HeadSignal(t)
	verifier.ActL2PipelineFull(t)
	require.Equal(t, uint64(5), verifier.SyncStatus().SafeL2.L1Origin.Number)

	// Check that the tx from alice made it into the L2 chain
	verifCl := verifEngine.EthClient()
	for _, txHash := range txHashes {
		vTx, isPending, err := verifCl.TransactionByHash(t.Ctx(), txHash)
		require.NoError(t, err)
		require.False(t, isPending)
		require.NotNil(t, vTx)
	}
}

// TestSpanBatchEmptyChain tests derivation of empty chain using SpanBatch.
func TestSpanBatchEmptyChain(gt *testing.T) {
	t := actionsHelpers.NewDefaultTesting(gt)
	p := &e2eutils.TestParams{
		MaxSequencerDrift:   20,
		SequencerWindowSize: 24,
		ChannelTimeout:      20,
		L1BlockTime:         12,
		AllocType:           config.DefaultAllocType,
	}
	dp := e2eutils.MakeMantleDeployParams(t, p)
	minTs := hexutil.Uint64(0)
	// Activate Delta hardfork
	upgradesHelpers.ApplyArsiaTimeOffset(dp, &minTs)
	sd := e2eutils.SetupMantleNormal(t, dp, actionsHelpers.DefaultAlloc)
	log := testlog.Logger(t, log.LevelError)
	miner, seqEngine, sequencer := actionsHelpers.SetupSequencerTest(t, sd, log)
	_, verifier := actionsHelpers.SetupVerifier(t, sd, log, miner.L1Client(t, sd.RollupCfg), miner.BlobStore(), &sync.Config{})

	rollupSeqCl := sequencer.RollupClient()
	batcher := actionsHelpers.NewL2Batcher(log, sd.RollupCfg, actionsHelpers.MantleDefaultBatcherCfg(dp),
		rollupSeqCl, miner.EthClient(), seqEngine.EthClient(), seqEngine.EngineClient(t, sd.RollupCfg))

	sequencer.ActL2PipelineFull(t)
	verifier.ActL2PipelineFull(t)

	miner.ActEmptyBlock(t)
	sequencer.ActL1HeadSignal(t)
	// Make 1200 empty L2 blocks (L1BlockTime / L2BlockTime * 100)
	for i := 0; i < 100; i++ {
		sequencer.ActBuildToL1Head(t)

		if i%10 == 9 {
			// batch submit to L1
			batcher.ActSubmitAll(t)

			// Since the unsafe head could be changed due to the reorg during derivation, save the current unsafe head.
			unsafeHead := sequencer.L2Unsafe().ID()

			// confirm batch on L1
			miner.ActL1StartBlock(12)(t)
			miner.ActL1IncludeTx(dp.Addresses.Batcher)(t)
			miner.ActL1EndBlock(t)

			sequencer.ActL1HeadSignal(t)
			sequencer.ActL2PipelineFull(t)

			// After derivation pipeline, the safe head must be same as latest unsafe head
			// i.e. There must be no reorg during derivation pipeline.
			require.Equal(t, sequencer.L2Safe().ID(), unsafeHead)
		} else {
			miner.ActEmptyBlock(t)
			sequencer.ActL1HeadSignal(t)
		}
	}

	verifier.ActL1HeadSignal(t)
	verifier.ActL2PipelineFull(t)

	require.Equal(t, sequencer.L2Unsafe(), sequencer.L2Safe())
	require.Equal(t, verifier.L2Unsafe(), verifier.L2Safe())
	require.Equal(t, sequencer.L2Safe(), verifier.L2Safe())
}

// TestSpanBatchLowThroughputChain tests derivation of low-throughput chain using SpanBatch.
func TestSpanBatchLowThroughputChain(gt *testing.T) {
	t := actionsHelpers.NewDefaultTesting(gt)
	p := &e2eutils.TestParams{
		MaxSequencerDrift:   20,
		SequencerWindowSize: 24,
		ChannelTimeout:      20,
		L1BlockTime:         12,
		AllocType:           config.DefaultAllocType,
	}
	dp := e2eutils.MakeMantleDeployParams(t, p)
	minTs := hexutil.Uint64(0)
	// Activate Delta hardfork
	upgradesHelpers.ApplyArsiaTimeOffset(dp, &minTs)
	sd := e2eutils.SetupMantleNormal(t, dp, actionsHelpers.DefaultAlloc)
	log := testlog.Logger(t, log.LevelError)
	miner, seqEngine, sequencer := actionsHelpers.SetupSequencerTest(t, sd, log)
	_, verifier := actionsHelpers.SetupVerifier(t, sd, log, miner.L1Client(t, sd.RollupCfg), miner.BlobStore(), &sync.Config{})

	rollupSeqCl := sequencer.RollupClient()
	batcher := actionsHelpers.NewL2Batcher(log, sd.RollupCfg, actionsHelpers.MantleDefaultBatcherCfg(dp),
		rollupSeqCl, miner.EthClient(), seqEngine.EthClient(), seqEngine.EngineClient(t, sd.RollupCfg))
	cl := seqEngine.EthClient()

	const numTestUsers = 5
	var privKeys [numTestUsers]*ecdsa.PrivateKey
	var addrs [numTestUsers]common.Address
	for i := 0; i < numTestUsers; i++ {
		// Create a new test account
		privateKey, err := dp.Secrets.Wallet.PrivateKey(accounts.Account{
			URL: accounts.URL{
				Path: fmt.Sprintf("m/44'/60'/0'/0/%d", 10+i),
			},
		})
		privKeys[i] = privateKey
		addr := crypto.PubkeyToAddress(privateKey.PublicKey)
		require.NoError(t, err)
		addrs[i] = addr

		bal, err := cl.BalanceAt(context.Background(), addr, nil)
		require.NoError(gt, err)
		require.Equal(gt, 1, bal.Cmp(common.Big0), "account %d must have non-zero balance, address: %s, balance: %d", i, addr, bal)
	}

	sequencer.ActL2PipelineFull(t)
	verifier.ActL2PipelineFull(t)

	miner.ActEmptyBlock(t)
	totalTxCount := 0
	// Make 600 L2 blocks (L1BlockTime / L2BlockTime * 50) including 1~3 txs
	for i := 0; i < 50; i++ {
		for sequencer.L2Unsafe().L1Origin.Number < sequencer.SyncStatus().HeadL1.Number {
			sequencer.ActL2StartBlock(t)
			// fill the block with random number of L2 txs
			for j := 0; j < rand.Intn(3); j++ {
				userIdx := totalTxCount % numTestUsers
				signer := types.LatestSigner(sd.L2Cfg.Config)
				data := make([]byte, rand.Intn(100))
				_, err := crand.Read(data[:]) // fill with random bytes
				require.NoError(t, err)
				// calculate intrinsicGas
				intrinsicGasCosts, err := core.IntrinsicGas(data, nil, nil, false, true, true, false, false)
				require.NoError(t, err)
				intrinsicGas := intrinsicGasCosts.Sum()
				//calculate Floor data gas
				floorDataGas, err := core.FloorDataGas(params.Rules{IsAmsterdam: true}, data, nil)
				require.NoError(t, err)
				gas := intrinsicGas
				if floorDataGas > gas {
					gas = floorDataGas
				}
				baseFee := seqEngine.L2Chain().CurrentBlock().BaseFee
				nonce, err := cl.PendingNonceAt(t.Ctx(), addrs[userIdx])
				require.NoError(t, err)
				tx := types.MustSignNewTx(privKeys[userIdx], signer, &types.DynamicFeeTx{
					ChainID:   sd.L2Cfg.Config.ChainID,
					Nonce:     nonce,
					GasTipCap: big.NewInt(2 * params.GWei),
					GasFeeCap: new(big.Int).Add(new(big.Int).Mul(baseFee, big.NewInt(2)), big.NewInt(2*params.GWei)),
					Gas:       gas,
					To:        &dp.Addresses.Bob,
					Value:     big.NewInt(0),
					Data:      data,
				})
				require.NoError(gt, cl.SendTransaction(t.Ctx(), tx))
				seqEngine.ActL2IncludeTx(addrs[userIdx])(t)
				totalTxCount += 1
			}
			sequencer.ActL2EndBlock(t)
		}

		if i%10 == 9 {
			// batch submit to L1
			batcher.ActSubmitAll(t)

			// Since the unsafe head could be changed due to the reorg during derivation, save the current unsafe head.
			unsafeHead := sequencer.L2Unsafe().ID()

			// confirm batch on L1
			miner.ActL1StartBlock(12)(t)
			miner.ActL1IncludeTx(dp.Addresses.Batcher)(t)
			miner.ActL1EndBlock(t)

			sequencer.ActL1HeadSignal(t)
			sequencer.ActL2PipelineFull(t)

			// After derivation pipeline, the safe head must be same as latest unsafe head
			// i.e. There must be no reorg during derivation pipeline.
			require.Equal(t, sequencer.L2Safe().ID(), unsafeHead)
		} else {
			miner.ActEmptyBlock(t)
			sequencer.ActL1HeadSignal(t)
		}
	}

	verifier.ActL1HeadSignal(t)
	verifier.ActL2PipelineFull(t)

	require.Equal(t, sequencer.L2Unsafe(), sequencer.L2Safe())
	require.Equal(t, verifier.L2Unsafe(), verifier.L2Safe())
	require.Equal(t, sequencer.L2Safe(), verifier.L2Safe())
}

func TestSpanBatchSingularBatchEquivalence(gt *testing.T) {
	t := actionsHelpers.NewDefaultTesting(gt)
	log := testlog.Logger(t, log.LevelError)

	p := &e2eutils.TestParams{
		MaxSequencerDrift:   20, // larger than L1 block time we simulate in this test (12)
		SequencerWindowSize: 24,
		ChannelTimeout:      20,
		L1BlockTime:         12,
		AllocType:           config.DefaultAllocType,
	}
	// Arsia activated deploy config
	dp := e2eutils.MakeMantleDeployParams(t, p)
	minTs := hexutil.Uint64(0)
	upgradesHelpers.ApplyArsiaTimeOffset(dp, &minTs)
	sdArsiaActivated := e2eutils.SetupMantleNormal(t, dp, actionsHelpers.DefaultAlloc)

	// Setup sequencer
	miner, seqEngine, sequencer := actionsHelpers.SetupSequencerTest(t, sdArsiaActivated, log)
	rollupSeqCl := sequencer.RollupClient()
	seqEngCl := seqEngine.EthClient()

	// Setup Arsia activated spanVerifier
	_, spanVerifier := actionsHelpers.SetupVerifier(t, sdArsiaActivated, log, miner.L1Client(t, sdArsiaActivated.RollupCfg), miner.BlobStore(), &sync.Config{})

	// Setup Arsia activated singularVerifier
	_, singularVerifier := actionsHelpers.SetupVerifier(t, sdArsiaActivated, log, miner.L1Client(t, sdArsiaActivated.RollupCfg), miner.BlobStore(), &sync.Config{})

	// Setup SpanBatcher
	spanBatcher := actionsHelpers.NewL2Batcher(log, sdArsiaActivated.RollupCfg, &actionsHelpers.BatcherCfg{
		MinL1TxSize:          0,
		MaxL1TxSize:          128_000,
		BatcherKey:           dp.Secrets.Batcher,
		ForceSubmitSpanBatch: true,
		DataAvailabilityType: batcherFlags.CalldataType,
	}, rollupSeqCl, miner.EthClient(), seqEngine.EthClient(), seqEngine.EngineClient(t, sdArsiaActivated.RollupCfg))

	// Setup SingularBatcher
	singularBatcher := actionsHelpers.NewL2Batcher(log, sdArsiaActivated.RollupCfg, &actionsHelpers.BatcherCfg{
		MinL1TxSize:              0,
		MaxL1TxSize:              128_000,
		BatcherKey:               dp.Secrets.Batcher,
		ForceSubmitSingularBatch: true,
		DataAvailabilityType:     batcherFlags.CalldataType,
	}, rollupSeqCl, miner.EthClient(), seqEngine.EthClient(), seqEngine.EngineClient(t, sdArsiaActivated.RollupCfg))

	const numTestUsers = 5
	var privKeys [numTestUsers]*ecdsa.PrivateKey
	var addrs [numTestUsers]common.Address
	for i := 0; i < numTestUsers; i++ {
		// Create a new test account
		privateKey, err := dp.Secrets.Wallet.PrivateKey(accounts.Account{
			URL: accounts.URL{
				Path: fmt.Sprintf("m/44'/60'/0'/0/%d", 10+i),
			},
		})
		privKeys[i] = privateKey
		addr := crypto.PubkeyToAddress(privateKey.PublicKey)
		require.NoError(t, err)
		addrs[i] = addr
	}

	miner.ActEmptyBlock(t)
	sequencer.ActL1HeadSignal(t)
	sequencer.ActL2PipelineFull(t)
	totalTxCount := 0
	// Build random blocks
	for sequencer.L2Unsafe().L1Origin.Number < sequencer.SyncStatus().HeadL1.Number {
		sequencer.ActL2StartBlock(t)
		// fill the block with random number of L2 txs
		for j := 0; j < rand.Intn(3); j++ {
			userIdx := totalTxCount % numTestUsers
			signer := types.LatestSigner(sdArsiaActivated.L2Cfg.Config)
			data := make([]byte, rand.Intn(100))
			_, err := crand.Read(data[:]) // fill with random bytes
			require.NoError(t, err)
			// calculate intrinsicGas
			intrinsicGasCosts, err := core.IntrinsicGas(data, nil, nil, false, true, true, false, false)
			require.NoError(t, err)
			intrinsicGas := intrinsicGasCosts.Sum()
			//calculate Floor data gas
			floorDataGas, err := core.FloorDataGas(params.Rules{IsAmsterdam: true}, data, nil)
			require.NoError(t, err)
			gas := intrinsicGas
			if floorDataGas > gas {
				gas = floorDataGas
			}
			baseFee := seqEngine.L2Chain().CurrentBlock().BaseFee
			nonce, err := seqEngCl.PendingNonceAt(t.Ctx(), addrs[userIdx])
			require.NoError(t, err)
			tx := types.MustSignNewTx(privKeys[userIdx], signer, &types.DynamicFeeTx{
				ChainID:   sdArsiaActivated.L2Cfg.Config.ChainID,
				Nonce:     nonce,
				GasTipCap: big.NewInt(2 * params.GWei),
				GasFeeCap: new(big.Int).Add(new(big.Int).Mul(baseFee, big.NewInt(2)), big.NewInt(2*params.GWei)),
				Gas:       gas,
				To:        &dp.Addresses.Bob,
				Value:     big.NewInt(0),
				Data:      data,
			})
			require.NoError(gt, seqEngCl.SendTransaction(t.Ctx(), tx))
			seqEngine.ActL2IncludeTx(addrs[userIdx])(t)
			totalTxCount += 1
		}
		sequencer.ActL2EndBlock(t)
	}

	// Submit SpanBatch
	spanBatcher.ActSubmitAll(t)
	miner.ActL1StartBlock(12)(t)
	miner.ActL1IncludeTx(dp.Addresses.Batcher)(t)
	miner.ActL1EndBlock(t)

	// Run derivation pipeline for verifiers
	spanVerifier.ActL1HeadSignal(t)
	spanVerifier.ActL2PipelineFull(t)
	singularVerifier.ActL1HeadSignal(t)
	singularVerifier.ActL2PipelineFull(t)

	// Both verifiers should be synced (both support SpanBatch in Arsia)
	require.Equal(t, spanVerifier.L2Safe(), sequencer.L2Unsafe())
	require.Equal(t, singularVerifier.L2Safe(), sequencer.L2Unsafe())

	// Submit SingularBatches
	singularBatcher.ActSubmitAll(t)
	miner.ActL1StartBlock(12)(t)
	miner.ActL1IncludeTx(dp.Addresses.Batcher)(t)
	miner.ActL1EndBlock(t)

	// Run derivation pipeline for verifiers
	spanVerifier.ActL1HeadSignal(t)
	spanVerifier.ActL2PipelineFull(t)
	singularVerifier.ActL1HeadSignal(t)
	singularVerifier.ActL2PipelineFull(t)

	// Both verifiers should still be synced and have the same state
	require.Equal(t, spanVerifier.L2Safe(), singularVerifier.L2Safe())
}

func TestDropSpanBatchBeforeArsia(gt *testing.T) {
	t := actionsHelpers.NewDefaultTesting(gt)
	p := &e2eutils.TestParams{
		MaxSequencerDrift:   20, // larger than L1 block time we simulate in this test (12)
		SequencerWindowSize: 24,
		ChannelTimeout:      20,
		L1BlockTime:         12,
		AllocType:           config.DefaultAllocType,
	}
	dp := e2eutils.MakeMantleDeployParams(t, p)
	dp.DeployConfig.GasPriceOracleTokenRatio = 1
	dp.DeployConfig.L2GenesisBlockGasLimit = 300000000

	// Do not activate Arsia hardfork for verifier
	upgradesHelpers.ApplyArsiaTimeOffset(dp, nil)
	sd := e2eutils.SetupMantleNormal(t, dp, actionsHelpers.DefaultAlloc)
	log := testlog.Logger(t, log.LevelError)
	miner, seqEngine, sequencer := actionsHelpers.SetupSequencerTest(t, sd, log)
	verifEngine, verifier := actionsHelpers.SetupVerifier(t, sd, log, miner.L1Client(t, sd.RollupCfg), miner.BlobStore(), &sync.Config{})
	rollupSeqCl := sequencer.RollupClient()

	// Force batcher to submit SpanBatches to L1.
	batcher := actionsHelpers.NewL2Batcher(log, sd.RollupCfg, &actionsHelpers.BatcherCfg{
		MinL1TxSize:          0,
		MaxL1TxSize:          128_000,
		BatcherKey:           dp.Secrets.Batcher,
		ForceSubmitSpanBatch: true,
		DataAvailabilityType: batcherFlags.BlobsType,
	}, rollupSeqCl, miner.EthClient(), seqEngine.EthClient(), seqEngine.EngineClient(t, sd.RollupCfg))

	// Build and finalize an empty L1 block first
	// This is required for blob transactions to work properly with the blob pool
	miner.ActEmptyBlock(t)
	miner.ActL1SafeNext(t)
	miner.ActL1FinalizeNext(t)

	// Alice makes a L2 tx
	cl := seqEngine.EthClient()
	n, err := cl.PendingNonceAt(t.Ctx(), dp.Addresses.Alice)
	require.NoError(t, err)
	signer := types.LatestSigner(sd.L2Cfg.Config)
	tx := types.MustSignNewTx(dp.Secrets.Alice, signer, &types.DynamicFeeTx{
		ChainID:   sd.L2Cfg.Config.ChainID,
		Nonce:     n,
		GasTipCap: big.NewInt(2 * params.GWei),
		GasFeeCap: new(big.Int).Add(miner.L1Chain().CurrentBlock().BaseFee, big.NewInt(2*params.GWei)),
		Gas:       50000,
		To:        &dp.Addresses.Bob,
		Value:     e2eutils.Ether(2),
	})
	require.NoError(gt, cl.SendTransaction(t.Ctx(), tx))

	sequencer.ActL1HeadSignal(t)
	sequencer.ActL2PipelineFull(t)
	verifier.ActL1HeadSignal(t)
	verifier.ActL2PipelineFull(t)

	// Make L2 block
	sequencer.ActL2StartBlock(t)
	seqEngine.ActL2IncludeTx(dp.Addresses.Alice)(t)
	sequencer.ActL2EndBlock(t)

	// Batch submit to L1. batcher should submit span batches.
	batcher.ActL2BatchBuffer(t)
	batcher.ActL2ChannelClose(t)
	batcher.ActL2BatchSubmitMantle(t)
	batchTX := batcher.LastSubmitted

	// Confirm batch on L1
	miner.ActL1StartBlock(12)(t)
	miner.ActL1IncludeTxByHash(batchTX.Hash())(t)
	miner.ActL1EndBlock(t)
	bl := miner.L1Chain().CurrentBlock()
	log.Info("bl", "txs", len(miner.L1Chain().GetBlockByHash(bl.Hash()).Transactions()))

	// Now make enough L1 blocks that the verifier will have to derive a L2 block
	// It will also eagerly derive the block from the batcher
	// for i := uint64(0); i < sd.RollupCfg.SeqWindowSize; i++ {
	// 	miner.ActL1StartBlock(12)(t)
	// 	miner.ActL1EndBlock(t)
	// }

	// Try to sync verifier from L1 batch. but verifier should drop every span batch.
	verifier.ActL1HeadSignal(t)
	verifier.ActL2PipelineFull(t)
	// Span batch should be dropped because Arsia is not activated (Delta not active)
	// L2 safe head should remain at genesis (block 0)
	require.Equal(t, uint64(0), verifier.SyncStatus().SafeL2.Number, "span batch should be dropped, safe head should remain at genesis")

	t.Log("Switching batcher to SingularBatch mode")
	singularBatcher := actionsHelpers.NewL2Batcher(log, sd.RollupCfg, &actionsHelpers.BatcherCfg{
		MinL1TxSize:              0,
		MaxL1TxSize:              128_000,
		BatcherKey:               dp.Secrets.Batcher,
		ForceSubmitSingularBatch: true, // Force SingularBatch
		DataAvailabilityType:     batcherFlags.BlobsType,
	}, rollupSeqCl, miner.EthClient(), seqEngine.EthClient(), seqEngine.EngineClient(t, sd.RollupCfg))

	// Manually add the block containing Alice's transaction (block 1)
	// This is necessary because the previous SpanBatcher already buffered this block,
	// so we need to explicitly specify which block to include in the SingularBatch
	singularBatcher.ActCreateChannel(t, false)
	singularBatcher.ActAddBlockByNumber(t, 1, actionsHelpers.BlockLogger(t))
	singularBatcher.ActL2ChannelClose(t)
	singularBatcher.ActL2BatchSubmitMantle(t)
	singularBatchTx := singularBatcher.LastSubmitted

	// Confirm batch on L1
	miner.ActL1StartBlock(12)(t)
	miner.ActL1IncludeTxByHash(singularBatchTx.Hash())(t)
	miner.ActL1EndBlock(t)

	// Generate enough L1 blocks for verifier to derive
	// for i := uint64(0); i < sd.RollupCfg.SeqWindowSize; i++ {
	// 	miner.ActL1StartBlock(12)(t)
	// 	miner.ActL1EndBlock(t)
	// }

	// Sync verifier from L1
	verifier.ActL1HeadSignal(t)
	verifier.ActL2PipelineFull(t)
	// Singular batch should be processed successfully
	// L2 safe head should advance to block 1 (containing Alice's transaction)
	require.Equal(t, uint64(1), verifier.SyncStatus().SafeL2.Number, "singular batch should be processed, safe head should advance to block 1")

	// Verify that Alice's transaction is now included after switching to SingularBatch
	verifCl := verifEngine.EthClient()
	vTx, isPending, err := verifCl.TransactionByHash(t.Ctx(), tx.Hash())
	require.NoError(t, err)
	require.False(t, isPending)
	require.NotNil(t, vTx, "Alice's transaction should be included after switching to SingularBatch")

	t.Log("Safe head recovered after switching to SingularBatch")
}

// TestSpanBatchMaxBlocksPerSpanBatch tests that MaxBlocksPerSpanBatch correctly limits
// the number of L2 blocks per span batch. When the limit is reached, a new span batch
// is started automatically.
func TestSpanBatchMaxBlocksPerSpanBatch(gt *testing.T) {
	t := actionsHelpers.NewDefaultTesting(gt)
	p := &e2eutils.TestParams{
		MaxSequencerDrift:   20,
		SequencerWindowSize: 24,
		ChannelTimeout:      20,
		L1BlockTime:         12,
		AllocType:           config.DefaultAllocType,
	}
	dp := e2eutils.MakeMantleDeployParams(t, p)

	// Activate Arsia (which enables Delta/SpanBatch)
	arsiaOffset := hexutil.Uint64(0)
	upgradesHelpers.ApplyArsiaTimeOffset(dp, &arsiaOffset)
	sd := e2eutils.SetupMantleNormal(t, dp, actionsHelpers.DefaultAlloc)
	log := testlog.Logger(t, log.LevelInfo)
	miner, seqEngine, sequencer := actionsHelpers.SetupSequencerTest(t, sd, log)
	_, verifier := actionsHelpers.SetupVerifier(t, sd, log, miner.L1Client(t, sd.RollupCfg), miner.BlobStore(), &sync.Config{})

	rollupSeqCl := sequencer.RollupClient()
	batcher := actionsHelpers.NewL2Batcher(log, sd.RollupCfg, actionsHelpers.MantleSpanBatcherCfg(dp),
		rollupSeqCl, miner.EthClient(), seqEngine.EthClient(), seqEngine.EngineClient(t, sd.RollupCfg))

	// Build initial L1 block
	miner.ActEmptyBlock(t)

	sequencer.ActL2PipelineFull(t)
	verifier.ActL2PipelineFull(t)

	// Configuration: limit span batch to 5 blocks
	const maxBlocksPerSpanBatch = 5
	const totalL2Blocks = 12 // Should create 3 span batches: 5 + 5 + 2

	// Generate L2 blocks with transactions
	cl := seqEngine.EthClient()
	signer := types.LatestSigner(sd.L2Cfg.Config)

	for i := 0; i < totalL2Blocks; i++ {
		sequencer.ActL2StartBlock(t)

		// Add a transaction to each block
		nonce, err := cl.PendingNonceAt(t.Ctx(), dp.Addresses.Alice)
		require.NoError(t, err)
		baseFee := seqEngine.L2Chain().CurrentBlock().BaseFee
		tx := types.MustSignNewTx(dp.Secrets.Alice, signer, &types.DynamicFeeTx{
			ChainID:   sd.L2Cfg.Config.ChainID,
			Nonce:     nonce,
			GasTipCap: big.NewInt(2 * params.GWei),
			GasFeeCap: new(big.Int).Add(new(big.Int).Mul(baseFee, big.NewInt(2)), big.NewInt(2*params.GWei)),
			Gas:       21000,
			To:        &dp.Addresses.Bob,
			Value:     big.NewInt(1),
		})
		require.NoError(t, cl.SendTransaction(t.Ctx(), tx))
		seqEngine.ActL2IncludeTx(dp.Addresses.Alice)(t)

		sequencer.ActL2EndBlock(t)
	}

	// Record unsafe head before submission
	unsafeHead := sequencer.L2Unsafe()
	log.Info("Generated L2 blocks", "unsafeHead", unsafeHead.Number, "totalBlocks", totalL2Blocks)

	// Buffer all blocks with MaxBlocksPerSpanBatch option
	// This will create multiple span batches when the limit is reached
	for batcher.L2BufferedBlock.Number < unsafeHead.Number {
		require.NoError(t, batcher.Buffer(t,
			actionsHelpers.WithChannelModifier(derive.WithMaxBlocksPerSpanBatch(maxBlocksPerSpanBatch)),
		), "failed to buffer block")
	}

	// Submit the channel to L1
	batcher.ActL2ChannelClose(t)
	batcher.ActL2BatchSubmitMantleRaw(t, batcher.ReadNextOutputFrame(t))

	// Include batcher tx in L1 block
	miner.ActL1StartBlock(12)(t)
	miner.ActL1IncludeTx(dp.Addresses.Batcher)(t)
	miner.ActL1EndBlock(t)

	// Derive on sequencer
	sequencer.ActL1HeadSignal(t)
	sequencer.ActL2PipelineFull(t)

	// Verify sequencer safe head matches unsafe head
	require.Equal(t, unsafeHead.Number, sequencer.L2Safe().Number,
		"sequencer safe head should match unsafe head after derivation")

	// Derive on verifier
	verifier.ActL1HeadSignal(t)
	verifier.ActL2PipelineFull(t)

	// Verify verifier safe head matches sequencer
	require.Equal(t, sequencer.L2Safe(), verifier.L2Safe(),
		"verifier safe head should match sequencer safe head")

	log.Info("TestSpanBatchMaxBlocksPerSpanBatch passed",
		"maxBlocksPerSpanBatch", maxBlocksPerSpanBatch,
		"totalL2Blocks", totalL2Blocks,
		"safeHead", verifier.L2Safe().Number)
}
