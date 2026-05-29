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

// TestBoundary_L1EpochCrossActivation pins that when L1 Amsterdam activates
// exactly at a derivation epoch boundary —
// i.e. activation lands on an L1 block that is the first of a new
// sequencing window — L2 derivation must still emit a coherent epoch:
// the new-format header is well-formed and the L2 deposits / system info
// it carries does not corrupt the new epoch's first L2 block.
//
// Test shape (when unblocked):
//  1. Setup with `MakeL1GlamsterdamL2ArsiaDeployParams`. Pick
//     `amsterdamOffset` so that Amsterdam activates exactly on a block
//     whose `Number == sd.RollupCfg.Genesis.L1.Number + k * SequencerWindowSize`.
//  2. Mine L1 to the activation block (the new-epoch block).
//  3. Build L2 forward; sequencer must emit a new epoch's first L2 block
//     anchored to the activation block.
//  4. Submit batch; verifier must accept and derive the same epoch.
//  5. Assert L2 head L1Origin == activation block and that the activation
//     block is post-Amsterdam (`IsAmsterdam` true) on a clean activation
//     boundary.
//
// STATUS: skeleton only.
func TestBoundary_L1EpochCrossActivation(gt *testing.T) {
	gt.Skip("blocked by op-geth: post-Amsterdam L1 block construction does not populate Header.BlockAccessListHash, so op-e2e L1Miner cannot mine a valid Amsterdam block yet")

	t := actionsHelpers.NewDefaultTesting(gt)
	// amsterdamOffset to be chosen so activation lands on an epoch boundary.
	// SequencerWindowSize is on sd.RollupCfg; L1 block time is 12s for these
	// tests, so amsterdamOffset = 12 * (windowSize - 1) puts Amsterdam on
	// the second L1 block of the second window. Tune as needed.
	amsterdamOffset := hexutil.Uint64(24)
	dp := l1compatHelpers.MakeL1GlamsterdamL2ArsiaDeployParams(t, l1compatHelpers.DefaultRollupTestParams(), &amsterdamOffset)
	sd := e2eutils.SetupMantleNormal(t, dp, actionsHelpers.DefaultAlloc)
	logger := testlog.Logger(t, log.LevelDebug)

	_, _, _, _, _, _, _, _ = actionsHelpers.SetupMantleReorgTestActors(t, dp, sd, logger, true)

	// TODO(unblock): compute the precise amsterdamOffset to land on an epoch
	// boundary using sd.RollupCfg.SeqWindowSize and L1 block time; mine to that
	// activation block; drive L2 to derive the new epoch and assert origin +
	// IsAmsterdam.
}
