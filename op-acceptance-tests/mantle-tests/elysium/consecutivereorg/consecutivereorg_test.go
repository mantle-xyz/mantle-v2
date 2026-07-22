package consecutivereorg

import (
	"fmt"
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

const l1BlockTime = 6 * time.Second

// postBoundaryMargin is how many L2-origin epochs past the Amsterdam activation we drive before
// the first reorg, so every reorg target (and its fork parent) is comfortably post-Amsterdam.
const postBoundaryMargin = uint64(8)

// reorgDepth is how many blocks below the current L1 head each reorg target sits: shallow enough
// to stay above the finalized horizon and inside geth's max reorg depth, deep enough that the L2
// has already derived past it (so the reorg actually invalidates an L2 epoch).
const reorgDepth = uint64(4)

// numReorgs is how many reorgs are injected back-to-back. Two consecutive full re-derivations are
// enough to prove the pipeline resets cleanly between reorgs and does not accumulate stale state.
const numReorgs = 2

// TestL1Reorg_Consecutive_PostUpgrade injects SEVERAL post-Amsterdam L1 reorgs back-to-back and
// asserts the L2 survives every one: after each reorg the sequencer re-derives onto the new
// canonical L1 without wedging, the independent verifier converges onto the same re-derived safe
// block, and the next reorg starts from that converged state. After the last reorg the L2 must
// still be advancing.
//
// HONEST SCOPE NOTE: the reorg re-derivation core exercised here is L1-fork-independent. The
// sequencer/verifier re-derivation and convergence behave identically under any L1 fork, and no
// assertion in this test is sensitive to whether the L1 runs Glamsterdam — the IsAmsterdam checks
// only confirm the reorg targets land in the post-Amsterdam environment (guarding the setup), they
// do not discriminate a Glamsterdam-specific regression. The single-reorg-across-the-boundary case
// is already covered by the reorg/ package, whose TestL1Reorg_AtUpgradeActivation reorgs the
// Amsterdam ACTIVATION block itself (forking at its pre-Amsterdam parent) and is the stronger case
// for L1-fork sensitivity. This test's UNIQUE value is orthogonal to the L1 fork: surviving TWO
// consecutive reorgs with no stale pipeline state carried between them — a later reorg failing to
// converge, or the L2 wedging after two in a row, fails this test even though one reorg alone
// passes.
//
// This test takes exclusive control of L1 production, so it is the only test in this package.
func TestL1Reorg_Consecutive_PostUpgrade(gt *testing.T) {
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

	// Take manual control of L1 production: with auto-FakePoS stopped, advancing the (time-travel)
	// clock moves only the real-time-bound L2 sequencer, not the L1, so the L2 origin can catch up
	// between manually-produced L1 blocks and each reorg target stays above the finalized horizon.
	sys.ControlPlane.FakePoSState(cl.ID(), stack.Stop)

	// driveErr records the first L1-production error that happens INSIDE a polled condition.
	//
	// testify runs an Eventually condition in its own goroutine (assert.Eventually:
	// `go func() { ch <- condition() }()`). require.NoError there calls FailNow ->
	// runtime.Goexit, which kills only that goroutine: nothing is ever sent on the channel,
	// so Eventually just keeps ticking and finally reports "Condition never satisfied"
	// minutes later, with the real error lost. Errors are therefore recorded here and
	// asserted on the test goroutine.
	var driveErr error

	// driveL1InStep produces one L1 block, then advances the clock until the L2 unsafe origin
	// catches up to the new L1 head (strict in-step, used only before the first reorg).
	// Only call this from the test goroutine.
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

	// produceL1 makes one L1 block WITHOUT requiring the L2 to keep pace — during a reorg the L2
	// origin legitimately falls behind, so the strict in-step catch-up cannot hold.
	// It is called from inside polled conditions, so it records into driveErr rather than
	// asserting; every caller checks driveErr on the test goroutine.
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

	// driveWhile advances the clock and keeps a little L1 ahead of the L2 origin (so there is
	// always L1 to consume) until cond holds — used while derivation re-runs after a reorg.
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

	// Drive WELL PAST the Amsterdam activation so every reorg target is post-Amsterdam. Amsterdam
	// activates amsterdamOffset SECONDS after L1 genesis; with 6s L1 blocks that is L1 block
	// expectedBoundary.
	expectedBoundary := amsterdamOffset / uint64(l1BlockTime/time.Second)
	require.GreaterOrEqual(expectedBoundary, uint64(2), "offset must leave pre-Amsterdam blocks above genesis")

	// A bounded loop rather than Eventually: each iteration must run on the test goroutine so
	// driveL1InStep's require.NoError reports the real L1-production error instead of being
	// swallowed into a timeout (see driveErr).
	const maxDriveIters = 96
	for i := 0; ; i++ {
		require.Lessf(i, maxDriveIters,
			"L2 origin never got %d epochs past the Amsterdam boundary (block %d) after %d L1 blocks",
			postBoundaryMargin, expectedBoundary, maxDriveIters)
		driveL1InStep()
		if sys.L2EL.BlockRefByLabel(eth.Unsafe).L1Origin.Number > expectedBoundary+postBoundaryMargin {
			break
		}
	}
	logger.Info("driven past Amsterdam; starting consecutive reorgs",
		"l1", sys.L1EL.BlockRefByLabel(eth.Unsafe).Number,
		"l2Origin", sys.L2EL.BlockRefByLabel(eth.Unsafe).L1Origin.Number)

	// reorgOnce injects one shallow post-Amsterdam L1 reorg and drives BOTH nodes to re-converge:
	// the sequencer re-derives onto the new canonical L1 without wedging, and the independent
	// verifier reaches the same post-reorg safe block (height AND hash).
	reorgOnce := func(round int) {
		oldHead := sys.L1EL.BlockRefByLabel(eth.Unsafe).Number
		require.Greaterf(oldHead, reorgDepth+1, "round %d: need enough L1 history for a reorg target", round)
		l1Height := oldHead - reorgDepth

		l1Before := sys.L1EL.BlockRefByNumber(l1Height)
		l1Parent := sys.L1EL.BlockRefByNumber(l1Height - 1)
		require.Truef(l1Config.IsAmsterdam(new(big.Int).SetUint64(l1Height), l1Before.Time),
			"round %d: reorg target must be a post-Amsterdam L1 block", round)
		require.Truef(l1Config.IsAmsterdam(new(big.Int).SetUint64(l1Height-1), l1Parent.Time),
			"round %d: the fork parent must also be post-Amsterdam", round)
		require.Lessf(oldHead-l1Height, uint64(20), "round %d: reorg target must be above the finalized horizon", round)

		l2BeforeReorg := sys.L2EL.BlockRefByLabel(eth.Unsafe)
		require.Greaterf(l2BeforeReorg.L1Origin.Number, l1Height,
			"round %d: L2 must have derived past the reorg target before the reorg", round)

		// Inject the reorg: build a competing block on the target's post-Amsterdam parent (the
		// TestSequencer forces it canonical), then extend past the old head so it wins.
		require.NoError(ts.New(ctx, seqtypes.BuildOpts{Parent: l1Before.ParentHash}))
		require.NoError(ts.Next(ctx))
		require.Eventually(func() bool {
			sys.AdvanceTime(2 * time.Second)
			return sys.L1EL.BlockRefByLabel(eth.Unsafe).Number >= l1Height
		}, 30*time.Second, 200*time.Millisecond)
		// Bounded: without a limit a competing chain that never becomes canonical would spin
		// here until the whole test times out, instead of failing with a usable message.
		for i := 0; sys.L1EL.BlockRefByLabel(eth.Unsafe).Number <= oldHead+1; i++ {
			require.Lessf(uint64(i), reorgDepth+16,
				"round %d: competing chain never overtook the old head %d (L1 head stuck at %d)",
				round, oldHead, sys.L1EL.BlockRefByLabel(eth.Unsafe).Number)
			produceL1()
			require.NoErrorf(driveErr, "round %d: L1 block production failed while extending the competing chain", round)
		}

		l1After := sys.L1EL.BlockRefByNumber(l1Height)
		require.NotEqualf(l1After.Hash, l1Before.Hash, "round %d: L1 must have reorged at the target height", round)
		require.Truef(l1Config.IsAmsterdam(new(big.Int).SetUint64(l1Height), l1After.Time),
			"round %d: the reorged target must still be post-Amsterdam", round)
		logger.Info("L1 reorged", "round", round, "height", l1Height, "old", l1Before.Hash, "new", l1After.Hash)

		// The L2 must reorg its own chain (the block that derived from the old L1 must change) and
		// keep advancing — no wedge.
		driveWhile(func() bool {
			l2At := sys.L2EL.BlockRefByNumber(l2BeforeReorg.Number)
			l2Head := sys.L2EL.BlockRefByLabel(eth.Unsafe)
			return l2At.Hash != l2BeforeReorg.Hash && l2Head.Number > l2BeforeReorg.Number
		}, 240*time.Second, fmt.Sprintf("round %d: L2 must reorg and keep advancing", round))

		// The sequencer's SAFE head must derive past the reorged L1 block onto the NEW canonical L1
		// (hash-matched) — not a stale pre-reorg origin.
		driveWhile(func() bool {
			return sys.L2EL.BlockRefByLabel(eth.Safe).L1Origin.Number > l1Height
		}, 240*time.Second, fmt.Sprintf("round %d: sequencer safe head must derive past the reorged block", round))
		seqSafe := sys.L2EL.BlockRefByLabel(eth.Safe)
		canonOrigin := sys.L1EL.BlockRefByNumber(seqSafe.L1Origin.Number)
		require.Equalf(seqSafe.L1Origin.Hash, canonOrigin.Hash,
			"round %d: sequencer safe head must derive from the new canonical L1, not a stale origin", round)

		// The INDEPENDENT verifier must re-derive the same post-reorg safe block — height AND hash.
		driveWhile(func() bool {
			vSafe := sys.L2ELB.BlockRefByLabel(eth.Safe)
			return vSafe.Number >= seqSafe.Number && sys.L2ELB.BlockRefByNumber(seqSafe.Number).Hash == seqSafe.Hash
		}, 300*time.Second, fmt.Sprintf("round %d: verifier must re-derive the sequencer's post-reorg safe block", round))
		require.Equalf(seqSafe.Hash, sys.L2ELB.BlockRefByNumber(seqSafe.Number).Hash,
			"round %d: sequencer and verifier must converge on the post-reorg safe block at height %d", round, seqSafe.Number)
		logger.Info("reorg round converged", "round", round, "l1Height", l1Height,
			"seqSafe", seqSafe.Number, "seqSafeHash", seqSafe.Hash)
	}

	for round := 1; round <= numReorgs; round++ {
		reorgOnce(round)
	}

	// After the consecutive reorgs the L2 must still be live: its unsafe head keeps advancing.
	finalBefore := sys.L2EL.BlockRefByLabel(eth.Unsafe)
	driveWhile(func() bool {
		return sys.L2EL.BlockRefByLabel(eth.Unsafe).Number > finalBefore.Number+2
	}, 60*time.Second, "L2 must keep advancing after the consecutive reorgs (no wedge)")
	logger.Info("L2 survived consecutive post-Amsterdam L1 reorgs and stayed live",
		"reorgs", numReorgs, "finalUnsafe", sys.L2EL.BlockRefByLabel(eth.Unsafe).Number)
}
