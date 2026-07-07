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

// TestReorg_L2SafeAdvancesUnderTimeTravel verifies that, on a time-travel clock,
// the L2 safe head advances while the L1 is driven manually via the
// TestSequencer across the Amsterdam boundary. On the wall clock the same driving
// pattern stalls L2 derivation (L1 races ahead of real time). This is the
// prerequisite for the L1-reorg acceptance tests.
func TestReorg_L2SafeAdvancesUnderTimeTravel(gt *testing.T) {
	t := devtest.SerialT(gt)
	sys := presets.NewMantleSingleChainMultiNodeWithTestSeq(t)
	require := t.Require()
	logger := t.Logger()
	ctx := t.Ctx()

	ts := sys.TestSequencer.Escape().ControlAPI(sys.L1Network.ChainID())
	cl := sys.L1Network.Escape().L1CLNode(match.FirstL1CL)

	sys.L1Network.WaitForBlock()
	sys.ControlPlane.FakePoSState(cl.ID(), stack.Stop)

	startL1 := sys.L1EL.BlockRefByLabel(eth.Unsafe)
	logger.Info("driving L1 via TestSequencer under time-travel", "start", startL1.Number, "amsterdamOffset", amsterdamOffset)

	require.Eventually(func() bool {
		require.NoError(ts.New(ctx, seqtypes.BuildOpts{Parent: common.Hash{}}), "ts.New")
		require.NoError(ts.Next(ctx), "ts.Next")
		// Keep the virtual clock in step with the L1 block we just produced, so the
		// L2 sequencer and derivation can adopt the advancing L1 origin.
		sys.AdvanceTime(l1BlockTime)

		l1head := sys.L1EL.BlockRefByLabel(eth.Unsafe)
		l2Safe := sys.L2EL.BlockRefByLabel(eth.Safe)
		logger.Info("progress", "l1", l1head.Number, "l2Safe", l2Safe.Number, "l2SafeOrigin", l2Safe.L1Origin.Number)
		return l2Safe.Number > 0 && l2Safe.L1Origin.Number > startL1.Number
	}, 120*time.Second, time.Second)

	l2Safe := sys.L2EL.BlockRefByLabel(eth.Safe)
	require.Greater(l2Safe.L1Origin.Number, startL1.Number,
		"L2 safe head must advance its L1 origin while the L1 is driven under time-travel")
	l1head := sys.L1EL.BlockRefByLabel(eth.Unsafe)
	require.Greater(l1head.Number, startL1.Number+amsterdamOffset,
		"L1 must have crossed the Amsterdam boundary")
	logger.Info("L2 safe advanced under time-travel", "l2Safe", l2Safe.Number, "origin", l2Safe.L1Origin.Number, "l1", l1head.Number)
}

// TestL1Reorg_AtUpgradeActivation drives the L1 past the Amsterdam activation,
// then reorgs the L1 block that the L2 safe head derives from (post-Amsterdam) by
// building a competing chain on its parent. It asserts the L1 reorgs and the L2
// safe chain reorgs and re-converges onto the new L1 — i.e. an L1 reorg across the
// Glamsterdam activation does not wedge derivation. (M4-1)
func TestL1Reorg_AtUpgradeActivation(gt *testing.T) {
	gt.Skip("WIP: injecting a competing block on an older L1 head returns " +
		"forkchoiceUpdated{VALID, PayloadID:nil} — geth's \"ignoring beacon update " +
		"to old head while syncing\" guard (eth.Synced()==false for the subprocess " +
		"vanilla geth driven purely by the engine API). L2-safe advancement under " +
		"time-travel is already proven by TestReorg_L2SafeAdvancesUnderTimeTravel; " +
		"the remaining work is making the fakepos reorg build on an old L1 head.")
	t := devtest.SerialT(gt)
	sys := presets.NewMantleSingleChainMultiNodeWithTestSeq(t)
	require := t.Require()
	logger := t.Logger()
	ctx := t.Ctx()

	ts := sys.TestSequencer.Escape().ControlAPI(sys.L1Network.ChainID())
	cl := sys.L1Network.Escape().L1CLNode(match.FirstL1CL)

	sys.L1Network.WaitForBlock()
	sys.ControlPlane.FakePoSState(cl.ID(), stack.Stop)

	startL1 := sys.L1EL.BlockRefByLabel(eth.Unsafe)

	// driveL1 builds one L1 block on the current head and steps the virtual clock.
	driveL1 := func() {
		require.NoError(ts.New(ctx, seqtypes.BuildOpts{Parent: common.Hash{}}))
		require.NoError(ts.Next(ctx))
		sys.AdvanceTime(l1BlockTime)
	}

	// Advance L1 past Amsterdam and let the L2 safe head derive from a
	// post-Amsterdam L1 origin.
	require.Eventually(func() bool {
		driveL1()
		l2Safe := sys.L2EL.BlockRefByLabel(eth.Safe)
		return l2Safe.Number > 0 && l2Safe.L1Origin.Number > startL1.Number+amsterdamOffset
	}, 120*time.Second, time.Second)

	l2BeforeReorg := sys.L2EL.BlockRefByLabel(eth.Safe)
	l1Height := l2BeforeReorg.L1Origin.Number
	l1BeforeReorg := sys.L1EL.BlockRefByNumber(l1Height)
	oldHead := sys.L1EL.BlockRefByLabel(eth.Unsafe).Number
	logger.Info("reorg target (post-Amsterdam)", "l1Height", l1Height, "l1Hash", l1BeforeReorg.Hash, "l2Safe", l2BeforeReorg.Number)

	// Inject the L1 reorg: build a competing block on the parent of the target L1
	// block (the TestSequencer forces it canonical via forkchoiceUpdated), then
	// extend the competing chain past the old head.
	require.NoError(ts.New(ctx, seqtypes.BuildOpts{Parent: l1BeforeReorg.ParentHash}))
	require.NoError(ts.Next(ctx))
	sys.AdvanceTime(l1BlockTime)
	for sys.L1EL.BlockRefByLabel(eth.Unsafe).Number <= oldHead+2 {
		driveL1()
	}

	l1AfterReorg := sys.L1EL.BlockRefByNumber(l1Height)
	require.NotEqual(l1AfterReorg.Hash, l1BeforeReorg.Hash, "L1 must have reorged at the target height")
	logger.Info("L1 reorged", "height", l1Height, "old", l1BeforeReorg.Hash, "new", l1AfterReorg.Hash)

	// The L2 must detect the L1 reorg and reorg its safe chain onto the new L1.
	// Keep driving so derivation resets and re-converges under time-travel.
	require.Eventually(func() bool {
		driveL1()
		l2After := sys.L2EL.BlockRefByNumber(l2BeforeReorg.Number)
		l2Safe := sys.L2EL.BlockRefByLabel(eth.Safe)
		return l2After.Hash != l2BeforeReorg.Hash && l2Safe.Number >= l2BeforeReorg.Number
	}, 120*time.Second, time.Second)

	l2After := sys.L2EL.BlockRefByNumber(l2BeforeReorg.Number)
	require.NotEqual(l2After.Hash, l2BeforeReorg.Hash, "L2 safe block must have reorged after the L1 reorg")
	l2Safe := sys.L2EL.BlockRefByLabel(eth.Safe)
	require.GreaterOrEqual(l2Safe.Number, l2BeforeReorg.Number, "L2 safe head must re-converge past the reorg point")
	logger.Info("L2 re-converged after L1 reorg across Amsterdam", "l2Safe", l2Safe.Number, "origin", l2Safe.L1Origin.Number)
}
