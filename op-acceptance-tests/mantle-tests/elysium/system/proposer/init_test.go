package proposer

import (
	"testing"

	"github.com/ethereum-optimism/optimism/op-acceptance-tests/mantle-tests/elysium/internal/testmain"
)

// The case in this package uses presets.NewMantleMinimal, so it needs RunMinimal
// (sysgo.DefaultMantleMinimalSystem), which is also what wires op-proposer in legacy
// L2OutputOracle mode via WithLegacyProposer.
//
// PostBoundaryAmsterdamOffset: the test observes nothing before the activation boundary. Its very
// first step is WaitForGlamsterdamL1, and every assertion afterwards only looks at post-Amsterdam
// L1 blocks (blocks failing l1Config.IsAmsterdam are skipped outright). A long pre-Amsterdam
// window would just be wall-clock spent waiting.
//
// The offset is also safe for the proposer's submission cadence: sysgo configures
// ProposalInterval = 6s (op-devstack/sysgo/l2_proposer.go), so once the L2 is past the boundary
// the proposer lands a fresh proposeL2Output call every few seconds -- comfortably inside the
// test's 240s deadline and its 64-block backward scan window. Activating Amsterdam earlier only
// helps here: it makes post-Amsterdam proposer txs available sooner, never later.
func TestMain(m *testing.M) {
	testmain.RunMinimal(m, testmain.PostBoundaryAmsterdamOffset)
}
