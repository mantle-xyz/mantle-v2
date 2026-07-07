package reorg

import (
	"testing"
	"time"

	"github.com/ethereum-optimism/optimism/op-devstack/devtest"
	"github.com/ethereum-optimism/optimism/op-devstack/presets"
	"github.com/ethereum-optimism/optimism/op-devstack/stack"
	"github.com/ethereum-optimism/optimism/op-devstack/stack/match"
	"github.com/ethereum-optimism/optimism/op-service/eth"
	"github.com/ethereum-optimism/optimism/op-test-sequencer/sequencer/seqtypes"
	"github.com/ethereum/go-ethereum/common"
)

const l1BlockTime = 6 * time.Second

// TestL1Reorg_AtUpgradeActivation drives the L1 past the Amsterdam activation
// while keeping the L2 in step, then reorgs a recent post-Amsterdam L1 block that
// the L2 derives from (above the finalized horizon and within max reorg depth), by
// building a competing chain on its parent. It asserts the L1 reorgs and the L2
// reorgs its own chain and keeps advancing (no wedge). (M4-1)
//
// This test takes exclusive control of L1 production, so it is the only test in
// this package: two L1-driving tests cannot share one devstack system.
func TestL1Reorg_AtUpgradeActivation(gt *testing.T) {
	t := devtest.SerialT(gt)
	sys := presets.NewMantleSingleChainMultiNodeWithTestSeq(t)
	require := t.Require()
	logger := t.Logger()
	ctx := t.Ctx()

	ts := sys.TestSequencer.Escape().ControlAPI(sys.L1Network.ChainID())
	cl := sys.L1Network.Escape().L1CLNode(match.FirstL1CL)

	sys.L1Network.WaitForBlock()

	// Take manual control of L1 up front. With the auto-FakePoS stopped, advancing
	// the (time-travel) clock moves only the real-time-bound L2 sequencer, not the
	// L1 — so we can let the L2 origin catch up between manually-produced L1 blocks.
	// This keeps the L2 origin within ~confDepth of the L1 head, so the reorg target
	// stays above both the finalized horizon (20) and the max reorg depth (32).
	sys.ControlPlane.FakePoSState(cl.ID(), stack.Stop)

	// driveL1InStep produces one L1 block, then advances the clock until the L2
	// unsafe origin catches up to the new L1 head.
	driveL1InStep := func() {
		require.NoError(ts.New(ctx, seqtypes.BuildOpts{Parent: common.Hash{}}))
		require.NoError(ts.Next(ctx))
		require.Eventually(func() bool {
			sys.AdvanceTime(2 * time.Second)
			l1head := sys.L1EL.BlockRefByLabel(eth.Unsafe).Number
			l2origin := sys.L2EL.BlockRefByLabel(eth.Unsafe).L1Origin.Number
			return l1head-l2origin <= 2
		}, 60*time.Second, 200*time.Millisecond)
	}

	require.Eventually(func() bool {
		driveL1InStep()
		l1head := sys.L1EL.BlockRefByLabel(eth.Unsafe).Number
		l2origin := sys.L2EL.BlockRefByLabel(eth.Unsafe).L1Origin.Number
		logger.Info("in-step progress", "l1", l1head, "l2Origin", l2origin, "lag", l1head-l2origin)
		return l2origin > amsterdamOffset
	}, 180*time.Second, 100*time.Millisecond)

	l2BeforeReorg := sys.L2EL.BlockRefByLabel(eth.Unsafe)
	l1Height := l2BeforeReorg.L1Origin.Number
	l1Before := sys.L1EL.BlockRefByNumber(l1Height)
	oldHead := sys.L1EL.BlockRefByLabel(eth.Unsafe).Number
	require.Greater(l1Height, amsterdamOffset, "reorg target must be post-Amsterdam")
	require.Less(oldHead-l1Height, uint64(20), "reorg target must be above the finalized horizon")
	require.Less(oldHead-l1Height, uint64(32), "reorg must be within geth's max reorg depth")
	logger.Info("reorg target (post-Amsterdam, reorgable)", "l1Height", l1Height, "l1Head", oldHead, "l2Unsafe", l2BeforeReorg.Number)

	// Inject the L1 reorg: build a competing block on the parent of the target
	// (the TestSequencer forces it canonical), then extend past the old head.
	require.NoError(ts.New(ctx, seqtypes.BuildOpts{Parent: l1Before.ParentHash}))
	require.NoError(ts.Next(ctx))
	require.Eventually(func() bool {
		sys.AdvanceTime(2 * time.Second)
		return sys.L1EL.BlockRefByLabel(eth.Unsafe).Number >= l1Height
	}, 30*time.Second, 200*time.Millisecond)
	for sys.L1EL.BlockRefByLabel(eth.Unsafe).Number <= oldHead+1 {
		driveL1InStep()
	}

	l1After := sys.L1EL.BlockRefByNumber(l1Height)
	require.NotEqual(l1After.Hash, l1Before.Hash, "L1 must have reorged at the target height")
	logger.Info("L1 reorged", "height", l1Height, "old", l1Before.Hash, "new", l1After.Hash)

	// The L2 must detect the L1 reorg and reorg its own chain: the L2 block that
	// derived from the old L1 target must change, and the chain must keep advancing
	// (no wedge). Keep driving in step so derivation resets and re-converges.
	require.Eventually(func() bool {
		driveL1InStep()
		l2At := sys.L2EL.BlockRefByNumber(l2BeforeReorg.Number)
		l2Head := sys.L2EL.BlockRefByLabel(eth.Unsafe)
		return l2At.Hash != l2BeforeReorg.Hash && l2Head.Number > l2BeforeReorg.Number
	}, 120*time.Second, 100*time.Millisecond)

	l2At := sys.L2EL.BlockRefByNumber(l2BeforeReorg.Number)
	require.NotEqual(l2At.Hash, l2BeforeReorg.Hash, "L2 must have reorged the block that derived from the old L1")
	logger.Info("L2 reorged and kept advancing after L1 reorg across Amsterdam",
		"l2Head", sys.L2EL.BlockRefByLabel(eth.Unsafe).Number)
}
