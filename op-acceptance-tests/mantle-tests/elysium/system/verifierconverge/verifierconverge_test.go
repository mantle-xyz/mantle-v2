package verifierconverge

import (
	"testing"
	"time"

	opforks "github.com/ethereum-optimism/optimism/op-core/forks"
	"github.com/ethereum-optimism/optimism/op-devstack/devtest"
	"github.com/ethereum-optimism/optimism/op-devstack/presets"
	suptypes "github.com/ethereum-optimism/optimism/op-supervisor/supervisor/types"
)

// TestL1Glamsterdam_VerifierConverge proves an independent verifier derives a byte-identical L2
// chain to the sequencer across the L1 Glamsterdam (Amsterdam) boundary — from L1, not from the
// sequencer's unsafe p2p gossip. Cross-safe is each node's own L1-derived head, and an equal
// cross-safe hash on both nodes implies byte-identical chains up to it: deterministic derivation
// across the fork.
//
// Topology prerequisite: the system must expose at least TWO L2 CL nodes on the chain — an active
// sequencer plus an independent follower. NewMantleSingleChainMultiNode resolves that follower
// through match.Assume, which by design SKIPS the test rather than failing it when nothing matches,
// so the suite stays portable across topologies that have no independent verifier. TestMain brings
// up the sysgo multi-node system, which satisfies this.
//
// Worth knowing when reading a green CI run: because the miss is a skip, a follower that fails to
// come up is reported as "skipped" and `go test` still exits 0. What guarantees this case actually
// ran is the gate's fixed topology, not anything this test asserts about itself.
func TestL1Glamsterdam_VerifierConverge(gt *testing.T) {
	t := devtest.SerialT(gt)
	sys := presets.NewMantleSingleChainMultiNode(t)
	require := t.Require()

	l1Config := sys.L1Network.Escape().ChainConfig()
	require.True(sys.L2Chain.IsMantleForkActive(opforks.MantleElysium), "L2 must run with Mantle Elysium active")
	require.NotNil(l1Config.AmsterdamTime, "L1 AmsterdamTime must be configured")
	amsterdamTime := *l1Config.AmsterdamTime
	sys.L1EL.WaitForTime(amsterdamTime)

	// Advance the sequencer's safe head until it derives from a post-Amsterdam L1 origin, so the
	// head the two nodes converge on was re-derived from a Glamsterdam L1, not a pre-fork one.
	require.Eventually(func() bool {
		s := sys.L2CL.SyncStatus().SafeL2
		return s.Number > 0 && sys.L1EL.BlockRefByNumber(s.L1Origin.Number).Time >= amsterdamTime
	}, 120*time.Second, time.Second, "sequencer safe head must derive from a post-Amsterdam L1 origin")

	// The independent verifier's cross-safe head must converge to the sequencer's — same number
	// AND hash (Matched auto-checks every follower, covering a reth verifier under DEVSTACK_L2EL_KIND).
	sys.L2CLB.Matched(sys.L2CL, suptypes.CrossSafe, 60)

	// ...and that converged head must be post-Amsterdam, so the convergence genuinely spans the fork.
	converged := sys.L2CL.SyncStatus().SafeL2
	require.GreaterOrEqual(sys.L1EL.BlockRefByNumber(converged.L1Origin.Number).Time, amsterdamTime,
		"converged safe block's L1 origin (block %d) must be post-Amsterdam", converged.L1Origin.Number)
}
