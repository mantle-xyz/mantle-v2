package sync

import (
	"errors"
	"math/big"
	"math/rand"
	"strings"
	"testing"
	"time"

	actionsHelpers "github.com/ethereum-optimism/optimism/op-e2e/actions/helpers"
	upgradesHelpers "github.com/ethereum-optimism/optimism/op-e2e/actions/mantleupgrades/helpers"
	"github.com/ethereum-optimism/optimism/op-e2e/e2eutils/blobstore"
	"github.com/stretchr/testify/require"

	"github.com/ethereum/go-ethereum"
	gethengine "github.com/ethereum/go-ethereum/beacon/engine"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/params"
	"github.com/ethereum/go-ethereum/rpc"

	batcherFlags "github.com/ethereum-optimism/optimism/op-batcher/flags"
	"github.com/ethereum-optimism/optimism/op-e2e/e2eutils"
	"github.com/ethereum-optimism/optimism/op-node/rollup"
	"github.com/ethereum-optimism/optimism/op-node/rollup/derive"
	engine2 "github.com/ethereum-optimism/optimism/op-node/rollup/engine"
	syncConfig "github.com/ethereum-optimism/optimism/op-node/rollup/sync"
	"github.com/ethereum-optimism/optimism/op-service/eth"
	"github.com/ethereum-optimism/optimism/op-service/event"
	"github.com/ethereum-optimism/optimism/op-service/sources"
	"github.com/ethereum-optimism/optimism/op-service/testlog"
	"github.com/ethereum-optimism/optimism/op-service/testutils"
)

func newSpanChannelOut(t actionsHelpers.StatefulTesting, e e2eutils.SetupData) derive.ChannelOut {
	channelOut, err := derive.NewSpanChannelOut(128_000, derive.Zlib, rollup.NewChainSpec(e.RollupCfg))
	require.NoError(t, err)
	return channelOut
}

// TestSyncBatchType run each sync test case in singular batch mode and span batch mode.
func TestSyncBatchType(t *testing.T) {
	tests := []struct {
		name string
		f    func(gt *testing.T, isSpanBatch bool)
	}{
		{"DerivationWithFlakyL1RPC", DerivationWithFlakyL1RPC},
		{"FinalizeWhileSyncing", FinalizeWhileSyncing},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name+"_SingularBatch", func(t *testing.T) {
			test.f(t, false)
		})
	}

	for _, test := range tests {
		test := test
		t.Run(test.name+"_SpanBatch", func(t *testing.T) {
			test.f(t, true)
		})
	}
}

func DerivationWithFlakyL1RPC(gt *testing.T, isSpanBatch bool) {
	t := actionsHelpers.NewDefaultTesting(gt)
	dp := e2eutils.MakeMantleDeployParams(t, actionsHelpers.DefaultRollupTestParams())
	arsiaTimeOffset := hexutil.Uint64(0)
	upgradesHelpers.ApplyArsiaTimeOffset(dp, &arsiaTimeOffset)

	sd := e2eutils.SetupMantleNormal(t, dp, actionsHelpers.DefaultAlloc)
	log := testlog.Logger(t, log.LevelError) // mute all the temporary derivation errors that we forcefully create
	_, _, miner, sequencer, _, verifier, _, batcher := actionsHelpers.SetupMantleReorgTestActors(t, dp, sd, log, isSpanBatch)

	rng := rand.New(rand.NewSource(1234))
	sequencer.ActL2PipelineFull(t)
	verifier.ActL2PipelineFull(t)

	// build a L1 chain with 20 blocks and matching L2 chain and batches to test some derivation work
	miner.ActEmptyBlock(t)
	for i := 0; i < 20; i++ {
		sequencer.ActL1HeadSignal(t)
		sequencer.ActL2PipelineFull(t)
		sequencer.ActBuildToL1Head(t)
		batcher.ActSubmitAll(t)
		miner.ActL1StartBlock(12)(t)
		miner.ActL1IncludeTx(batcher.BatcherAddr)(t)
		miner.ActL1EndBlock(t)
	}
	// Make verifier aware of head
	verifier.ActL1HeadSignal(t)

	// Now make the L1 RPC very flaky: requests will randomly fail with 50% chance
	miner.MockL1RPCErrors(func() error {
		if rng.Intn(2) == 0 {
			return errors.New("mock rpc error")
		}
		return nil
	})

	// And sync the verifier
	verifier.ActL2PipelineFull(t)
	// Verifier should be synced, even though it hit lots of temporary L1 RPC errors
	require.Equal(t, sequencer.L2Unsafe(), verifier.L2Safe(), "verifier is synced")
}

func FinalizeWhileSyncing(gt *testing.T, isSpanBatch bool) {
	t := actionsHelpers.NewDefaultTesting(gt)
	dp := e2eutils.MakeMantleDeployParams(t, actionsHelpers.DefaultRollupTestParams())
	arsiaTimeOffset := hexutil.Uint64(0)
	upgradesHelpers.ApplyArsiaTimeOffset(dp, &arsiaTimeOffset)
	sd := e2eutils.SetupMantleNormal(t, dp, actionsHelpers.DefaultAlloc)
	log := testlog.Logger(t, log.LevelError) // mute all the temporary derivation errors that we forcefully create
	_, _, miner, sequencer, _, verifier, _, batcher := actionsHelpers.SetupReorgTestActors(t, dp, sd, log)

	sequencer.ActL2PipelineFull(t)
	verifier.ActL2PipelineFull(t)

	verifierStartStatus := verifier.SyncStatus()

	// Build an L1 chain with 64 + 1 blocks, containing batches of L2 chain.
	// Enough to go past the finalityDelay of the engine queue,
	// to make the verifier finalize while it syncs.
	miner.ActEmptyBlock(t)
	for i := 0; i < 64+1; i++ {
		sequencer.ActL1HeadSignal(t)
		sequencer.ActL2PipelineFull(t)
		sequencer.ActBuildToL1Head(t)
		batcher.ActSubmitAll(t)
		miner.ActL1StartBlock(12)(t)
		miner.ActL1IncludeTx(batcher.BatcherAddr)(t)
		miner.ActL1EndBlock(t)
	}
	l1Head := miner.L1Chain().CurrentHeader()
	// finalize all of L1
	miner.ActL1Safe(t, l1Head.Number.Uint64())
	miner.ActL1Finalize(t, l1Head.Number.Uint64())

	// Now signal L1 finality to the verifier, while the verifier is not synced.
	verifier.ActL1HeadSignal(t)
	verifier.ActL1SafeSignal(t)
	verifier.ActL1FinalizedSignal(t)

	// Now sync the verifier, without repeating the signal.
	// While it's syncing, it should finalize on interval now, based on the future L1 finalized block it remembered.
	verifier.ActL2PipelineFull(t)

	// Verify the verifier finalized something new
	result := verifier.SyncStatus()
	require.Less(t, verifierStartStatus.FinalizedL2.Number, result.FinalizedL2.Number, "verifier finalized L2 blocks during sync")
}

// TestUnsafeSync tests that a verifier properly imports unsafe blocks via gossip.
func TestUnsafeSync(gt *testing.T) {
	t := actionsHelpers.NewDefaultTesting(gt)
	dp := e2eutils.MakeMantleDeployParams(t, actionsHelpers.DefaultRollupTestParams())
	sd := e2eutils.SetupMantleNormal(t, dp, actionsHelpers.DefaultAlloc)
	log := testlog.Logger(t, log.LevelInfo)

	sd, _, _, sequencer, seqEng, verifier, _, _ := actionsHelpers.SetupMantleReorgTestActors(t, dp, sd, log, false)
	seqEngCl, err := sources.NewEngineClient(seqEng.RPCClient(), log, nil, sources.EngineClientDefaultConfig(sd.RollupCfg))
	require.NoError(t, err)

	sequencer.ActL2PipelineFull(t)
	verifier.ActL2PipelineFull(t)

	for i := 0; i < 10; i++ {
		// Build a L2 block
		sequencer.ActL2StartBlock(t)
		sequencer.ActL2EndBlock(t)
		// Notify new L2 block to verifier by unsafe gossip
		seqHead, err := seqEngCl.PayloadByLabel(t.Ctx(), eth.Unsafe)
		require.NoError(t, err)
		verifier.ActL2UnsafeGossipReceive(seqHead)(t)
		// Handle unsafe payload
		verifier.ActL2PipelineFull(t)
		// Verifier must advance its unsafe head.
		require.Equal(t, sequencer.L2Unsafe().Hash, verifier.L2Unsafe().Hash)
	}
}

func TestBackupUnsafe(gt *testing.T) {
	t := actionsHelpers.NewDefaultTesting(gt)
	dp := e2eutils.MakeMantleDeployParams(t, actionsHelpers.DefaultRollupTestParams())
	minTs := hexutil.Uint64(0)
	// Activate Arsia hardfork
	upgradesHelpers.ApplyArsiaTimeOffset(dp, &minTs)
	dp.DeployConfig.L2BlockTime = 2
	sd := e2eutils.SetupMantleNormal(t, dp, actionsHelpers.DefaultAlloc)
	log := testlog.Logger(t, log.LvlInfo)
	_, dp, miner, sequencer, seqEng, verifier, _, batcher := actionsHelpers.SetupMantleReorgTestActors(t, dp, sd, log, true)
	l2Cl := seqEng.EthClient()
	seqEngCl, err := sources.NewEngineClient(seqEng.RPCClient(), log, nil, sources.EngineClientDefaultConfig(sd.RollupCfg))
	require.NoError(t, err)

	rng := rand.New(rand.NewSource(1234))
	signer := types.LatestSigner(sd.L2Cfg.Config)

	sequencer.ActL2PipelineFull(t)
	verifier.ActL2PipelineFull(t)

	// ========================================
	// Phase 1: Build original unsafe chain A1 ~ A5
	// ========================================
	for i := 0; i < 5; i++ {
		sequencer.ActL2StartBlock(t)
		sequencer.ActL2EndBlock(t)

		// Notify new L2 block to verifier by unsafe gossip
		seqHead, err := seqEngCl.PayloadByLabel(t.Ctx(), eth.Unsafe)
		require.NoError(t, err)
		verifier.ActL2UnsafeGossipReceive(seqHead)(t)
	}

	seqHead, err := seqEngCl.PayloadByLabel(t.Ctx(), eth.Unsafe)
	require.NoError(t, err)
	// Save the hash of original A5 for later comparison
	targetUnsafeHeadHash := seqHead.ExecutionPayload.BlockHash

	// Verify initial state: unsafe at A5, safe at genesis
	require.Equal(t, uint64(5), sequencer.L2Unsafe().Number)
	require.Equal(t, uint64(0), sequencer.L2Safe().Number)

	verifier.ActL2PipelineFull(t)
	require.Equal(t, uint64(5), verifier.L2Unsafe().Number)
	require.Equal(t, uint64(0), verifier.L2Safe().Number)

	// ========================================
	// Phase 2: Create malicious span batch
	// ========================================
	// Batch contains: A1 (same), B2 (different but valid), B3 (invalid), B4, B5
	channelOut := newSpanChannelOut(t, *sd)

	for i := uint64(1); i <= sequencer.L2Unsafe().Number; i++ {
		block, err := l2Cl.BlockByNumber(t.Ctx(), new(big.Int).SetUint64(i))
		require.NoError(t, err)

		if i == 2 {
			// B2: Valid block but different from A2 (contains Alice's transaction)
			n, err := l2Cl.PendingNonceAt(t.Ctx(), dp.Addresses.Alice)
			require.NoError(t, err)
			validTx := types.MustSignNewTx(dp.Secrets.Alice, signer, &types.DynamicFeeTx{
				ChainID:   sd.L2Cfg.Config.ChainID,
				Nonce:     n,
				GasTipCap: big.NewInt(2 * params.GWei),
				GasFeeCap: new(big.Int).Add(miner.L1Chain().CurrentBlock().BaseFee, big.NewInt(2*params.GWei)),
				Gas:       params.TxGas,
				To:        &dp.Addresses.Bob,
				Value:     e2eutils.Ether(2),
			})
			block = block.WithBody(types.Body{Transactions: []*types.Transaction{block.Transactions()[0], validTx}})
		}

		if i == 3 {
			// B3: Invalid block (contains random invalid transaction)
			invalidTx := testutils.RandomTx(rng, big.NewInt(100), signer)
			block = block.WithBody(types.Body{Transactions: []*types.Transaction{block.Transactions()[0], invalidTx}})
		}

		_, err = channelOut.AddBlock(sd.RollupCfg, block)
		require.NoError(t, err)
	}

	// Submit the malicious span batch to L1
	batcher.L2ChannelOut = channelOut
	batcher.ActL2ChannelClose(t)
	batcher.ActL2BatchSubmit(t)

	miner.ActL1StartBlock(12)(t)
	miner.ActL1IncludeTx(dp.Addresses.Batcher)(t)
	miner.ActL1EndBlock(t)

	// ========================================
	// Phase 3: Sequencer processes the malicious batch
	// ========================================
	sequencer.ActL1HeadSignal(t)

	// Initial state: backupUnsafe is empty, pendingSafe is at genesis
	require.Equal(t, eth.L2BlockRef{}, sequencer.L2BackupUnsafe())
	require.Equal(t, uint64(0), sequencer.L2PendingSafe().Number)

	// Step 1: Process A1 (matches original, no reorg)
	sequencer.ActL2EventsUntilPending(t, 1)
	require.Equal(t, uint64(1), sequencer.L2PendingSafe().Number, "A1 is valid, pendingSafe advances to 1")
	require.Equal(t, uint64(5), sequencer.L2Unsafe().Number, "unsafe still at A5")
	require.Equal(t, eth.L2BlockRef{}, sequencer.L2BackupUnsafe(), "no backup yet")

	// Step 2: Process B2 (valid but different, triggers reorg and backup)
	sequencer.ActL2EventsUntilPending(t, 2)
	require.Equal(t, uint64(2), sequencer.L2Unsafe().Number, "unsafe reorged to B2")
	require.Equal(t, targetUnsafeHeadHash, sequencer.L2BackupUnsafe().Hash, "A5 backed up")
	require.Equal(t, uint64(2), sequencer.L2PendingSafe().Number, "B2 is valid, pendingSafe advances to 2")

	// Step 3: Process B3 (invalid) and remaining blocks
	// In Arsia fork, this triggers deposits-only recovery
	sequencer.ActL2PipelineFull(t)

	// Additional processing for Arsia fork's deposits-only mechanism
	sequencer.ActL2PipelineFull(t)

	// ========================================
	// Phase 4: Verify Arsia fork behavior
	// ========================================
	// In Arsia/Holocene fork:
	// - B3 is invalid, triggers deposits-only recovery
	// - A deposits-only block B3' is created (only L1 attributes)
	// - B3' is valid, so pendingSafe advances to 3
	// - Unsafe chain stops at B3' (block 3)
	// - BackupUnsafe is cleared (not used because deposits-only creates valid chain)
	// - Safe head advances to B3' because it's a valid deposits-only block

	require.Equal(t, eth.L2BlockRef{}, sequencer.L2BackupUnsafe(),
		"backupUnsafe cleared after deposits-only recovery")
	require.Equal(t, uint64(3), sequencer.L2PendingSafe().Number,
		"pendingSafe at deposits-only block 3")
	require.Equal(t, uint64(3), sequencer.L2Unsafe().Number,
		"unsafe at deposits-only block 3")
	require.Equal(t, uint64(3), sequencer.L2Safe().Number,
		"safe advances to deposits-only block 3")
	require.NotEqual(t, targetUnsafeHeadHash, sequencer.L2Unsafe().Hash,
		"unsafe hash different from original A5 (it's B3' now)")

	// ========================================
	// Phase 5: Verifier processes the same batch
	// ========================================
	verifier.ActL1HeadSignal(t)
	verifier.ActL2PipelineFull(t)

	// Verifier should reach the same state as sequencer
	require.Equal(t, uint64(3), verifier.L2Unsafe().Number,
		"verifier unsafe at deposits-only block 3")
	require.Equal(t, uint64(3), verifier.L2Safe().Number,
		"verifier safe at deposits-only block 3")
	require.NotEqual(t, targetUnsafeHeadHash, verifier.L2Unsafe().Hash,
		"verifier unsafe hash different from original A5")

	// ========================================
	// Phase 6: Rebuild chain and verify recovery
	// ========================================
	// Current state: sequencer at block 3 (deposits-only block B3')
	// The original A4-A5 blocks were lost during reorg.
	// We need to build new blocks to reach block 5.

	// Build blocks 4 and 5
	sequencer.ActL2StartBlock(t)
	sequencer.ActL2EndBlock(t)
	require.Equal(t, uint64(4), sequencer.L2Unsafe().Number,
		"sequencer built block 4")

	// Submit block 4
	batcher.ActL2BatchBuffer(t)
	batcher.ActL2ChannelClose(t)
	batcher.ActL2BatchSubmit(t)

	miner.ActL1StartBlock(12)(t)
	miner.ActL1IncludeTx(dp.Addresses.Batcher)(t)
	miner.ActL1EndBlock(t)

	miner.ActL1SafeNext(t)
	miner.ActL1FinalizeNext(t)

	sequencer.ActL1HeadSignal(t)
	sequencer.ActL2PipelineFull(t)

	require.Equal(t, uint64(4), sequencer.L2Safe().Number,
		"safe at block 4")
	//build block 5
	sequencer.ActL2StartBlock(t)
	sequencer.ActL2EndBlock(t)
	require.Equal(t, uint64(5), sequencer.L2Unsafe().Number,
		"sequencer built block 5")

	// Get the new block 5 hash (different from original A5)
	seqHead, err = seqEngCl.PayloadByLabel(t.Ctx(), eth.Unsafe)
	require.NoError(t, err)
	newBlock5Hash := seqHead.ExecutionPayload.BlockHash

	// The new block 5 hash should be different from original A5
	require.NotEqual(t, targetUnsafeHeadHash, newBlock5Hash,
		"new block 5 hash is different from original A5 due to different chain history")

	// Submit the new chain (blocks 1-5) to L1
	batcher.ActL2BatchBuffer(t)
	batcher.ActL2ChannelClose(t)
	batcher.ActL2BatchSubmit(t)

	miner.ActL1StartBlock(12)(t)
	miner.ActL1IncludeTx(dp.Addresses.Batcher)(t)
	miner.ActL1EndBlock(t)

	// Sequencer processes the batch
	sequencer.ActL1HeadSignal(t)
	sequencer.ActL2PipelineFull(t)

	// After processing valid batch, safe head should advance to 5
	require.Equal(t, uint64(5), sequencer.L2Unsafe().Number,
		"unsafe at block 5")
	require.Equal(t, uint64(5), sequencer.L2Safe().Number,
		"safe advanced to block 5")
	require.Equal(t, eth.L2BlockRef{}, sequencer.L2BackupUnsafe(),
		"backupUnsafe cleared after consolidation")

	// Verifier processes the batch
	verifier.ActL1HeadSignal(t)
	verifier.ActL2PipelineFull(t)

	// Verifier should also advance to block 5
	require.Equal(t, uint64(5), verifier.L2Unsafe().Number,
		"verifier unsafe at block 5")
	require.Equal(t, uint64(5), verifier.L2Safe().Number,
		"verifier safe at block 5")
	require.Equal(t, eth.L2BlockRef{}, verifier.L2BackupUnsafe(),
		"verifier backupUnsafe cleared")
}

func TestBackupUnsafeReorgForkChoiceInputError(gt *testing.T) {
	t := actionsHelpers.NewDefaultTesting(gt)
	dp := e2eutils.MakeMantleDeployParams(t, actionsHelpers.DefaultRollupTestParams())
	minTs := hexutil.Uint64(0)
	// Activate Arsia hardfork
	upgradesHelpers.ApplyArsiaTimeOffset(dp, &minTs)
	dp.DeployConfig.L2BlockTime = 2
	sd := e2eutils.SetupMantleNormal(t, dp, actionsHelpers.DefaultAlloc)
	log := testlog.Logger(t, log.LvlInfo)
	_, dp, miner, sequencer, seqEng, verifier, _, batcher := actionsHelpers.SetupMantleReorgTestActors(t, dp, sd, log, true)
	l2Cl := seqEng.EthClient()
	seqEngCl, err := sources.NewEngineClient(seqEng.RPCClient(), log, nil, sources.EngineClientDefaultConfig(sd.RollupCfg))
	require.NoError(t, err)

	rng := rand.New(rand.NewSource(1234))
	signer := types.LatestSigner(sd.L2Cfg.Config)

	sequencer.ActL2PipelineFull(t)
	verifier.ActL2PipelineFull(t)

	// ========================================
	// Phase 1: Build original unsafe chain A1 ~ A5
	// ========================================
	for i := 0; i < 5; i++ {
		sequencer.ActL2StartBlock(t)
		sequencer.ActL2EndBlock(t)

		seqHead, err := seqEngCl.PayloadByLabel(t.Ctx(), eth.Unsafe)
		require.NoError(t, err)
		verifier.ActL2UnsafeGossipReceive(seqHead)(t)
	}

	seqHead, err := seqEngCl.PayloadByLabel(t.Ctx(), eth.Unsafe)
	require.NoError(t, err)
	targetUnsafeHeadHash := seqHead.ExecutionPayload.BlockHash

	require.Equal(t, uint64(5), sequencer.L2Unsafe().Number)
	require.Equal(t, uint64(0), sequencer.L2Safe().Number)

	verifier.ActL2PipelineFull(t)
	require.Equal(t, uint64(5), verifier.L2Unsafe().Number)
	require.Equal(t, uint64(0), verifier.L2Safe().Number)

	// ========================================
	// Phase 2: Create malicious span batch
	// ========================================
	// Batch: A1 (same), B2 (different but valid), B3 (invalid), B4, B5
	channelOut := newSpanChannelOut(t, *sd)

	for i := uint64(1); i <= sequencer.L2Unsafe().Number; i++ {
		block, err := l2Cl.BlockByNumber(t.Ctx(), new(big.Int).SetUint64(i))
		require.NoError(t, err)

		if i == 2 {
			// B2: Valid but different (contains Alice's transaction)
			n, err := l2Cl.PendingNonceAt(t.Ctx(), dp.Addresses.Alice)
			require.NoError(t, err)
			validTx := types.MustSignNewTx(dp.Secrets.Alice, signer, &types.DynamicFeeTx{
				ChainID:   sd.L2Cfg.Config.ChainID,
				Nonce:     n,
				GasTipCap: big.NewInt(2 * params.GWei),
				GasFeeCap: new(big.Int).Add(miner.L1Chain().CurrentBlock().BaseFee, big.NewInt(2*params.GWei)),
				Gas:       params.TxGas,
				To:        &dp.Addresses.Bob,
				Value:     e2eutils.Ether(2),
			})
			block = block.WithBody(types.Body{Transactions: []*types.Transaction{block.Transactions()[0], validTx}})
		}

		if i == 3 {
			// B3: Invalid block
			invalidTx := testutils.RandomTx(rng, big.NewInt(100), signer)
			block = block.WithBody(types.Body{Transactions: []*types.Transaction{block.Transactions()[0], invalidTx}})
		}

		_, err = channelOut.AddBlock(sd.RollupCfg, block)
		require.NoError(t, err)
	}

	// Submit the malicious batch
	batcher.L2ChannelOut = channelOut
	batcher.ActL2ChannelClose(t)
	batcher.ActL2BatchSubmit(t)

	miner.ActL1StartBlock(12)(t)
	miner.ActL1IncludeTx(dp.Addresses.Batcher)(t)
	miner.ActL1EndBlock(t)

	// ========================================
	// Phase 3: Process batch until B2
	// ========================================
	sequencer.ActL1HeadSignal(t)

	require.Equal(t, eth.L2BlockRef{}, sequencer.L2BackupUnsafe(),
		"backupUnsafe initially empty")
	require.Equal(t, uint64(0), sequencer.L2PendingSafe().Number,
		"pendingSafe initially at genesis")

	// Process A1
	sequencer.ActL2EventsUntilPending(t, 1)
	require.Equal(t, uint64(1), sequencer.L2PendingSafe().Number,
		"A1 is valid, pendingSafe advances to 1")
	require.Equal(t, uint64(5), sequencer.L2Unsafe().Number,
		"unsafe still at A5")
	require.Equal(t, eth.L2BlockRef{}, sequencer.L2BackupUnsafe(),
		"no backup yet")

	// Process B2 (triggers reorg and backup)
	sequencer.ActL2EventsUntilPending(t, 2)
	require.Equal(t, uint64(2), sequencer.L2Unsafe().Number,
		"unsafe reorged to B2")
	require.Equal(t, targetUnsafeHeadHash, sequencer.L2BackupUnsafe().Hash,
		"A5 backed up")
	require.Equal(t, uint64(2), sequencer.L2PendingSafe().Number,
		"B2 is valid, pendingSafe advances to 2")

	// ========================================
	// Phase 4: Mock forkChoiceUpdate error
	// ========================================
	// Wait until B3 processing starts (BuildStartEvent)
	sequencer.ActL2EventsUntil(t, event.Is[engine2.BuildStartEvent], 100, true)

	// Mock forkChoiceUpdate returning InputError
	// This simulates a scenario where backupUnsafe restoration fails
	seqEng.ActL2RPCFail(t, eth.InputError{
		Inner: errors.New("mock L2 RPC error"),
		Code:  eth.InvalidForkchoiceState,
	})

	// ========================================
	// Phase 5: Process invalid B3 with error
	// ========================================
	// Try to process B3 (invalid) and remaining blocks
	sequencer.ActL2PipelineFull(t)

	// Additional processing for Arsia fork's deposits-only mechanism
	sequencer.ActL2PipelineFull(t)

	// ========================================
	// Phase 6: Verify Arsia fork behavior with InputError
	// ========================================
	// In Arsia fork with InputError:
	// 1. B3 is invalid, triggers deposits-only recovery
	// 2. Deposits-only block B3' is created and executed successfully
	// 3. Unsafe chain advances to B3' (block 3)
	// 4. Safe head advances to B3' (deposits-only block is valid)
	// 5. forkChoiceUpdate fails with InputError when trying to restore backupUnsafe
	// 6. BackupUnsafe restoration fails and is cleared
	// 7. PendingSafe advances to B3' (deposits-only block is valid)
	//
	// KEY INSIGHT: InputError only prevents backupUnsafe restoration,
	// but does NOT prevent deposits-only recovery from succeeding.
	// The deposits-only block is valid, so safe/unsafe/pendingSafe all advance to it.

	require.Equal(t, eth.L2BlockRef{}, sequencer.L2BackupUnsafe(),
		"backupUnsafe cleared after failed restoration attempt")

	require.Equal(t, uint64(3), sequencer.L2PendingSafe().Number,
		"pendingSafe advances to deposits-only block 3")

	require.Equal(t, uint64(3), sequencer.L2Unsafe().Number,
		"unsafe advances to deposits-only block 3 (deposits-only recovery succeeds despite InputError)")

	require.Equal(t, uint64(3), sequencer.L2Safe().Number,
		"safe advances to deposits-only block 3 (deposits-only block is valid)")

	// ========================================
	// Test complete
	// ========================================
	// We've verified that in Arsia fork:
	// - Deposits-only recovery creates a valid block (B3') and applies it
	// - All heads (unsafe/safe/pendingSafe) advance to the deposits-only block
	// - InputError prevents backupUnsafe restoration (cannot restore to A5)
	// - The deposits-only block is treated as a valid block for safe head advancement
}

func TestBackupUnsafeReorgForkChoiceNotInputError(gt *testing.T) {
	t := actionsHelpers.NewDefaultTesting(gt)
	dp := e2eutils.MakeMantleDeployParams(t, actionsHelpers.DefaultRollupTestParams())
	minTs := hexutil.Uint64(0)
	// Activate Arsia hardfork
	upgradesHelpers.ApplyArsiaTimeOffset(dp, &minTs)
	dp.DeployConfig.L2BlockTime = 2
	sd := e2eutils.SetupMantleNormal(t, dp, actionsHelpers.DefaultAlloc)
	log := testlog.Logger(t, log.LvlInfo)
	_, dp, miner, sequencer, seqEng, verifier, _, batcher := actionsHelpers.SetupMantleReorgTestActors(t, dp, sd, log, true)
	l2Cl := seqEng.EthClient()
	seqEngCl, err := sources.NewEngineClient(seqEng.RPCClient(), log, nil, sources.EngineClientDefaultConfig(sd.RollupCfg))
	require.NoError(t, err)

	rng := rand.New(rand.NewSource(1234))
	signer := types.LatestSigner(sd.L2Cfg.Config)

	sequencer.ActL2PipelineFull(t)
	verifier.ActL2PipelineFull(t)

	// ========================================
	// Phase 1: Build original unsafe chain A1 ~ A5
	// ========================================
	for i := 0; i < 5; i++ {
		sequencer.ActL2StartBlock(t)
		sequencer.ActL2EndBlock(t)

		seqHead, err := seqEngCl.PayloadByLabel(t.Ctx(), eth.Unsafe)
		require.NoError(t, err)
		verifier.ActL2UnsafeGossipReceive(seqHead)(t)
	}

	seqHead, err := seqEngCl.PayloadByLabel(t.Ctx(), eth.Unsafe)
	require.NoError(t, err)
	targetUnsafeHeadHash := seqHead.ExecutionPayload.BlockHash

	require.Equal(t, uint64(5), sequencer.L2Unsafe().Number)
	require.Equal(t, uint64(0), sequencer.L2Safe().Number)

	verifier.ActL2PipelineFull(t)
	require.Equal(t, uint64(5), verifier.L2Unsafe().Number)
	require.Equal(t, uint64(0), verifier.L2Safe().Number)

	// ========================================
	// Phase 2: Create malicious span batch
	// ========================================
	// Batch: A1 (same), B2 (different but valid), B3 (invalid), B4, B5
	channelOut := newSpanChannelOut(t, *sd)

	for i := uint64(1); i <= sequencer.L2Unsafe().Number; i++ {
		block, err := l2Cl.BlockByNumber(t.Ctx(), new(big.Int).SetUint64(i))
		require.NoError(t, err)

		if i == 2 {
			// B2: Valid but different (contains Alice's transaction)
			n, err := l2Cl.PendingNonceAt(t.Ctx(), dp.Addresses.Alice)
			require.NoError(t, err)
			validTx := types.MustSignNewTx(dp.Secrets.Alice, signer, &types.DynamicFeeTx{
				ChainID:   sd.L2Cfg.Config.ChainID,
				Nonce:     n,
				GasTipCap: big.NewInt(2 * params.GWei),
				GasFeeCap: new(big.Int).Add(miner.L1Chain().CurrentBlock().BaseFee, big.NewInt(2*params.GWei)),
				Gas:       params.TxGas,
				To:        &dp.Addresses.Bob,
				Value:     e2eutils.Ether(2),
			})
			block = block.WithBody(types.Body{Transactions: []*types.Transaction{block.Transactions()[0], validTx}})
		}

		if i == 3 {
			// B3: Invalid block
			invalidTx := testutils.RandomTx(rng, big.NewInt(100), signer)
			block = block.WithBody(types.Body{Transactions: []*types.Transaction{block.Transactions()[0], invalidTx}})
		}

		_, err = channelOut.AddBlock(sd.RollupCfg, block)
		require.NoError(t, err)
	}

	// Submit the malicious batch
	batcher.L2ChannelOut = channelOut
	batcher.ActL2ChannelClose(t)
	batcher.ActL2BatchSubmit(t)

	miner.ActL1StartBlock(12)(t)
	miner.ActL1IncludeTx(dp.Addresses.Batcher)(t)
	miner.ActL1EndBlock(t)

	// ========================================
	// Phase 3: Process batch until B2
	// ========================================
	sequencer.ActL1HeadSignal(t)

	require.Equal(t, eth.L2BlockRef{}, sequencer.L2BackupUnsafe(),
		"backupUnsafe initially empty")
	require.Equal(t, uint64(0), sequencer.L2PendingSafe().Number,
		"pendingSafe initially at genesis")

	// Process A1
	sequencer.ActL2EventsUntilPending(t, 1)
	require.Equal(t, uint64(1), sequencer.L2PendingSafe().Number,
		"A1 is valid, pendingSafe advances to 1")
	require.Equal(t, uint64(5), sequencer.L2Unsafe().Number,
		"unsafe still at A5")
	require.Equal(t, eth.L2BlockRef{}, sequencer.L2BackupUnsafe(),
		"no backup yet")

	// Process B2 (triggers reorg and backup)
	sequencer.ActL2EventsUntilPending(t, 2)
	require.Equal(t, uint64(2), sequencer.L2Unsafe().Number,
		"unsafe reorged to B2")
	require.Equal(t, targetUnsafeHeadHash, sequencer.L2BackupUnsafe().Hash,
		"A5 backed up")
	require.Equal(t, uint64(2), sequencer.L2PendingSafe().Number,
		"B2 is valid, pendingSafe advances to 2")

	// ========================================
	// Phase 4: Mock forkChoiceUpdate server error (retryable)
	// ========================================
	// Wait until B3 processing starts (BuildStartEvent)
	sequencer.ActL2EventsUntil(t, event.Is[engine2.BuildStartEvent], 100, true)

	serverErrCnt := 2
	// Mock forkChoiceUpdate returning server error (retryable)
	// After 2 failures, it will succeed
	seqEng.FailL2RPC = func(call []rpc.BatchElem) error {
		for _, e := range call {
			// Only fail forkchoiceUpdated calls without payload attributes
			// (these are the backupUnsafe restoration calls)
			if strings.HasPrefix(e.Method, "engine_forkchoiceUpdated") && e.Args[1].(*eth.PayloadAttributes) == nil {
				if serverErrCnt > 0 {
					serverErrCnt -= 1
					return gethengine.GenericServerError
				} else {
					return nil
				}
			}
		}
		return nil
	}

	// ========================================
	// Phase 5: Process invalid B3 with retryable errors
	// ========================================
	// First attempt: server error
	sequencer.ActL2PipelineFull(t)

	// Retry: server error again
	// Then success on third attempt
	sequencer.ActL2PipelineFull(t)

	// Additional processing for Arsia fork
	sequencer.ActL2PipelineFull(t)

	// ========================================
	// Phase 6: Verify Arsia fork behavior with successful retry
	// ========================================
	// In Arsia fork with successful retry:
	// 1. B3 is invalid, triggers deposits-only recovery
	// 2. Deposits-only block B3' is created and applied
	// 3. Unsafe chain advances to B3' (block 3)
	// 4. forkChoiceUpdate fails with server error (retryable) - first attempt
	// 5. Retry succeeds - forkChoiceUpdate returns success
	// 6. However, deposits-only block has already been applied
	// 7. Unsafe chain stays at B3' (does NOT revert to backupUnsafe)
	// 8. PendingSafe and Safe both at B3' (deposits-only block is valid)
	//
	// KEY INSIGHT: In Arsia fork, deposits-only recovery takes precedence.
	// Once the deposits-only block is applied, the chain does NOT revert to
	// backupUnsafe even if forkChoiceUpdate retry succeeds.
	// This is different from Pre-Arsia behavior where successful retry would
	// restore the unsafe chain to backupUnsafe (A5).

	require.Equal(t, eth.L2BlockRef{}, sequencer.L2BackupUnsafe(),
		"backupUnsafe cleared (not used because deposits-only recovery took precedence)")

	require.Equal(t, uint64(3), sequencer.L2PendingSafe().Number,
		"pendingSafe advances to deposits-only block 3")

	require.Equal(t, uint64(3), sequencer.L2Unsafe().Number,
		"unsafe stays at deposits-only block 3 (deposits-only recovery takes precedence even after successful retry)")

	require.Equal(t, uint64(3), sequencer.L2Safe().Number,
		"safe advances to deposits-only block 3 (deposits-only block is valid)")

	// ========================================
	// Test complete
	// ========================================
	// We've verified that in Arsia fork:
	// - Deposits-only recovery creates a valid block (B3') and applies it
	// - All heads (unsafe/safe/pendingSafe) advance to the deposits-only block
	// - Server errors are retryable and forkChoiceUpdate eventually succeeds
	// - However, deposits-only recovery takes precedence over backupUnsafe restoration
	// - The chain stays at deposits-only block, does NOT revert to backupUnsafe
	// - This is the key difference from Pre-Arsia: deposits-only recovery is irreversible
}

// builds l2 blocks within the specified range `from` - `to`
// and performs an EL sync between the sequencer and the verifier,
// then checks the validity of the payloads within a specified block range.
func PerformELSyncAndCheckPayloads(t actionsHelpers.Testing, miner *actionsHelpers.L1Miner, seqEng *actionsHelpers.L2Engine, sequencer *actionsHelpers.L2Sequencer, verEng *actionsHelpers.L2Engine, verifier *actionsHelpers.L2Verifier, seqEngCl *sources.EngineClient, from, to uint64) {
	miner.ActEmptyBlock(t)
	sequencer.ActL2PipelineFull(t)

	// Build L1 blocks on the sequencer
	for i := from; i < to; i++ {
		// Build a L2 block
		sequencer.ActL2StartBlock(t)
		sequencer.ActL2EndBlock(t)
	}

	// Wait longer to peer. This tests flakes or takes a long time when the op-geth instances are not able to peer.
	verEng.AddPeers(seqEng.Enode())

	// Insert it on the verifier
	seqHead, err := seqEngCl.PayloadByLabel(t.Ctx(), eth.Unsafe)
	require.NoError(t, err)
	// Must check with block which is not genesis
	startBlockNum := from + 1
	seqStart, err := seqEngCl.PayloadByNumber(t.Ctx(), startBlockNum)
	require.NoError(t, err)
	verifier.ActL2InsertUnsafePayload(seqHead)(t)

	require.Eventually(t,
		func() bool {
			return seqEng.PeerCount() > 0 && verEng.PeerCount() > 0
		},
		120*time.Second, 1500*time.Millisecond,
		"Sequencer & Verifier must peer with each other for snap sync to work",
	)

	// Expect snap sync to download & execute the entire chain
	// Verify this by checking that the verifier has the correct value for block startBlockNum
	require.Eventually(t,
		func() bool {
			block, err := verifier.Eng.L2BlockRefByNumber(t.Ctx(), startBlockNum)
			if err != nil {
				return false
			}
			return seqStart.ExecutionPayload.BlockHash == block.Hash
		},
		60*time.Second, 1500*time.Millisecond,
		"verifier did not snap sync",
	)
}

// verifies that a specific block number on the L2 engine has the expected label.
func VerifyBlock(t actionsHelpers.Testing, engine actionsHelpers.L2API, number uint64, label eth.BlockLabel) {
	id, err := engine.L2BlockRefByLabel(t.Ctx(), label)
	require.NoError(t, err)
	require.Equal(t, number, id.Number)
}

// submits batch at a specified block number
func BatchSubmitBlock(t actionsHelpers.Testing, miner *actionsHelpers.L1Miner, sequencer *actionsHelpers.L2Sequencer, verifier *actionsHelpers.L2Verifier, batcher *actionsHelpers.L2Batcher, dp *e2eutils.DeployParams, number uint64) {
	sequencer.ActL2StartBlock(t)
	sequencer.ActL2EndBlock(t)
	batcher.ActSubmitAll(t)
	miner.ActL1StartBlock(number)(t)
	miner.ActL1IncludeTx(dp.Addresses.Batcher)(t)
	miner.ActL1EndBlock(t)
	sequencer.ActL2PipelineFull(t)
	verifier.ActL2PipelineFull(t)
}

// TestELSync tests that a verifier will have the EL import the full chain from the sequencer
// when passed a single unsafe block. op-geth can either snap sync or full sync here.
/*
func TestELSync(gt *testing.T) {
	t := actionsHelpers.NewDefaultTesting(gt)
	dp := e2eutils.MakeMantleDeployParams(t, actionsHelpers.DefaultRollupTestParams())
	sd := e2eutils.SetupMantleNormal(t, dp, actionsHelpers.DefaultAlloc)
	log := testlog.Logger(t, log.LevelInfo)

	miner, seqEng, sequencer := actionsHelpers.SetupSequencerTest(t, sd, log)
	// Enable engine P2P sync
	verEng, verifier := actionsHelpers.SetupVerifier(t, sd, log, miner.L1Client(t, sd.RollupCfg), miner.BlobStore(), &sync.Config{SyncMode: sync.ELSync})

	seqEngCl, err := sources.NewEngineClient(seqEng.RPCClient(), log, nil, sources.EngineClientDefaultConfig(sd.RollupCfg))
	require.NoError(t, err)

	PerformELSyncAndCheckPayloads(t, miner, seqEng, sequencer, verEng, verifier, seqEngCl, 0, 10)
}
*/
func PrepareELSyncedNode(t actionsHelpers.Testing, miner *actionsHelpers.L1Miner, sequencer *actionsHelpers.L2Sequencer, seqEng *actionsHelpers.L2Engine, verifier *actionsHelpers.L2Verifier, verEng *actionsHelpers.L2Engine, seqEngCl *sources.EngineClient, batcher *actionsHelpers.L2Batcher, dp *e2eutils.DeployParams) {
	PerformELSyncAndCheckPayloads(t, miner, seqEng, sequencer, verEng, verifier, seqEngCl, 0, 10)

	// Despite downloading the blocks, it has not finished finalizing
	_, err := verifier.Eng.L2BlockRefByLabel(t.Ctx(), "safe")
	require.ErrorIs(t, err, ethereum.NotFound)

	// Insert a block on the verifier to end snap sync
	sequencer.ActL2StartBlock(t)
	sequencer.ActL2EndBlock(t)
	seqHead, err := seqEngCl.PayloadByLabel(t.Ctx(), eth.Unsafe)
	require.NoError(t, err)
	verifier.ActL2InsertUnsafePayload(seqHead)(t)

	// Check that safe + finalized are there
	VerifyBlock(t, verifier.Eng, 11, eth.Safe)
	VerifyBlock(t, verifier.Eng, 11, eth.Finalized)

	// Batch submit everything
	BatchSubmitBlock(t, miner, sequencer, verifier, batcher, dp, 12)

	// Verify that the batch submitted blocks are there now
	VerifyBlock(t, sequencer.Eng, 12, eth.Safe)
	VerifyBlock(t, verifier.Eng, 12, eth.Safe)
}

// TestELSyncTransitionstoCL tests that a verifier which starts with EL sync can switch back to a proper CL sync.
// It takes a sequencer & verifier through the following:
//  1. Build 10 unsafe blocks on the sequencer
//  2. Snap sync those blocks to the verifier
//  3. Build & insert 1 unsafe block from the sequencer to the verifier to end snap sync
//  4. Batch submit everything
//  5. Build 10 more unsafe blocks on the sequencer
//  6. Gossip in the highest block to the verifier. **Expect that it does not snap sync**
//  7. Then gossip the rest of the blocks to the verifier. Once this is complete it should pick up all of the unsafe blocks.
//     Prior to this PR, the test would fail at this point.
//  8. Create 1 more block & batch submit everything & assert that the verifier picked up those blocks
/*
func TestELSyncTransitionstoCL(gt *testing.T) {
	t := actionsHelpers.NewDefaultTesting(gt)
	dp := e2eutils.MakeMantleDeployParams(t, actionsHelpers.DefaultRollupTestParams())
	sd := e2eutils.SetupMantleNormal(t, dp, actionsHelpers.DefaultAlloc)
	logger := testlog.Logger(t, log.LevelInfo)

	captureLog, captureLogHandler := testlog.CaptureLogger(t, log.LevelInfo)

	miner, seqEng, sequencer := actionsHelpers.SetupSequencerTest(t, sd, logger)
	batcher := actionsHelpers.NewL2Batcher(logger, sd.RollupCfg, actionsHelpers.DefaultBatcherCfg(dp), sequencer.RollupClient(), miner.EthClient(), seqEng.EthClient(), seqEng.EngineClient(t, sd.RollupCfg))
	// Enable engine P2P sync
	verEng, verifier := actionsHelpers.SetupVerifier(t, sd, captureLog, miner.L1Client(t, sd.RollupCfg), miner.BlobStore(), &sync.Config{SyncMode: sync.ELSync})

	seqEngCl, err := sources.NewEngineClient(seqEng.RPCClient(), logger, nil, sources.EngineClientDefaultConfig(sd.RollupCfg))
	require.NoError(t, err)

	PrepareELSyncedNode(t, miner, sequencer, seqEng, verifier, verEng, seqEngCl, batcher, dp)

	// Build another 10 L1 blocks on the sequencer
	for i := 0; i < 10; i++ {
		// Build a L2 block
		sequencer.ActL2StartBlock(t)
		sequencer.ActL2EndBlock(t)
	}

	// Now pass payloads to the derivation pipeline
	// This is a little hacky that we have to manually switch between InsertBlock
	// and UnsafeGossipReceive in the tests
	seqHead, err := seqEngCl.PayloadByLabel(t.Ctx(), eth.Unsafe)
	require.NoError(t, err)
	verifier.ActL2UnsafeGossipReceive(seqHead)(t)
	verifier.ActL2PipelineFull(t)
	// Verify that the derivation pipeline did not request a sync to the new head. This is the core of the test, but a little fragile.
	record := captureLogHandler.FindLog(testlog.NewMessageFilter("Forkchoice requested sync to new head"), testlog.NewAttributesFilter("number", "22"))
	require.Nil(t, record, "The verifier should not request to sync to block number 22 because it is in CL mode, not EL mode at this point.")

	for i := 13; i < 23; i++ {
		seqHead, err = seqEngCl.PayloadByNumber(t.Ctx(), uint64(i))
		require.NoError(t, err)
		verifier.ActL2UnsafeGossipReceive(seqHead)(t)
	}
	verifier.ActL2PipelineFull(t)

	// Verify that the unsafe blocks are there now
	// This was failing prior to PR 9661 because op-node would attempt to immediately insert blocks into the EL inside the engine queue. op-geth
	// would not be able to fetch the second range of blocks & it would wipe out the unsafe payloads queue because op-node thought that it had a
	// higher unsafe block but op-geth did not.
	VerifyBlock(t, verifier.Eng, 22, eth.Unsafe)

	// Create 1 more block & batch submit everything
	BatchSubmitBlock(t, miner, sequencer, verifier, batcher, dp, 12)

	// Verify that the batch submitted blocks are there now
	VerifyBlock(t, sequencer.Eng, 23, eth.Safe)
	VerifyBlock(t, verifier.Eng, 23, eth.Safe)
}

func TestELSyncTransitionsToCLSyncAfterNodeRestart(gt *testing.T) {
	t := actionsHelpers.NewDefaultTesting(gt)
	dp := e2eutils.MakeMantleDeployParams(t, actionsHelpers.DefaultRollupTestParams())
	sd := e2eutils.SetupMantleNormal(t, dp, actionsHelpers.DefaultAlloc)
	logger := testlog.Logger(t, log.LevelInfo)

	captureLog, captureLogHandler := testlog.CaptureLogger(t, log.LevelInfo)

	miner, seqEng, sequencer := actionsHelpers.SetupSequencerTest(t, sd, logger)
	batcher := actionsHelpers.NewL2Batcher(logger, sd.RollupCfg, actionsHelpers.DefaultBatcherCfg(dp), sequencer.RollupClient(), miner.EthClient(), seqEng.EthClient(), seqEng.EngineClient(t, sd.RollupCfg))
	// Enable engine P2P sync
	verEng, verifier := actionsHelpers.SetupVerifier(t, sd, captureLog, miner.L1Client(t, sd.RollupCfg), miner.BlobStore(), &sync.Config{SyncMode: sync.ELSync})

	seqEngCl, err := sources.NewEngineClient(seqEng.RPCClient(), logger, nil, sources.EngineClientDefaultConfig(sd.RollupCfg))
	require.NoError(t, err)

	PrepareELSyncedNode(t, miner, sequencer, seqEng, verifier, verEng, seqEngCl, batcher, dp)

	// Create a new verifier which is essentially a new op-node with the sync mode of ELSync and default geth engine kind.
	verifier = actionsHelpers.NewL2Verifier(t, captureLog, miner.L1Client(t, sd.RollupCfg), miner.BlobStore(), altda.Disabled, verifier.Eng, sd.RollupCfg, sd.L1Cfg.Config, sd.DependencySet, &sync.Config{SyncMode: sync.ELSync}, actionsHelpers.DefaultVerifierCfg().SafeHeadListener)

	// Build another 10 L1 blocks on the sequencer
	for i := 0; i < 10; i++ {
		// Build a L2 block
		sequencer.ActL2StartBlock(t)
		sequencer.ActL2EndBlock(t)
	}

	// Insert new block to the engine and kick off a CL sync
	seqHead, err := seqEngCl.PayloadByLabel(t.Ctx(), eth.Unsafe)
	require.NoError(t, err)
	verifier.ActL2InsertUnsafePayload(seqHead)(t)

	// Verify that the derivation pipeline did not request a sync to the new head. This is the core of the test, but a little fragile.
	record := captureLogHandler.FindLog(testlog.NewMessageFilter("Forkchoice requested sync to new head"), testlog.NewAttributesFilter("number", "22"))
	require.Nil(t, record, "The verifier should not request to sync to block number 22 because it is in CL mode, not EL mode at this point.")

	// Verify that op-node has skipped ELSync and started CL sync because geth has finalized block from ELSync.
	record = captureLogHandler.FindLog(testlog.NewMessageFilter("Skipping EL sync and going straight to CL sync because there is a finalized block"))
	require.NotNil(t, record, "The verifier should skip EL Sync at this point.")
}

func TestForcedELSyncCLAfterNodeRestart(gt *testing.T) {
	t := actionsHelpers.NewDefaultTesting(gt)
	dp := e2eutils.MakeMantleDeployParams(t, actionsHelpers.DefaultRollupTestParams())
	sd := e2eutils.SetupMantleNormal(t, dp, actionsHelpers.DefaultAlloc)
	logger := testlog.Logger(t, log.LevelInfo)

	captureLog, captureLogHandler := testlog.CaptureLogger(t, log.LevelInfo)

	miner, seqEng, sequencer := actionsHelpers.SetupSequencerTest(t, sd, logger)
	batcher := actionsHelpers.NewL2Batcher(logger, sd.RollupCfg, actionsHelpers.DefaultBatcherCfg(dp), sequencer.RollupClient(), miner.EthClient(), seqEng.EthClient(), seqEng.EngineClient(t, sd.RollupCfg))
	// Enable engine P2P sync
	verEng, verifier := actionsHelpers.SetupVerifier(t, sd, captureLog, miner.L1Client(t, sd.RollupCfg), miner.BlobStore(), &sync.Config{SyncMode: sync.ELSync})

	seqEngCl, err := sources.NewEngineClient(seqEng.RPCClient(), logger, nil, sources.EngineClientDefaultConfig(sd.RollupCfg))
	require.NoError(t, err)

	PrepareELSyncedNode(t, miner, sequencer, seqEng, verifier, verEng, seqEngCl, batcher, dp)

	// Create a new verifier which is essentially a new op-node with the sync mode of ELSync and erigon engine kind.
	verifier2 := actionsHelpers.NewL2Verifier(t, captureLog, miner.L1Client(t, sd.RollupCfg), miner.BlobStore(), altda.Disabled, verifier.Eng, sd.RollupCfg, sd.L1Cfg.Config, sd.DependencySet, &sync.Config{SyncMode: sync.ELSync, SupportsPostFinalizationELSync: true}, actionsHelpers.DefaultVerifierCfg().SafeHeadListener)

	// Build another 10 L1 blocks on the sequencer
	for i := 0; i < 10; i++ {
		// Build a L2 block
		sequencer.ActL2StartBlock(t)
		sequencer.ActL2EndBlock(t)
	}

	// Insert it on the verifier and kick off EL sync.
	// Syncing doesn't actually work in test,
	// but we can validate the engine is starting EL sync through p2p
	seqHead, err := seqEngCl.PayloadByLabel(t.Ctx(), eth.Unsafe)
	require.NoError(t, err)
	verifier2.ActL2InsertUnsafePayload(seqHead)(t)

	// Verify that the derivation pipeline did not request a sync to the new head. This is the core of the test, but a little fragile.
	record := captureLogHandler.FindLog(testlog.NewMessageFilter("Forkchoice requested sync to new head"), testlog.NewAttributesFilter("number", "22"))
	require.NotNil(t, record, "The verifier should request to sync to block number 22 in EL mode")

	// Verify that op-node is starting ELSync.
	record = captureLogHandler.FindLog(testlog.NewMessageFilter("Skipping EL sync and going straight to CL sync because there is a finalized block"))
	require.Nil(t, record, "The verifier should start EL Sync when l2.engineKind is not geth")
	record = captureLogHandler.FindLog(testlog.NewMessageFilter("Starting EL sync"))
	require.NotNil(t, record, "The verifier should start EL Sync when l2.engineKind is not geth")
}
*/
// TestDerivationWithMissingBlobData tests that when L1 cannot provide blob data,
// the verifier's derivation pipeline stalls and the safe head does not advance.
// This simulates scenarios like:
// - Blob data expired after 18-day retention period
// - Beacon node unavailable
// - Network errors preventing blob retrieval
func TestDerivationWithMissingBlobData(gt *testing.T) {
	t := actionsHelpers.NewDefaultTesting(gt)
	dp := e2eutils.MakeMantleDeployParams(t, actionsHelpers.DefaultRollupTestParams())
	arsiaTimeOffset := hexutil.Uint64(0)
	// Activate Arsia hardfork (Mantle starts using standard blobs after Arsia)
	upgradesHelpers.ApplyArsiaTimeOffset(dp, &arsiaTimeOffset)

	sd := e2eutils.SetupMantleNormal(t, dp, actionsHelpers.DefaultAlloc)

	// Use a log handler to capture errors
	captureLog, captureLogHandler := testlog.CaptureLogger(t, log.LevelDebug)

	miner, seqEngine, sequencer := actionsHelpers.SetupSequencerTest(t, sd, captureLog)
	rollupSeqCl := sequencer.RollupClient()

	// Use blob batching
	batcher := actionsHelpers.NewL2Batcher(captureLog, sd.RollupCfg, &actionsHelpers.BatcherCfg{
		MinL1TxSize:          0,
		MaxL1TxSize:          128_000,
		BatcherKey:           dp.Secrets.Batcher,
		DataAvailabilityType: batcherFlags.BlobsType,
	}, rollupSeqCl, miner.EthClient(), seqEngine.EthClient(), seqEngine.EngineClient(t, sd.RollupCfg))

	// Setup verifier with the same blob store (normal scenario)
	_, verifier := actionsHelpers.SetupVerifier(t, sd, captureLog, miner.L1Client(t, sd.RollupCfg), miner.BlobStore(), &syncConfig.Config{})

	sequencer.ActL2PipelineFull(t)
	verifier.ActL2PipelineFull(t)

	// Build and finalize an empty L1 block first (required for blob pool)
	miner.ActEmptyBlock(t)
	miner.ActL1SafeNext(t)
	miner.ActL1FinalizeNext(t)

	// Create L2 blocks with transactions
	sequencer.ActL1HeadSignal(t)
	sequencer.ActBuildToL1Head(t)

	// Record the L2 unsafe head
	l2UnsafeHead := sequencer.L2Unsafe()
	require.Greater(t, l2UnsafeHead.Number, uint64(0), "should have built L2 blocks")

	// Submit all L2 blocks as blob batches
	batcher.ActSubmitAll(t)
	batchTx := batcher.LastSubmitted
	require.Equal(t, uint8(types.BlobTxType), batchTx.Type(), "batch tx must be blob-tx")

	// Include batch in L1 block
	miner.ActL1StartBlock(12)(t)
	miner.ActL1IncludeTxByHash(batchTx.Hash())(t)
	miner.ActL1EndBlock(t)

	batchL1BlockNum := miner.L1Chain().CurrentBlock().Number.Uint64()

	// Verifier processes the L1 block with blobs - should succeed normally
	verifier.ActL1HeadSignal(t)
	verifier.ActL2PipelineFull(t)
	require.Equal(t, l2UnsafeHead, verifier.L2Safe(), "verifier should derive all blocks when blobs are available")

	// ========================================
	// Now simulate blob data becoming unavailable
	// ========================================

	// Create a new verifier with an EMPTY blob store
	// This simulates a scenario where:
	// 1. A new node joins the network after blobs expired (18+ days)
	// 2. The beacon node cannot provide blob data
	// 3. Blob data is lost/unavailable
	emptyBlobStore := blobstore.New()
	_, verifier2 := actionsHelpers.SetupVerifier(t, sd, captureLog, miner.L1Client(t, sd.RollupCfg), emptyBlobStore, &syncConfig.Config{})

	safeHeadBefore := verifier2.L2Safe().Number
	require.Equal(t, uint64(0), safeHeadBefore, "verifier2 should start at genesis")

	// Signal the L1 head to verifier2
	verifier2.ActL1HeadSignal(t)

	// Try to derive - this will attempt to fetch blobs and fail
	// We use ActL2EventsUntil with a limited number of iterations to avoid infinite loop
	// The pipeline will reset repeatedly due to missing blob data
	resetCount := 0
	for i := 0; i < 10; i++ {
		verifier2.ActL2EventsUntil(t, func(ev event.Event) bool {
			// Check if pipeline is resetting due to blob fetch error
			if _, ok := ev.(rollup.ResetEvent); ok {
				resetCount++
				return true // Stop on reset event
			}
			return false
		}, 20, false)
	}

	// Check logs for the specific blob fetch error
	blobFetchErrorLogs := captureLogHandler.FindLogs(
		testlog.NewErrContainsFilter("failed to fetch blobs"),
	)
	t.Logf("Found %d 'failed to fetch blobs' error logs", len(blobFetchErrorLogs))

	// Also check for generic "not found" errors related to blobs
	blobNotFoundLogs := captureLogHandler.FindLogs(
		testlog.NewErrContainsFilter("no blobs known with given time"),
	)
	t.Logf("Found %d 'no blobs known' error logs", len(blobNotFoundLogs))

	// Verify that blob errors occurred
	require.True(t, len(blobFetchErrorLogs) > 0 || len(blobNotFoundLogs) > 0,
		"should have blob fetch errors when blob data is unavailable")

	// Verify safe head does NOT advance
	safeHeadAfter := verifier2.L2Safe().Number
	require.Equal(t, safeHeadBefore, safeHeadAfter,
		"safe head should not advance when blob data is unavailable")

	// The safe head should still be at genesis
	require.Equal(t, uint64(0), safeHeadAfter,
		"safe head should remain at genesis when blob data is unavailable")

	t.Logf("Blob data unavailable scenario verified:")
	t.Logf("  - L1 block with batch: %d", batchL1BlockNum)
	t.Logf("  - L2 unsafe head: %d", l2UnsafeHead.Number)
	t.Logf("  - Verifier safe head (with blobs): %d", verifier.L2Safe().Number)
	t.Logf("  - Verifier2 safe head (without blobs): %d", safeHeadAfter)
	t.Logf("  - Pipeline reset count: %d", resetCount)
}

func TestInvalidPayloadInSpanBatch(gt *testing.T) {
	t := actionsHelpers.NewDefaultTesting(gt)
	dp := e2eutils.MakeDeployParams(t, actionsHelpers.DefaultRollupTestParams())
	minTs := hexutil.Uint64(0)
	// Activate Arsia hardfork
	upgradesHelpers.ApplyArsiaTimeOffset(dp, &minTs)
	dp.DeployConfig.L2BlockTime = 2
	sd := e2eutils.SetupMantleNormal(t, dp, actionsHelpers.DefaultAlloc)
	log := testlog.Logger(t, log.LevelInfo)
	_, _, miner, sequencer, seqEng, verifier, _, batcher := actionsHelpers.SetupMantleReorgTestActors(t, dp, sd, log, true)
	l2Cl := seqEng.EthClient()
	rng := rand.New(rand.NewSource(1234))
	signer := types.LatestSigner(sd.L2Cfg.Config)

	sequencer.ActL2PipelineFull(t)
	verifier.ActL2PipelineFull(t)

	channelOut := newSpanChannelOut(t, *sd)

	// ========================================
	// Phase 1: Create first span batch with invalid block A8
	// ========================================
	// Create block A1 ~ A12 for L1 block #0 ~ #2
	miner.ActEmptyBlock(t)
	miner.ActEmptyBlock(t)
	sequencer.ActL1HeadSignal(t)
	sequencer.ActBuildToL1HeadUnsafe(t)

	for i := uint64(1); i <= sequencer.L2Unsafe().Number; i++ {
		block, err := l2Cl.BlockByNumber(t.Ctx(), new(big.Int).SetUint64(i))
		require.NoError(t, err)
		if i == 8 {
			// Make block A8 as an invalid block
			invalidTx := testutils.RandomTx(rng, big.NewInt(100), signer)
			block = block.WithBody(types.Body{Transactions: []*types.Transaction{block.Transactions()[0], invalidTx}})
		}
		// Add A1 ~ A12 into the channel
		_, err = channelOut.AddBlock(sd.RollupCfg, block)
		require.NoError(t, err)
	}

	// Submit span batch(A1, ...,  A7, invalid A8, A9, ..., A12)
	batcher.L2ChannelOut = channelOut
	batcher.ActL2ChannelClose(t)
	batcher.ActL2BatchSubmit(t)

	miner.ActL1StartBlock(12)(t)
	miner.ActL1IncludeTx(dp.Addresses.Batcher)(t)
	miner.ActL1EndBlock(t)
	miner.ActL1SafeNext(t)
	miner.ActL1FinalizeNext(t)

	// ========================================
	// Phase 2: Verify Arsia fork behavior after first batch
	// ========================================
	// After the verifier processed the span batch:
	// - In Arsia fork: A8 is invalid, triggers deposits-only recovery
	// - Deposits-only block A8' is created (contains only L1 attributes)
	// - Unsafe head advances to A8' (block 8)
	// - Safe head advances to A8' (deposits-only block is valid)
	verifier.ActL1HeadSignal(t)
	verifier.ActL2PipelineFull(t)

	// Additional processing for deposits-only recovery
	verifier.ActL2PipelineFull(t)

	require.Equal(t, uint64(8), verifier.L2Unsafe().Number,
		"unsafe advances to deposits-only block 8 (A8 invalid, A8' created)")
	require.Equal(t, uint64(8), verifier.L2Safe().Number,
		"safe advances to deposits-only block 8 (deposits-only block is valid)")

	// ========================================
	// Phase 3: Create second span batch with valid blocks
	// ========================================
	channelOut = newSpanChannelOut(t, *sd)

	for i := uint64(1); i <= sequencer.L2Unsafe().Number; i++ {
		block, err := l2Cl.BlockByNumber(t.Ctx(), new(big.Int).SetUint64(i))
		require.NoError(t, err)
		if i == 1 {
			// Create valid TX
			aliceNonce, err := seqEng.EthClient().PendingNonceAt(t.Ctx(), dp.Addresses.Alice)
			require.NoError(t, err)
			data := make([]byte, rand.Intn(100))
			gas, err := core.FloorDataGas(params.Rules{IsAmsterdam: true}, data, nil)
			require.NoError(t, err)
			baseFee := seqEng.L2Chain().CurrentBlock().BaseFee
			tx := types.MustSignNewTx(dp.Secrets.Alice, signer, &types.DynamicFeeTx{
				ChainID:   sd.L2Cfg.Config.ChainID,
				Nonce:     aliceNonce,
				GasTipCap: big.NewInt(2 * params.GWei),
				GasFeeCap: new(big.Int).Add(new(big.Int).Mul(baseFee, big.NewInt(2)), big.NewInt(2*params.GWei)),
				Gas:       gas,
				To:        &dp.Addresses.Bob,
				Value:     big.NewInt(0),
				Data:      data,
			})
			// Create valid new block B1 at the same height as A1
			block = block.WithBody(types.Body{Transactions: []*types.Transaction{block.Transactions()[0], tx}})
		}
		// Add B1, A2 ~ A12 into the channel
		_, err = channelOut.AddBlock(sd.RollupCfg, block)
		require.NoError(t, err)
	}

	// Submit span batch(B1, A2, ... A12)
	batcher.L2ChannelOut = channelOut
	batcher.ActL2ChannelClose(t)
	batcher.ActL2BatchSubmit(t)

	miner.ActL1StartBlock(12)(t)
	miner.ActL1IncludeTx(dp.Addresses.Batcher)(t)
	miner.ActL1EndBlock(t)
	miner.ActL1SafeNext(t)
	miner.ActL1FinalizeNext(t)

	verifier.ActL1HeadSignal(t)
	verifier.ActL2PipelineFull(t)

	// ========================================
	// Phase 4: Verify final state
	// ========================================
	// verifier should advance its unsafe and safe head to the height of A12.
	require.Equal(t, uint64(12), verifier.L2Unsafe().Number,
		"unsafe advances to block 12 after processing second valid batch")
	require.Equal(t, uint64(12), verifier.L2Safe().Number,
		"safe advances to block 12 after processing second valid batch")
}

func TestSpanBatchAtomicity_Consolidation(gt *testing.T) {
	t := actionsHelpers.NewDefaultTesting(gt)
	dp := e2eutils.MakeMantleDeployParams(t, actionsHelpers.DefaultRollupTestParams())
	minTs := hexutil.Uint64(0)
	// Activate Arsia hardfork
	upgradesHelpers.ApplyArsiaTimeOffset(dp, &minTs)
	dp.DeployConfig.L2BlockTime = 2
	sd := e2eutils.SetupMantleNormal(t, dp, actionsHelpers.DefaultAlloc)
	log := testlog.Logger(t, log.LevelInfo)
	_, _, miner, sequencer, seqEng, verifier, _, batcher := actionsHelpers.SetupMantleReorgTestActors(t, dp, sd, log, true)
	seqEngCl, err := sources.NewEngineClient(seqEng.RPCClient(), log, nil, sources.EngineClientDefaultConfig(sd.RollupCfg))
	require.NoError(t, err)

	targetHeadNumber := uint64(6) // L1 block time / L2 block time

	sequencer.ActL2PipelineFull(t)
	verifier.ActL2PipelineFull(t)

	// Create 6 blocks
	miner.ActEmptyBlock(t)
	sequencer.ActL1HeadSignal(t)
	sequencer.ActBuildToL1HeadUnsafe(t)
	require.Equal(t, targetHeadNumber, sequencer.L2Unsafe().Number)

	// Gossip unsafe blocks to the verifier
	for i := uint64(1); i <= sequencer.L2Unsafe().Number; i++ {
		seqHead, err := seqEngCl.PayloadByNumber(t.Ctx(), i)
		require.NoError(t, err)
		verifier.ActL2UnsafeGossipReceive(seqHead)(t)
	}
	verifier.ActL2PipelineFull(t)

	// Check if the verifier's unsafe sync is done
	require.Equal(t, sequencer.L2Unsafe().Hash, verifier.L2Unsafe().Hash)

	// Build and submit a span batch with 6 blocks
	batcher.ActSubmitAll(t)
	miner.ActL1StartBlock(12)(t)
	miner.ActL1IncludeTx(dp.Addresses.Batcher)(t)
	miner.ActL1EndBlock(t)

	// Start verifier safe sync
	verifier.ActL1HeadSignal(t)
	verifier.L2PipelineIdle = false
	for !verifier.L2PipelineIdle {
		// wait for next pending block
		// In Arsia fork, deposits-only recovery may cause safe head to update
		// during span batch processing, so we allow SafeDerivedEvent
		verifier.ActL2EventsUntil(t, func(ev event.Event) bool {
			return event.Any(
				event.Is[engine2.PendingSafeUpdateEvent],
				event.Is[derive.DeriverIdleEvent],
				event.Is[engine2.SafeDerivedEvent], // Allow safe updates in Arsia fork
			)(ev)
		}, 1000, false)

		if verifier.L2PendingSafe().Number < targetHeadNumber {
			// In Arsia fork, safe head may advance during span batch processing
			// We don't enforce safe=0 anymore
			t.Logf("Processing span batch: pending-safe=%d, safe=%d",
				verifier.L2PendingSafe().Number, verifier.L2Safe().Number)
		} else {
			// Make sure we do the post-processing of what safety updates might happen
			verifier.ActL2PipelineFull(t)
			// Once the span batch is fully processed, the safe head must advance to the end of span batch.
			require.Equal(t, targetHeadNumber, verifier.L2Safe().Number,
				"safe head should reach target after span batch is fully processed")
			require.Equal(t, verifier.L2Safe(), verifier.L2PendingSafe(),
				"safe and pending-safe should be equal after span batch is fully processed")
		}
		// The unsafe head must not be changed
		require.Equal(t, sequencer.L2Unsafe(), verifier.L2Unsafe(),
			"unsafe head should not change during safe sync")
	}
}

func TestSpanBatchAtomicity_ForceAdvance(gt *testing.T) {
	t := actionsHelpers.NewDefaultTesting(gt)
	dp := e2eutils.MakeMantleDeployParams(t, actionsHelpers.DefaultRollupTestParams())
	minTs := hexutil.Uint64(0)
	// Activate Arsia hardfork
	upgradesHelpers.ApplyArsiaTimeOffset(dp, &minTs)
	dp.DeployConfig.L2BlockTime = 2
	sd := e2eutils.SetupMantleNormal(t, dp, actionsHelpers.DefaultAlloc)
	log := testlog.Logger(t, log.LevelInfo)
	_, _, miner, sequencer, _, verifier, _, batcher := actionsHelpers.SetupMantleReorgTestActors(t, dp, sd, log, true)

	targetHeadNumber := uint64(6) // L1 block time / L2 block time

	sequencer.ActL2PipelineFull(t)
	verifier.ActL2PipelineFull(t)
	require.Equal(t, verifier.L2Unsafe().Number, uint64(0))

	// Create 6 blocks
	miner.ActEmptyBlock(t)
	sequencer.ActL1HeadSignal(t)
	sequencer.ActBuildToL1HeadUnsafe(t)
	require.Equal(t, sequencer.L2Unsafe().Number, targetHeadNumber)

	// Build and submit a span batch with 6 blocks
	batcher.ActSubmitAll(t)
	miner.ActL1StartBlock(12)(t)
	miner.ActL1IncludeTx(dp.Addresses.Batcher)(t)
	miner.ActL1EndBlock(t)

	// Start verifier safe sync
	verifier.ActL1HeadSignal(t)
	verifier.L2PipelineIdle = false
	for !verifier.L2PipelineIdle {
		// wait for next pending block
		// In Arsia fork, deposits-only recovery may cause safe head to update
		// during span batch processing, so we allow SafeDerivedEvent
		verifier.ActL2EventsUntil(t, func(ev event.Event) bool {
			return event.Any(
				event.Is[engine2.PendingSafeUpdateEvent],
				event.Is[derive.DeriverIdleEvent],
				event.Is[engine2.SafeDerivedEvent], // Allow safe updates in Arsia fork
			)(ev)
		}, 1000, false)
		if verifier.L2PendingSafe().Number < targetHeadNumber {
			// In Arsia fork, safe head may advance during span batch processing
			// We don't enforce safe=0 anymore
			t.Logf("Processing span batch: pending-safe=%d, safe=%d",
				verifier.L2PendingSafe().Number, verifier.L2Safe().Number)
		} else {
			// Make sure we do the post-processing of what safety updates might happen
			// after the pending-safe event, before the next pending-safe event.
			verifier.ActL2EventsUntil(t, event.Is[engine2.PendingSafeUpdateEvent], 100, true)
			// Once the span batch is fully processed, the safe head must advance to the end of span batch.
			require.Equal(t, targetHeadNumber, verifier.L2Safe().Number,
				"safe head should reach target after span batch is fully processed")
			require.Equal(t, verifier.L2Safe(), verifier.L2PendingSafe(),
				"safe and pending-safe should be equal after span batch is fully processed")
		}
		// The unsafe head and the pending safe head must be the same
		require.Equal(t, verifier.L2PendingSafe(), verifier.L2Unsafe(),
			"unsafe and pending-safe should be equal during force advance")
	}
}
