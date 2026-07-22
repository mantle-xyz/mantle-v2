package deep

import (
	"math/big"
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

// reorgDepth discards enough L1 history to force a multi-epoch unwind while
// staying above fakepos finalization and inside geth's max reorg depth.
const reorgDepth = uint64(12)

// postBoundaryMargin keeps head-reorgDepth and its parent post-Amsterdam.
const postBoundaryMargin = uint64(16)

// minInvalidatedBlocks pins the "10+ discarded blocks" requirement.
const minInvalidatedBlocks = 10

// TestL1Reorg_DeepReorg_PostUpgrade invalidates a post-Amsterdam L1 range and
// requires every affected L2 epoch to re-derive. The activation-boundary case is
// covered by reorg/; this case focuses on depth.
func TestL1Reorg_DeepReorg_PostUpgrade(gt *testing.T) {
	t := devtest.SerialT(gt)
	sys := presets.NewMantleSingleChainMultiNodeWithTestSeq(t)
	require := t.Require()
	logger := t.Logger()
	ctx := t.Ctx()

	l1Config := sys.L1Network.Escape().ChainConfig()
	require.NotNil(l1Config.AmsterdamTime, "L1 AmsterdamTime must be configured")

	ts := sys.TestSequencer.Escape().ControlAPI(sys.L1Network.ChainID())
	cl := sys.L1Network.Escape().L1CLNode(match.FirstL1CL)

	sys.L1Network.WaitForBlock()

	// Manual L1 production lets the L2 origin catch up between produced blocks.
	sys.ControlPlane.FakePoSState(cl.ID(), stack.Stop)

	// require.NoError inside Eventually would fail only the polling goroutine.
	// Record production errors here and assert them from the test goroutine.
	var driveErr error

	// driveL1InStep produces one L1 block, then advances the clock until the L2 unsafe origin
	// catches up. Only call this from the test goroutine.
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

	// produceL1 mines one block without requiring the L2 origin to keep pace.
	produceL1 := func() {
		if driveErr != nil {
			return
		}
		if err := ts.New(ctx, seqtypes.BuildOpts{Parent: common.Hash{}}); err != nil {
			driveErr = err
			return
		}
		if err := ts.Next(ctx); err != nil {
			driveErr = err
		}
	}

	driveWhile := func(cond func() bool, timeout time.Duration, msg string) {
		require.Eventually(func() bool {
			if driveErr != nil {
				return true // bail out of polling; the error is asserted below
			}
			sys.AdvanceTime(2 * time.Second)
			if sys.L2EL.BlockRefByLabel(eth.Unsafe).L1Origin.Number+2 >= sys.L1EL.BlockRefByLabel(eth.Unsafe).Number {
				produceL1()
			}
			return cond()
		}, timeout, 300*time.Millisecond, msg)
		require.NoErrorf(driveErr, "L1 block production failed while waiting: %s", msg)
	}

	// findEpochOpener returns the first L2 block of the epoch anchored at l1Num.
	findEpochOpener := func(fromL2 uint64, l1Num uint64) (eth.L2BlockRef, bool) {
		var opener eth.L2BlockRef
		found := false
		for n := fromL2; n > 0; n-- {
			b := sys.L2EL.BlockRefByNumber(n)
			if b.L1Origin.Number == l1Num {
				opener = b
				found = true
			} else if found && b.L1Origin.Number < l1Num {
				break
			}
		}
		return opener, found
	}

	// Drive far enough past Amsterdam that head-reorgDepth remains post-Amsterdam.
	expectedBoundary := amsterdamOffset / uint64(l1BlockTime/time.Second)
	require.Greater(postBoundaryMargin, reorgDepth,
		"the margin driven past Amsterdam must exceed the reorg depth, or the target lands pre-Amsterdam")

	// Keep this loop on the test goroutine so L1-production errors surface directly.
	const maxDriveIters = 128
	for i := 0; ; i++ {
		require.Lessf(i, maxDriveIters,
			"L2 origin never got %d epochs past the Amsterdam boundary (block %d) after %d L1 blocks",
			postBoundaryMargin, expectedBoundary, maxDriveIters)
		driveL1InStep()
		l1head := sys.L1EL.BlockRefByLabel(eth.Unsafe).Number
		l2origin := sys.L2EL.BlockRefByLabel(eth.Unsafe).L1Origin.Number
		logger.Info("in-step progress", "l1", l1head, "l2Origin", l2origin, "lag", l1head-l2origin)
		if l2origin > expectedBoundary+postBoundaryMargin {
			break
		}
	}

	oldHead := sys.L1EL.BlockRefByLabel(eth.Unsafe).Number
	require.Greater(oldHead, reorgDepth+1, "need enough L1 history for a deep reorg target")
	l1Height := oldHead - reorgDepth

	l1Before := sys.L1EL.BlockRefByNumber(l1Height)
	l1Parent := sys.L1EL.BlockRefByNumber(l1Height - 1)
	require.True(l1Config.IsAmsterdam(new(big.Int).SetUint64(l1Height), l1Before.Time),
		"deep reorg target must be a post-Amsterdam L1 block")
	require.True(l1Config.IsAmsterdam(new(big.Int).SetUint64(l1Height-1), l1Parent.Time),
		"the fork parent must also be post-Amsterdam — the discarded range stays past the boundary")
	require.GreaterOrEqual(oldHead-l1Height, uint64(10),
		"this case must discard at least 10 L1 blocks to be a DEEP reorg (design doc §4 row 4)")
	require.Less(oldHead-l1Height, uint64(20), "reorg target must stay above the finalized horizon")
	require.Less(oldHead-l1Height, uint64(32), "reorg must stay within geth's max reorg depth")

	l2BeforeReorg := sys.L2EL.BlockRefByLabel(eth.Unsafe)
	require.Greater(l2BeforeReorg.L1Origin.Number, l1Height,
		"L2 must have derived past the deep-reorg target before the reorg")

	// Snapshot the whole discarded range and the L2 epoch opener for each L1 block.
	type epochSnapshot struct {
		l1Hash     common.Hash
		openerL2   uint64
		openerHash common.Hash
		hasOpener  bool
	}
	before := make(map[uint64]epochSnapshot, reorgDepth+1)
	for n := l1Height; n <= oldHead; n++ {
		snap := epochSnapshot{l1Hash: sys.L1EL.BlockRefByNumber(n).Hash}
		if opener, ok := findEpochOpener(l2BeforeReorg.Number, n); ok {
			snap.openerL2, snap.openerHash, snap.hasOpener = opener.Number, opener.Hash, true
		}
		before[n] = snap
	}
	logger.Info("deep reorg target selected",
		"l1Height", l1Height, "l1Head", oldHead, "depth", oldHead-l1Height, "l2Unsafe", l2BeforeReorg.Number)

	// Build a competing chain from the target's parent and extend it past the old head.
	require.NoError(ts.New(ctx, seqtypes.BuildOpts{Parent: l1Before.ParentHash}))
	require.NoError(ts.Next(ctx))
	require.Eventually(func() bool {
		sys.AdvanceTime(2 * time.Second)
		return sys.L1EL.BlockRefByLabel(eth.Unsafe).Number >= l1Height
	}, 30*time.Second, 200*time.Millisecond)
	// Bound the loop so a stuck competing chain fails with context.
	for i := 0; sys.L1EL.BlockRefByLabel(eth.Unsafe).Number <= oldHead+1; i++ {
		require.Lessf(uint64(i), reorgDepth+24,
			"competing chain never overtook the old head %d (L1 head stuck at %d)",
			oldHead, sys.L1EL.BlockRefByLabel(eth.Unsafe).Number)
		produceL1()
		require.NoError(driveErr, "L1 block production failed while extending the competing chain")
	}

	// The whole discarded range must change, not just the fork point.
	changed := uint64(0)
	for n := l1Height; n <= oldHead; n++ {
		if sys.L1EL.BlockRefByNumber(n).Hash != before[n].l1Hash {
			changed++
		}
	}
	require.GreaterOrEqualf(changed, uint64(minInvalidatedBlocks),
		"a deep reorg must invalidate at least %d L1 blocks, but only %d of the %d discarded heights changed hash",
		minInvalidatedBlocks, changed, oldHead-l1Height+1)

	l1After := sys.L1EL.BlockRefByNumber(l1Height)
	require.NotEqual(l1After.Hash, l1Before.Hash, "L1 must have reorged at the deep-reorg target height")
	require.True(l1Config.IsAmsterdam(new(big.Int).SetUint64(l1Height), l1After.Time),
		"the reorged target must still be post-Amsterdam")
	logger.Info("L1 deep-reorged", "depth", oldHead-l1Height, "invalidated", changed,
		"height", l1Height, "old", l1Before.Hash, "new", l1After.Hash)

	// The L2 must unwind and keep advancing (no wedge).
	require.Eventually(func() bool {
		if driveErr != nil {
			return true // bail out of polling; the error is asserted below
		}
		sys.AdvanceTime(2 * time.Second)
		if sys.L2EL.BlockRefByLabel(eth.Unsafe).L1Origin.Number+2 >= sys.L1EL.BlockRefByLabel(eth.Unsafe).Number {
			produceL1()
		}
		l2At := sys.L2EL.BlockRefByNumber(l2BeforeReorg.Number)
		l2Head := sys.L2EL.BlockRefByLabel(eth.Unsafe)
		return l2At.Hash != l2BeforeReorg.Hash && l2Head.Number > l2BeforeReorg.Number
	}, 300*time.Second, 300*time.Millisecond)
	require.NoError(driveErr, "L1 block production failed while waiting for the L2 to unwind")

	l2At := sys.L2EL.BlockRefByNumber(l2BeforeReorg.Number)
	require.NotEqual(l2At.Hash, l2BeforeReorg.Hash,
		"L2 must have reorged the block that derived from the discarded L1 range")

	// The re-derived L2 block must track the new canonical L1.
	canonOrigin := sys.L1EL.BlockRefByNumber(l2At.L1Origin.Number)
	require.Equal(l2At.L1Origin.Hash, canonOrigin.Hash,
		"reorged L2 block must derive from the NEW canonical L1 (not a stale pre-reorg origin)")

	// Drive the sequencer safe head past the discarded range.
	driveWhile(func() bool {
		return sys.L2EL.BlockRefByLabel(eth.Safe).L1Origin.Number > oldHead
	}, 300*time.Second, "sequencer safe head must derive past the entire discarded L1 range")
	seqSafe := sys.L2EL.BlockRefByLabel(eth.Safe)

	// Every invalidated L1 height that opened an L2 epoch must reopen on the new chain.
	l2Head := sys.L2EL.BlockRefByLabel(eth.Unsafe).Number
	hadOpener, reDerived := uint64(0), uint64(0)
	for n := l1Height; n <= oldHead; n++ {
		snap := before[n]
		if !snap.hasOpener {
			continue
		}
		hadOpener++
		reOpener, ok := findEpochOpener(l2Head, n)
		require.Truef(ok, "L1 block %d must re-open an L2 epoch after the deep reorg", n)
		require.Equalf(sys.L1EL.BlockRefByNumber(n).Hash, reOpener.L1Origin.Hash,
			"the epoch anchored at L1 %d must re-derive onto the NEW canonical block", n)
		if reOpener.L1Origin.Hash != snap.l1Hash {
			reDerived++
		}
	}

	// Every pre-reorg epoch in the discarded range must now use a different L1 origin.
	require.Equalf(hadOpener, reDerived,
		"all %d L2 epochs anchored in the discarded range must re-derive onto new L1 origins, only %d did",
		hadOpener, reDerived)

	// Allow a small lag: the last discarded L1 blocks may not have opened L2 epochs yet.
	const maxOriginLag = uint64(4)
	require.GreaterOrEqualf(hadOpener, reorgDepth+1-maxOriginLag,
		"a deep reorg must invalidate at least %d L2 epochs (depth %d minus up to %d blocks of L2 origin lag), but only %d of the discarded L1 blocks had opened one",
		reorgDepth+1-maxOriginLag, reorgDepth, maxOriginLag, hadOpener)
	logger.Info("L2 re-derived the discarded range", "epochs", reDerived, "safeHead", seqSafe.Number)

	// Verifier convergence: same post-reorg safe block by height and hash.
	driveWhile(func() bool {
		vSafe := sys.L2ELB.BlockRefByLabel(eth.Safe)
		return vSafe.Number >= seqSafe.Number && sys.L2ELB.BlockRefByNumber(seqSafe.Number).Hash == seqSafe.Hash
	}, 360*time.Second, "verifier must independently re-derive the sequencer's post-deep-reorg safe block")
	require.Equal(seqSafe.Hash, sys.L2ELB.BlockRefByNumber(seqSafe.Number).Hash,
		"sequencer and verifier must derive an identical safe L2 block at height %d after the deep reorg", seqSafe.Number)
	logger.Info("independent verifier re-derived the sequencer's post-deep-reorg safe chain",
		"safeHeight", seqSafe.Number, "safeHash", seqSafe.Hash)
}
