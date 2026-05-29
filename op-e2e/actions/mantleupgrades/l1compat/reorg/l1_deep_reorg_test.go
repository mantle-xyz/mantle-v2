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

// TestL1Reorg_DeepReorg_PostUpgrade pins that a deep L1 reorg (10+ blocks
// dropped) entirely within the post-Amsterdam
// window must rewind L2 state correctly. This stresses the derivation
// pipeline's rewind path past the depth covered by
// TestDerivation_L1ReorgPropagation and TestL1Reorg_AtUpgradeActivation.
//
// Test shape (when unblocked):
//  1. Setup with `MakeL1GlamsterdamL2ArsiaDeployParams`, small amsterdamOffset.
//  2. Mine ~12-15 L1 blocks all post-Amsterdam on chain A; build L2 + batches.
//  3. Rewind 10+ L1 blocks; mine a longer chain B (all post-Amsterdam).
//  4. Re-run verifier; assert L2 safe head L1Origin is on chain B and
//     historical L2 blocks no longer reference the dropped chain A segment.
//
// STATUS: skeleton only.
func TestL1Reorg_DeepReorg_PostUpgrade(gt *testing.T) {
	gt.Skip("blocked by op-geth: post-Amsterdam L1 block construction does not populate Header.BlockAccessListHash, so op-e2e L1Miner cannot mine a valid Amsterdam block yet")

	t := actionsHelpers.NewDefaultTesting(gt)
	amsterdamOffset := hexutil.Uint64(24)
	dp := l1compatHelpers.MakeL1GlamsterdamL2ArsiaDeployParams(t, l1compatHelpers.DefaultRollupTestParams(), &amsterdamOffset)
	sd := e2eutils.SetupMantleNormal(t, dp, actionsHelpers.DefaultAlloc)
	logger := testlog.Logger(t, log.LevelDebug)

	_, _, _, _, _, _, _, _ = actionsHelpers.SetupMantleReorgTestActors(t, dp, sd, logger, true)

	// TODO(unblock): build chain A with ~12 post-Amsterdam L1 blocks, batches
	// included; rewind 10+ blocks; build chain B with ~15 blocks; assert L2 safe
	// origin migrates to chain B. Pattern: ReorgFlipFlop in
	// op-e2e/actions/mantletests/derivation/reorg_test.go, scaled deeper.
}
