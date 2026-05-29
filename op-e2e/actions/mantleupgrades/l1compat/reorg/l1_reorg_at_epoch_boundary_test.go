package reorg

import (
	"testing"

	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/log"

	actionsHelpers "github.com/ethereum-optimism/optimism/op-e2e/actions/helpers"
	l1compatHelpers "github.com/ethereum-optimism/optimism/op-e2e/actions/mantleupgrades/l1compat/helpers"
	"github.com/ethereum-optimism/optimism/op-e2e/e2eutils"
	"github.com/ethereum-optimism/optimism/op-service/testlog"
)

// TestL1Reorg_AtEpochBoundary_PostUpgrade pins that when a post-Amsterdam L1
// reorg drops the block at the boundary of a
// derivation epoch (sequencing-window boundary), the verifier must still
// derive correctly on the new branch and produce a contiguous L2 history
// — no missed batches, no double-applied attributes, no panic.
//
// Test shape (when unblocked):
//  1. Setup `MakeL1GlamsterdamL2ArsiaDeployParams`, amsterdamOffset small.
//  2. Mine L1 past Amsterdam; advance to an L1 block whose number is exactly
//     `sd.RollupCfg.Genesis.L1.Number + k * SequencerWindowSize` for some k.
//  3. Build L2 to that L1 head and submit a batch.
//  4. Rewind that boundary block and mine an alternative one in its place,
//     plus enough follow-ups for the reorg to win.
//  5. Re-run verifier pipeline; assert L2 safe head L1 origin is on the new
//     branch and that no derivation error / panic surfaced.
//
// STATUS: skeleton only.
func TestL1Reorg_AtEpochBoundary_PostUpgrade(gt *testing.T) {
	gt.Skip("blocked by op-geth: post-Amsterdam L1 block construction does not populate Header.BlockAccessListHash, so op-e2e L1Miner cannot mine a valid Amsterdam block yet")

	t := actionsHelpers.NewDefaultTesting(gt)
	amsterdamOffset := hexutil.Uint64(24)
	dp := l1compatHelpers.MakeL1GlamsterdamL2ArsiaDeployParams(t, l1compatHelpers.DefaultRollupTestParams(), &amsterdamOffset)
	sd := e2eutils.SetupMantleNormal(t, dp, actionsHelpers.DefaultAlloc)
	logger := testlog.Logger(t, log.LevelDebug)

	_, _, _, _, _, _, _, _ = actionsHelpers.SetupMantleReorgTestActors(t, dp, sd, logger, true)

	// TODO(unblock): advance L1 to an epoch boundary post-Amsterdam, build
	// L2+batch on chain A, reorg the boundary block, verify L2 follows chain B.
	// `SequencerWindowSize` is on sd.RollupCfg.
}
