package reorg

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/log"
	"github.com/stretchr/testify/require"

	actionsHelpers "github.com/ethereum-optimism/optimism/op-e2e/actions/helpers"
	l1compatHelpers "github.com/ethereum-optimism/optimism/op-e2e/actions/mantleupgrades/l1compat/helpers"
	"github.com/ethereum-optimism/optimism/op-e2e/e2eutils"
	"github.com/ethereum-optimism/optimism/op-service/eth"
	"github.com/ethereum-optimism/optimism/op-service/testlog"
)

// TestL1Reorg_AtUpgradeActivation pins that when an L1 reorg straddles the
// Glamsterdam (Amsterdam) activation boundary —
// the pre-reorg canonical branch crosses activation, then a new longer branch
// replaces both the activation block and post-activation blocks — L2 must
// remain consistent: rewind safe/unsafe off the abandoned branch, re-derive
// against the new branch, and ultimately converge.
//
// Test shape (when unblocked):
//  1. SETUP: L1 with Amsterdam offset = N (small but > 0); L2 Arsia at L1 genesis.
//  2. ADVANCE: mine L1 to one block before Amsterdam activation. Snapshot the
//     "pre-pivot" L1 head.
//  3. CROSS-CHAIN-A: mine L1 across activation (a few post-Amsterdam blocks).
//     Build L2 forward to chain A's post-activation L1 head and submit batch.
//  4. REORG-TO-CHAIN-B: rewind L1 to the pre-pivot snapshot, then mine a
//     different chain B that also crosses Amsterdam activation and is longer
//     than chain A.
//  5. CONVERGE: signal verifier + sequencer, assert both end on a head whose
//     L1 origin is on chain B and that
//     sd.L1Cfg.Config.IsAmsterdam(L1Origin.Number, L1Origin.Time) is true.
//
// STATUS: skeleton only. The full body below documents the intended actor
// sequence but is gated by an op-geth blocker (see t.Skip reason). Remove the
// Skip once the blocker is resolved.
func TestL1Reorg_AtUpgradeActivation(gt *testing.T) {
	gt.Skip("blocked by op-geth: post-Amsterdam L1 block construction does not populate Header.BlockAccessListHash, so op-e2e L1Miner cannot mine a valid Amsterdam block yet")

	t := actionsHelpers.NewDefaultTesting(gt)
	amsterdamOffset := hexutil.Uint64(24)
	dp := l1compatHelpers.MakeL1GlamsterdamL2ArsiaDeployParams(t, l1compatHelpers.DefaultRollupTestParams(), &amsterdamOffset)
	sd := e2eutils.SetupMantleNormal(t, dp, actionsHelpers.DefaultAlloc)
	logger := testlog.Logger(t, log.LevelDebug)

	// isSpanBatch=true to match modern Mantle Arsia batching behavior.
	sd, _, miner, sequencer, _, verifier, _, batcher := actionsHelpers.SetupMantleReorgTestActors(t, dp, sd, logger, true)
	minerCl := miner.L1Client(t, sd.RollupCfg)

	isPostAmsterdam := func(ref eth.L1BlockRef) bool {
		return sd.L1Cfg.Config.IsAmsterdam(new(big.Int).SetUint64(ref.Number), ref.Time)
	}

	sequencer.ActL2PipelineFull(t)
	verifier.ActL2PipelineFull(t)

	// PHASE 1: mine L1 to the last pre-Amsterdam block and snapshot the pivot.
	miner.ActL1SetFeeRecipient(common.Address{'A', 0})
	const maxPreActivationBlocks = 8
	var pivot eth.L1BlockRef
	for i := 0; i < maxPreActivationBlocks; i++ {
		head, err := minerCl.L1BlockRefByLabel(t.Ctx(), eth.Unsafe)
		require.NoError(t, err)
		pivot = head
		miner.ActEmptyBlock(t)
		next, err := minerCl.L1BlockRefByLabel(t.Ctx(), eth.Unsafe)
		require.NoError(t, err)
		if isPostAmsterdam(next) {
			break
		}
	}
	require.False(t, isPostAmsterdam(pivot), "pivot must be the last pre-Amsterdam block")

	// PHASE 2: extend chain A across activation, build L2, submit batch.
	chainAActivation, err := minerCl.L1BlockRefByLabel(t.Ctx(), eth.Unsafe)
	require.NoError(t, err)
	require.True(t, isPostAmsterdam(chainAActivation), "chainAActivation must be post-Amsterdam")
	miner.ActL1SetFeeRecipient(common.Address{'A', 1})
	miner.ActEmptyBlock(t)
	miner.ActL1SetFeeRecipient(common.Address{'A', 2})
	miner.ActEmptyBlock(t)

	sequencer.ActL1HeadSignal(t)
	sequencer.ActBuildToL1Head(t)
	batcher.ActSubmitAll(t)

	miner.ActL1SetFeeRecipient(common.Address{'A', 3})
	miner.ActL1StartBlock(12)(t)
	miner.ActL1IncludeTx(sd.RollupCfg.Genesis.SystemConfig.BatcherAddr)(t)
	batchTxA := miner.L1Transactions[0]
	miner.ActL1EndBlock(t)

	verifier.ActL1HeadSignal(t)
	verifier.ActL2PipelineFull(t)
	// verifier safe origin should sit on chain A (post-Amsterdam side).
	require.Greater(t, verifier.L2Safe().L1Origin.Number, pivot.Number, "verifier safe origin must be past the pivot, i.e. on chain A's post-Amsterdam segment")

	// PHASE 3: rewind to pivot, then build chain B across activation, longer than A.
	headBeforeReorg, err := minerCl.L1BlockRefByLabel(t.Ctx(), eth.Unsafe)
	require.NoError(t, err)
	for {
		current, err := minerCl.L1BlockRefByLabel(t.Ctx(), eth.Unsafe)
		require.NoError(t, err)
		if current.Hash == pivot.Hash {
			break
		}
		miner.ActL1RewindToParent(t)
	}

	miner.ActL1SetFeeRecipient(common.Address{'B', 0})
	miner.ActEmptyBlock(t) // crosses Amsterdam on chain B
	miner.ActL1SetFeeRecipient(common.Address{'B', 1})
	miner.ActL1StartBlock(12)(t)
	require.NoError(t, miner.Eth.TxPool().Add([]*types.Transaction{batchTxA}, true)[0])
	miner.ActL1IncludeTx(sd.RollupCfg.Genesis.SystemConfig.BatcherAddr)(t)
	miner.ActL1EndBlock(t)

	// Extend chain B past the height chain A reached, so the verifier picks it.
	for {
		current, err := minerCl.L1BlockRefByLabel(t.Ctx(), eth.Unsafe)
		require.NoError(t, err)
		if current.Number > headBeforeReorg.Number {
			break
		}
		miner.ActL1SetFeeRecipient(common.Address{'B', 2})
		miner.ActEmptyBlock(t)
	}
	finalHead, err := minerCl.L1BlockRefByLabel(t.Ctx(), eth.Unsafe)
	require.NoError(t, err)
	require.True(t, isPostAmsterdam(finalHead), "chain B head must be post-Amsterdam")

	// PHASE 4: converge.
	verifier.ActL1HeadSignal(t)
	verifier.ActL2PipelineFull(t)
	sequencer.ActL1HeadSignal(t)
	sequencer.ActL2PipelineFull(t)
	sequencer.ActBuildToL1Head(t)

	// Final state: verifier + sequencer L1 origin is on chain B (past pivot).
	require.Greater(t, verifier.L2Safe().L1Origin.Number, pivot.Number, "verifier safe origin must be past the pivot on chain B")
	require.Greater(t, sequencer.L2Unsafe().L1Origin.Number, pivot.Number, "sequencer unsafe origin must be past the pivot on chain B")
}
