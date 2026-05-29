package boundary

import (
	"testing"

	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/log"

	actionsHelpers "github.com/ethereum-optimism/optimism/op-e2e/actions/helpers"
	l1compatHelpers "github.com/ethereum-optimism/optimism/op-e2e/actions/mantleupgrades/l1compat/helpers"
	"github.com/ethereum-optimism/optimism/op-e2e/e2eutils"
	"github.com/ethereum-optimism/optimism/op-service/testlog"
)

// TestBoundary_L1ReorgAcrossActivation is the boundary variant of
// TestL1Reorg_AtUpgradeActivation: it covers the case where
// the dropped/replaced segment straddles the activation block exactly. In
// the replacement chain B, the activation timestamp lands on a different L1
// block hash than it did on chain A. L2 must converge on chain B and the
// new epoch anchored to chain B's activation block must be well-formed.
//
// Scope difference vs TestL1Reorg_AtUpgradeActivation: that test keeps the
// activation block fixed and reorgs *post*-activation. This test reorgs the
// activation block itself — activation moves to a different block hash on
// chain B.
//
// Test shape (when unblocked):
//  1. Setup with `MakeL1GlamsterdamL2ArsiaDeployParams`, small amsterdamOffset.
//  2. Snapshot the last pre-activation block as the pivot.
//  3. Build chain A across activation; build L2 + batch on A.
//  4. Rewind below the pivot; build chain B with a different activation
//     block, longer than A.
//  5. Drive verifier through reorg; assert L2 safe origin migrates to
//     chain B's activation block (or post-activation on B) and that the
//     new origin's hash != chain A's activation block hash.
//
// STATUS: skeleton only.
func TestBoundary_L1ReorgAcrossActivation(gt *testing.T) {
	gt.Skip("blocked by op-geth: post-Amsterdam L1 block construction does not populate Header.BlockAccessListHash, so op-e2e L1Miner cannot mine a valid Amsterdam block yet")

	t := actionsHelpers.NewDefaultTesting(gt)
	amsterdamOffset := hexutil.Uint64(24)
	dp := l1compatHelpers.MakeL1GlamsterdamL2ArsiaDeployParams(t, l1compatHelpers.DefaultRollupTestParams(), &amsterdamOffset)
	sd := e2eutils.SetupMantleNormal(t, dp, actionsHelpers.DefaultAlloc)
	logger := testlog.Logger(t, log.LevelDebug)

	_, _, _, _, _, _, _, _ = actionsHelpers.SetupMantleReorgTestActors(t, dp, sd, logger, true)

	// TODO(unblock): mine chain A past activation; capture A's activation
	// block hash; rewind below activation; mine chain B such that the
	// activation block lands on a different hash; assert L2 follows B and
	// final L1 origin hash != A's activation block hash.
}
