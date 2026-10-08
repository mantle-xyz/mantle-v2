package finalize

import (
	"math/big"
	"testing"
	"time"

	"github.com/ethereum-optimism/optimism/op-acceptance-tests/mantle-tests/elysium/internal/l1drive"
	"github.com/ethereum-optimism/optimism/op-devstack/devtest"
	"github.com/ethereum-optimism/optimism/op-devstack/presets"
	"github.com/ethereum-optimism/optimism/op-service/eth"
)

// l1FinalizedDistance mirrors the fakepos builder's FinalizedDistance
// (op-devstack/sysgo/test_sequencer.go). It sizes the drive loop -- the chain has to be at least
// this tall before a fork parent at the horizon can even exist -- and nothing else.
//
// It deliberately does NOT locate the fork parent. The builder derives the finalized header from
// the head it held BEFORE the block it is about to build
// (op-test-sequencer/sequencer/backend/work/builders/fakepos/job.go), so the label trails
// head-FinalizedDistance by one further block. Computing the fork parent from this constant
// would bake that off-by-one in as an assumption, and it is not one this test wants to make:
// the case is "fork off whatever the L1 currently calls finalized", so it reads the label.
const l1FinalizedDistance = uint64(20)

// minReorgDepth is a floor on the resulting reorg, not a target. The actual depth falls out of
// where the finalized label sits when the drive loop stops; this only rejects a run where the
// horizon drifted so close to the head that the reorg would be shallow enough to prove nothing.
const minReorgDepth = uint64(12)

// postBoundaryMargin keeps both the reorg target and the fork parent (which sits a full
// finality distance below the head) comfortably post-Amsterdam.
const postBoundaryMargin = uint64(8)

// TestBoundary_L1ReorgAtFinalize drives the deepest L1 reorg the chain will legally perform:
// the competing chain forks off the block immediately above the finalized horizon and discards
// everything from there up. It is the case the deep-reorg suite deliberately excludes -- that
// test asserts its target stays well above the horizon, so nothing else here goes near it.
//
// It forks one block ABOVE the finalized block rather than off the finalized block itself
// because the latter is not something an EL will do: geth answers a forkchoice update whose head
// is at or below the finalized block with VALID and a nil payload id, building nothing
// ("Skipping beacon update to finalized ancestor", eth/catalyst/api.go). Finality is enforced
// there, at the engine API, not merely observed afterwards -- so finalized+1 is the boundary a
// test can actually stand on.
//
// The discriminating property is that FINALITY IS IRREVERSIBLE while everything above it is
// not. A reorg this deep must invalidate the L2's unsafe and safe chains and force a
// re-derivation, yet it must NOT move finality on either layer:
//
//   - the L1 finalized block must survive byte-identically (it is the fork parent's parent, so a
//     changed hash there means the reorg reached past the horizon);
//   - the L2 finalized head must not regress in height, and the exact block that was finalized
//     before the reorg must still be canonical at the same hash afterwards.
//
// A pipeline that treated an L1 reorg as license to rewind its own finalized head -- or that
// derived a different block at the previously-finalized height -- would fail those two checks
// while still passing the ordinary "did the L2 converge" ones, which is why this case exists
// separately from reorg/, reorg/deep and reorg/consecutive.
//
// The L1 is Glamsterdam throughout: the fork parent, the reorg target and the replacement
// block are all asserted to be post-Amsterdam, so the unwind runs entirely on Amsterdam
// headers rather than tailing off into the pre-fork chain.
func TestBoundary_L1ReorgAtFinalize(gt *testing.T) {
	t := devtest.SerialT(gt)
	sys := presets.NewMantleSingleChainMultiNodeWithTestSeq(t)
	require := t.Require()
	logger := t.Logger()

	l1Config := sys.L1Network.Escape().ChainConfig()
	require.NotNil(l1Config.AmsterdamTime, "L1 AmsterdamTime must be configured")

	drive := l1drive.New(t, sys)

	// The L1 must be tall enough that the finalized block -- roughly a full finality distance
	// below the head -- is itself still post-Amsterdam, on top of the usual margin for the L2
	// origin. The +2 covers the extra block the builder's finalized label trails by.
	expectedBoundary := amsterdamOffset / uint64(l1BlockTime/time.Second)
	require.GreaterOrEqual(expectedBoundary, uint64(2), "offset must leave pre-Amsterdam blocks above genesis")
	minL1Head := expectedBoundary + l1FinalizedDistance + postBoundaryMargin + 2

	// Drive the L1 by hand until the chain is tall enough, the L2 has derived well past the
	// Amsterdam boundary, AND the L2 has a non-genesis finalized head. That last condition is
	// what makes the finality assertions below mean anything: against a genesis finalized head
	// they would hold trivially.
	//
	// This loop stays on the test goroutine so L1-production errors surface directly.
	const maxDriveIters = 160
	for i := 0; ; i++ {
		require.Lessf(i, maxDriveIters,
			"after %d L1 blocks the preconditions were still unmet (L1 head %d, need >= %d; "+
				"L2 origin %d, need > %d; L2 finalized %d, need > 0)",
			maxDriveIters, sys.L1EL.BlockRefByLabel(eth.Unsafe).Number, minL1Head,
			sys.L2EL.BlockRefByLabel(eth.Unsafe).L1Origin.Number, expectedBoundary+postBoundaryMargin,
			sys.L2CL.SyncStatus().FinalizedL2.Number)
		drive.InStep()

		l1Head := sys.L1EL.BlockRefByLabel(eth.Unsafe).Number
		l2Origin := sys.L2EL.BlockRefByLabel(eth.Unsafe).L1Origin.Number
		l2Finalized := sys.L2CL.SyncStatus().FinalizedL2.Number
		if l1Head >= minL1Head && l2Origin > expectedBoundary+postBoundaryMargin && l2Finalized > 0 {
			break
		}
	}

	oldHead := sys.L1EL.BlockRefByLabel(eth.Unsafe).Number

	// Fork as deep as the L1 will legally allow: one block ABOVE the finalized horizon.
	//
	// Not ON it. geth refuses a forkchoice update whose head is at or below the finalized block
	// -- "Skipping beacon update to finalized ancestor", eth/catalyst/api.go, which answers VALID
	// with a nil payload id and builds nothing. Finality being irreversible is enforced by the EL
	// at the engine API, so a test cannot ask for a fork parent at the horizon; the deepest one it
	// can ask for is finalized+1, which rewrites everything from finalized+2 up.
	//
	// The horizon is read from the label rather than derived from l1FinalizedDistance: the builder
	// computes the finalized header from the head it held before the block it is building, so the
	// label trails head-FinalizedDistance by one more block and any arithmetic here would encode
	// that off-by-one as a constant.
	l1FinalizedBefore := sys.L1EL.BlockRefByLabel(eth.Finalized)
	l1Parent := sys.L1EL.BlockRefByNumber(l1FinalizedBefore.Number + 1)
	l1Height := l1Parent.Number + 1
	l1Before := sys.L1EL.BlockRefByNumber(l1Height)

	// l1Parent was fetched BY the number l1FinalizedBefore.Number+1, so asserting its Number equals
	// that same expression only restates the fetch and can never fail. Check the ancestry instead:
	// the fork parent must really be the finalized block's direct child, which is what makes this
	// the deepest legal reorg rather than merely a block sitting at the right height.
	require.Equalf(l1FinalizedBefore.Hash, l1Parent.ParentHash,
		"the fork parent (#%d) must be the direct child of the finalized horizon (#%d): "+
			"deeper is refused by the EL, shallower stops being the boundary case",
		l1Parent.Number, l1FinalizedBefore.Number)

	require.Greaterf(oldHead, l1Height,
		"the finalized block (#%d) must sit below the head (#%d) for a reorg above it to exist",
		l1Parent.Number, oldHead)
	require.GreaterOrEqualf(oldHead-l1Parent.Number, minReorgDepth,
		"the reorg would only be %d blocks deep (head #%d, finalized #%d); a shallow unwind does "+
			"not exercise what this case is for", oldHead-l1Parent.Number, oldHead, l1Parent.Number)

	// Both sides of the boundary must be post-Amsterdam, so the unwind is entirely Glamsterdam.
	require.True(l1Config.IsAmsterdam(new(big.Int).SetUint64(l1Height), l1Before.Time),
		"the reorg target must be a post-Amsterdam L1 block")
	require.True(l1Config.IsAmsterdam(new(big.Int).SetUint64(l1Height-1), l1Parent.Time),
		"the fork parent (the finalized block) must also be post-Amsterdam")
	require.Less(oldHead-l1Height, uint64(32), "reorg must stay within geth's max reorg depth")

	// The L2 must already have consumed the target and have a real finalized head to protect.
	l2BeforeReorg := sys.L2EL.BlockRefByLabel(eth.Unsafe)
	require.Greater(l2BeforeReorg.L1Origin.Number, l1Height,
		"L2 must have derived past the reorg target before the reorg")
	l2FinalizedBefore := sys.L2CL.SyncStatus().FinalizedL2
	require.NotZero(l2FinalizedBefore.Number, "L2 must have a non-genesis finalized head before the reorg")
	l2SafeBefore := sys.L2EL.BlockRefByLabel(eth.Safe)

	logger.Info("reorging at the finalized boundary",
		"l1Head", oldHead, "target", l1Height, "forkParent", l1Parent.Number,
		"l1Finalized", l1FinalizedBefore.Number, "l2Finalized", l2FinalizedBefore.Number,
		"l2Safe", l2SafeBefore.Number, "l2Unsafe", l2BeforeReorg.Number)

	// Build the competing chain directly off the finalized block and extend it past the old head.
	drive.Fork(l1Parent.Hash)
	require.Eventually(func() bool {
		sys.AdvanceTime(2 * time.Second)
		return sys.L1EL.BlockRefByLabel(eth.Unsafe).Number >= l1Height
	}, 30*time.Second, 200*time.Millisecond, "the competing chain must reach the reorg height")
	for i := uint64(0); sys.L1EL.BlockRefByLabel(eth.Unsafe).Number <= oldHead+1; i++ {
		require.Lessf(i, l1FinalizedDistance+24,
			"the competing chain never overtook the old head %d (L1 head stuck at %d)",
			oldHead, sys.L1EL.BlockRefByLabel(eth.Unsafe).Number)
		drive.Produce()
		require.NoError(drive.Err(), "L1 block production failed while extending the competing chain")
	}

	// The L1 really did reorg at the boundary, and the replacement is still Glamsterdam.
	l1After := sys.L1EL.BlockRefByNumber(l1Height)
	require.NotEqual(l1After.Hash, l1Before.Hash, "L1 must have reorged at the finalize-boundary height")
	require.True(l1Config.IsAmsterdam(new(big.Int).SetUint64(l1Height), l1After.Time),
		"the reorged block must still be post-Amsterdam")

	// ...but the finalized block below it must be untouched. If this fails the reorg reached
	// past the horizon, and every finality assertion below is meaningless.
	l1FinalizedNow := sys.L1EL.BlockRefByNumber(l1FinalizedBefore.Number)
	require.Equal(l1FinalizedBefore.Hash, l1FinalizedNow.Hash,
		"the L1 finalized block must survive a reorg that forks directly off it")
	logger.Info("L1 reorged at the finalized boundary",
		"height", l1Height, "old", l1Before.Hash, "new", l1After.Hash,
		"finalizedIntact", l1FinalizedBefore.Number)

	// The L2 must unwind the invalidated chain and keep producing.
	//
	// Check the head BEFORE reading the recorded height. A reorg this deep unwinds the L2 below
	// that height and then rebuilds, and dsl's BlockRefByNumber fails the test outright when the
	// block is absent rather than reporting absence -- so reading it during the gap turns a
	// mid-recovery poll into a failure. The gap is real but short: a fast machine rebuilds
	// between two polls and never lands in it, a slow one does. Waiting for the head first is
	// also exactly the condition that was already being asserted, so nothing is given up.
	drive.While(func() bool {
		if sys.L2EL.BlockRefByLabel(eth.Unsafe).Number <= l2BeforeReorg.Number {
			return false
		}
		return sys.L2EL.BlockRefByNumber(l2BeforeReorg.Number).Hash != l2BeforeReorg.Hash
	}, 300*time.Second, "L2 must reorg the invalidated chain and keep advancing")

	// FINALITY IS IRREVERSIBLE. The L2 finalized head may not regress, and the block that was
	// finalized before the reorg must still be canonical at the same hash. This is the
	// load-bearing pair: the convergence checks below would pass even if finality had rewound.
	l2FinalizedAfter := sys.L2CL.SyncStatus().FinalizedL2
	require.GreaterOrEqualf(l2FinalizedAfter.Number, l2FinalizedBefore.Number,
		"the L2 finalized head must never regress across an L1 reorg (was %d, now %d)",
		l2FinalizedBefore.Number, l2FinalizedAfter.Number)
	require.Equalf(l2FinalizedBefore.Hash, sys.L2EL.BlockRefByNumber(l2FinalizedBefore.Number).Hash,
		"the L2 block finalized before the reorg (#%d) must still be canonical at the same hash",
		l2FinalizedBefore.Number)

	// The sequencer must re-derive past the reorged height from the NEW canonical L1.
	drive.While(func() bool {
		return sys.L2EL.BlockRefByLabel(eth.Safe).L1Origin.Number > l1Height
	}, 300*time.Second, "sequencer safe head must derive past the reorged block")
	seqSafe := sys.L2EL.BlockRefByLabel(eth.Safe)
	canonOrigin := sys.L1EL.BlockRefByNumber(seqSafe.L1Origin.Number)
	require.Equal(seqSafe.L1Origin.Hash, canonOrigin.Hash,
		"sequencer safe head must derive from the new canonical L1, not a stale origin")

	// The verifier must independently reach the same post-reorg safe block.
	drive.While(func() bool {
		vSafe := sys.L2ELB.BlockRefByLabel(eth.Safe)
		return vSafe.Number >= seqSafe.Number && sys.L2ELB.BlockRefByNumber(seqSafe.Number).Hash == seqSafe.Hash
	}, 300*time.Second, "verifier must re-derive the sequencer's post-reorg safe block")
	require.Equalf(seqSafe.Hash, sys.L2ELB.BlockRefByNumber(seqSafe.Number).Hash,
		"sequencer and verifier must converge on the post-reorg safe block at height %d", seqSafe.Number)

	// Finality must also still be monotonic once the dust has settled, and the L2 must be live.
	finalUnsafe := sys.L2EL.BlockRefByLabel(eth.Unsafe)
	drive.While(func() bool {
		return sys.L2EL.BlockRefByLabel(eth.Unsafe).Number > finalUnsafe.Number+2
	}, 120*time.Second, "L2 must keep advancing after the finalize-boundary reorg (no wedge)")
	require.GreaterOrEqualf(sys.L2CL.SyncStatus().FinalizedL2.Number, l2FinalizedBefore.Number,
		"the L2 finalized head must still not have regressed after the chain resumed (was %d)",
		l2FinalizedBefore.Number)

	logger.Info("L2 survived an L1 reorg at the finalized boundary",
		"l1Target", l1Height, "l2FinalizedBefore", l2FinalizedBefore.Number,
		"l2FinalizedAfter", sys.L2CL.SyncStatus().FinalizedL2.Number,
		"seqSafe", seqSafe.Number)
}
