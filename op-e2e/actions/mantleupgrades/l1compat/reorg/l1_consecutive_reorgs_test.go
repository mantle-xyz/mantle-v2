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

// TestL1Reorg_ConsecutiveReorgs pins that multiple back-to-back L1 reorgs
// post-Amsterdam (A → B → C in rapid
// succession) must not deadlock or corrupt L2 state. The verifier must
// keep up with each reorg and ultimately converge on whichever chain wins.
//
// Test shape (when unblocked):
//  1. Setup with `MakeL1GlamsterdamL2ArsiaDeployParams`, small amsterdamOffset.
//  2. Mine chain A past Amsterdam; build L2 + batch.
//  3. Reorg to chain B, longer than A; build L2 + batch on chain B.
//  4. Reorg to chain C, longer than B; build L2 + batch on chain C.
//  5. Assert verifier L2 safe head L1Origin is on chain C and that no
//     deriver error / panic surfaced across the three transitions.
//
// STATUS: skeleton only.
func TestL1Reorg_ConsecutiveReorgs(gt *testing.T) {
	gt.Skip("blocked by op-geth: post-Amsterdam L1 block construction does not populate Header.BlockAccessListHash, so op-e2e L1Miner cannot mine a valid Amsterdam block yet")

	t := actionsHelpers.NewDefaultTesting(gt)
	amsterdamOffset := hexutil.Uint64(24)
	dp := l1compatHelpers.MakeL1GlamsterdamL2ArsiaDeployParams(t, l1compatHelpers.DefaultRollupTestParams(), &amsterdamOffset)
	sd := e2eutils.SetupMantleNormal(t, dp, actionsHelpers.DefaultAlloc)
	logger := testlog.Logger(t, log.LevelDebug)

	_, _, _, _, _, _, _, _ = actionsHelpers.SetupMantleReorgTestActors(t, dp, sd, logger, true)

	// TODO(unblock): mine chain A, reorg to B (longer), reorg to C (longer
	// than B), drive verifier through each, assert convergence on C. Pivot
	// hashes/numbers captured between reorgs to detect divergence early.
}
