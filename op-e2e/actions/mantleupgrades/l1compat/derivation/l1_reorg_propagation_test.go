package derivation

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

// TestDerivation_L1ReorgPropagation pins that after L1 activates Amsterdam
// (Glamsterdam), an L1 reorg that replaces a span
// of post-activation blocks must propagate to L2 — the verifier rolls back its
// L2 safe head off the abandoned L1 branch, and L2 derivation resumes on the
// new canonical L1 branch (which is itself post-Amsterdam).
//
// This is the cross-fork-boundary edge of standard L1 reorg propagation: it
// exercises the same op-node derivation rewind path that handles pre-Amsterdam
// reorgs, but does so under a chain config where IsAmsterdam(L1) == true for
// both the abandoned and the new branch.
//
// STATUS: skeleton only. The full body below documents the intended shape but
// is gated by an op-geth blocker (see t.Skip reason). Remove the Skip once the
// blocker is resolved.
func TestDerivation_L1ReorgPropagation(gt *testing.T) {
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

	// Mine L1 empty blocks past Amsterdam activation. With amsterdamOffset=24
	// and default 12s L1 block time, this takes 2-3 blocks; we cap the loop to
	// keep the test bounded.
	const maxPreAmsterdamBlocks = 8
	miner.ActL1SetFeeRecipient(common.Address{'A', 0})
	for i := 0; i < maxPreAmsterdamBlocks; i++ {
		miner.ActEmptyBlock(t)
		head, err := minerCl.L1BlockRefByLabel(t.Ctx(), eth.Unsafe)
		require.NoError(t, err)
		if isPostAmsterdam(head) {
			break
		}
	}
	blockA0, err := minerCl.L1BlockRefByLabel(t.Ctx(), eth.Unsafe)
	require.NoError(t, err)
	require.True(t, isPostAmsterdam(blockA0), "blockA0 must be post-Amsterdam")

	// Build L2 to L1 head A0, then submit batch.
	sequencer.ActL1HeadSignal(t)
	sequencer.ActBuildToL1Head(t)
	batcher.ActSubmitAll(t)

	// Include batch on chain A1.
	miner.ActL1SetFeeRecipient(common.Address{'A', 1})
	miner.ActL1StartBlock(12)(t)
	miner.ActL1IncludeTx(sd.RollupCfg.Genesis.SystemConfig.BatcherAddr)(t)
	batchTxA := miner.L1Transactions[0]
	miner.ActL1EndBlock(t)
	blockA1, err := minerCl.L1BlockRefByLabel(t.Ctx(), eth.Unsafe)
	require.NoError(t, err)
	require.True(t, isPostAmsterdam(blockA1), "blockA1 must be post-Amsterdam")

	// Verifier syncs to chain A.
	verifier.ActL1HeadSignal(t)
	verifier.ActL2PipelineFull(t)
	require.Equal(t, blockA0.ID(), verifier.L2Safe().L1Origin, "verifier safe origin must reference A0 before reorg")

	// Reorg: rewind A1 and A0 (both post-Amsterdam), build a different chain B.
	miner.ActL1RewindToParent(t) // undo A1
	miner.ActL1RewindToParent(t) // undo A0

	miner.ActL1SetFeeRecipient(common.Address{'B', 0})
	miner.ActEmptyBlock(t)
	blockB0, err := minerCl.L1BlockRefByLabel(t.Ctx(), eth.Unsafe)
	require.NoError(t, err)
	require.True(t, isPostAmsterdam(blockB0), "blockB0 must be post-Amsterdam")
	require.Equal(t, blockA0.Number, blockB0.Number, "B0 same height as A0")
	require.NotEqual(t, blockA0.Hash, blockB0.Hash, "B0 must differ from A0")

	// Re-include the original batch tx on chain B's B1, so the batch still anchors
	// to a post-Amsterdam L1 (now on the new branch).
	miner.ActL1SetFeeRecipient(common.Address{'B', 1})
	miner.ActL1StartBlock(12)(t)
	require.NoError(t, miner.Eth.TxPool().Add([]*types.Transaction{batchTxA}, true)[0])
	miner.ActL1IncludeTx(sd.RollupCfg.Genesis.SystemConfig.BatcherAddr)(t)
	miner.ActL1EndBlock(t)

	// Extend chain B so the verifier picks the reorg.
	miner.ActL1SetFeeRecipient(common.Address{'B', 2})
	miner.ActEmptyBlock(t)
	blockB2, err := minerCl.L1BlockRefByLabel(t.Ctx(), eth.Unsafe)
	require.NoError(t, err)

	// Verifier picks up the reorg.
	verifier.ActL1HeadSignal(t)
	verifier.ActL2PipelineFull(t)
	require.NotEqual(t, blockA0.Hash, verifier.L2Safe().L1Origin.Hash, "verifier L2 safe origin must NOT remain on the abandoned chain A")

	// Sync sequencer too and rebuild up to the new L1 head.
	sequencer.ActL1HeadSignal(t)
	sequencer.ActL2PipelineFull(t)
	sequencer.ActBuildToL1Head(t)
	require.Equal(t, blockB2.ID(), sequencer.L2Unsafe().L1Origin, "sequencer L2 unsafe origin = B2 after reorg")
}
