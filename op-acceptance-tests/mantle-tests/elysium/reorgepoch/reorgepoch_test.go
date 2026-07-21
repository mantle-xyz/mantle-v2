package reorgepoch

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

const l1BlockTime = 6 * time.Second

// postBoundaryMargin is how many L2-origin epochs past the Amsterdam activation we drive
// before choosing a reorg target, so that the target AND its parent (the fork point) are
// both comfortably post-Amsterdam. Unlike the reorg/ package — which forks the ACTIVATION
// block from its PRE-Amsterdam parent — the entire neighbourhood of this reorg is
// post-Glamsterdam.
const postBoundaryMargin = uint64(8)

// reorgDepth is how many blocks below the L1 head the reorg target sits. It must be:
//   - shallow enough to stay above the finalized horizon and inside geth's max reorg depth,
//   - deep enough that the L2 has already derived past it (so the reorg actually invalidates
//     the L2 epoch this L1 block opened, forcing a re-derivation).
const reorgDepth = uint64(4)

// TestL1Reorg_AtEpochBoundary_PostUpgrade drives the L1 WELL PAST the Amsterdam activation
// while keeping the L2 in step, then reorgs a recent POST-Amsterdam L1 block that opens an
// L2 sequencing epoch. In OP-Stack derivation each L1 block opens a new L2 epoch (the first
// L2 block whose L1Origin advances to that L1 block), so any post-Amsterdam L1 block above
// the finalized horizon is a valid epoch-boundary reorg target.
//
// SCOPE / HONESTY: the behavior actually verified here — L1-reorg detection, epoch
// re-derivation onto the new canonical L1, sequencer non-wedge, and verifier convergence — is
// L1-fork-INDEPENDENT: it would hold identically whether the L1 runs Glamsterdam or an earlier
// fork. The Amsterdam L1 is the ENVIRONMENT, not the discriminator. The IsAmsterdam(...) guards
// below only confirm the reorg lands in post-Amsterdam territory; none of them exercise a
// Glamsterdam-specific derivation code path, so this test does not "defend against" a
// Glamsterdam L1 in the way its assertions read. The sibling reorg/ package is the stronger,
// more Glamsterdam-specific case: its divergence STRADDLES the activation block (a pre-Amsterdam
// parent forking to a post-Amsterdam child), so it re-derives the fork transition itself. This
// package's complementary, unique angle is a reorg that lands DEEP inside post-Amsterdam
// territory — target AND fork point both well past the boundary — plus INDEPENDENT VERIFIER
// convergence, which reorg/ does not assert.
//
// The reorg is injected by building a competing chain on the target's POST-Amsterdam parent,
// so both the old and the new chain around the fork point are entirely post-Amsterdam. It
// asserts the L1 reorgs at the target height (old and new target block are both Amsterdam),
// the epoch anchored at that L1 block re-derives onto the NEW canonical L1, the sequencer L2 —
// which had already derived past the boundary — reorgs its own chain, re-converges onto the
// new L1, and keeps advancing (no wedge), AND the independent verifier re-derives the same
// post-reorg safe chain from L1 (same safe block, height and hash).
//
// Discriminating (all L1-fork-independent): BOTH nodes must re-derive an L1 epoch across the
// reorg — the sequencer without wedging and without keeping a stale pre-reorg L1 origin, and
// the verifier independently converging onto the sequencer's re-derived safe block. A sequencer
// that forked off alone or kept a stale L1 origin, or a verifier that never re-derived, would
// fail these checks.
//
// This test takes exclusive control of L1 production, so it is the only test in this
// package: two L1-driving tests cannot share one devstack system.
func TestL1Reorg_AtEpochBoundary_PostUpgrade(gt *testing.T) {
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

	// Take manual control of L1 production. With the auto-FakePoS stopped, advancing the
	// (time-travel) clock moves only the real-time-bound L2 sequencer, not the L1 — so we
	// can let the L2 origin catch up between manually-produced L1 blocks, keeping the reorg
	// target above the finalized horizon and within max reorg depth.
	sys.ControlPlane.FakePoSState(cl.ID(), stack.Stop)

	// driveL1InStep produces one L1 block, then advances the clock until the L2 unsafe
	// origin catches up to the new L1 head.
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

	// findEpochOpener scans the L2 chain (downward from fromL2) for the FIRST (lowest-numbered)
	// L2 block whose L1 origin is l1Num — the block that OPENS the epoch anchored at that L1
	// block. Returns false if no L2 block references l1Num yet.
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

	// Drive WELL PAST the Amsterdam activation so every L1 block around the reorg target — the
	// target and its parent (the fork point) — is post-Amsterdam. Amsterdam activates
	// amsterdamOffset SECONDS after L1 genesis; with 6s L1 blocks that is L1 block
	// expectedBoundary.
	expectedBoundary := amsterdamOffset / uint64(l1BlockTime/time.Second)
	require.GreaterOrEqual(expectedBoundary, uint64(2), "offset must leave pre-Amsterdam blocks above genesis")

	require.Eventually(func() bool {
		driveL1InStep()
		l1head := sys.L1EL.BlockRefByLabel(eth.Unsafe).Number
		l2origin := sys.L2EL.BlockRefByLabel(eth.Unsafe).L1Origin.Number
		logger.Info("in-step progress", "l1", l1head, "l2Origin", l2origin, "lag", l1head-l2origin)
		return l2origin > expectedBoundary+postBoundaryMargin
	}, 300*time.Second, 100*time.Millisecond)

	// Choose a RECENT post-Amsterdam L1 block as the reorg target: reorgDepth below the head so
	// it sits above the finalized horizon and inside geth's max reorg depth, while the L2 has
	// already derived past it.
	oldHead := sys.L1EL.BlockRefByLabel(eth.Unsafe).Number
	require.Greater(oldHead, reorgDepth+1, "need enough L1 history to pick a recent, non-genesis reorg target")
	l1Height := oldHead - reorgDepth

	l1Before := sys.L1EL.BlockRefByNumber(l1Height)
	l1Parent := sys.L1EL.BlockRefByNumber(l1Height - 1)
	require.True(l1Config.IsAmsterdam(new(big.Int).SetUint64(l1Height), l1Before.Time),
		"reorg target must be a POST-Amsterdam L1 block")
	require.True(l1Config.IsAmsterdam(new(big.Int).SetUint64(l1Height-1), l1Parent.Time),
		"the fork parent must ALSO be post-Amsterdam — the reorg stays entirely past the Glamsterdam boundary")
	require.Less(oldHead-l1Height, uint64(20), "reorg target must be above the finalized horizon")
	require.Less(oldHead-l1Height, uint64(32), "reorg must be within geth's max reorg depth")

	l2BeforeReorg := sys.L2EL.BlockRefByLabel(eth.Unsafe)
	require.Greater(l2BeforeReorg.L1Origin.Number, l1Height,
		"L2 must have derived past the reorg target before the reorg (so the target's epoch exists on L2)")

	// The reorg target is an L1 epoch boundary. Locate the L2 block that OPENS the epoch
	// anchored at l1Height and confirm it currently derives from the OLD canonical target block,
	// so we can later prove that exact epoch re-derives onto the NEW canonical L1.
	epochOpener, ok := findEpochOpener(l2BeforeReorg.Number, l1Height)
	require.True(ok, "the target L1 block must open an L2 epoch before the reorg")
	require.Equal(l1Height, epochOpener.L1Origin.Number, "epoch opener must reference the target L1 block")
	require.Equal(l1Before.Hash, epochOpener.L1Origin.Hash,
		"before the reorg the epoch opener must derive from the OLD canonical target L1 block")
	logger.Info("reorg target = recent post-Amsterdam L1 epoch boundary",
		"l1Height", l1Height, "l1Head", oldHead, "epochOpenerL2", epochOpener.Number, "l2Unsafe", l2BeforeReorg.Number)

	// produceL1 makes one L1 block WITHOUT requiring the L2 to keep pace — during a reorg the
	// L2 origin legitimately falls behind, so the strict in-step catch-up cannot hold here.
	produceL1 := func() {
		require.NoError(ts.New(ctx, seqtypes.BuildOpts{Parent: common.Hash{}}))
		require.NoError(ts.Next(ctx))
	}

	// Inject the L1 reorg: build a competing block on the POST-Amsterdam parent of the target
	// (the TestSequencer forces it canonical), then extend the competing chain past the old
	// head so it wins.
	require.NoError(ts.New(ctx, seqtypes.BuildOpts{Parent: l1Before.ParentHash}))
	require.NoError(ts.Next(ctx))
	require.Eventually(func() bool {
		sys.AdvanceTime(2 * time.Second)
		return sys.L1EL.BlockRefByLabel(eth.Unsafe).Number >= l1Height
	}, 30*time.Second, 200*time.Millisecond)
	for sys.L1EL.BlockRefByLabel(eth.Unsafe).Number <= oldHead+1 {
		produceL1()
	}

	l1After := sys.L1EL.BlockRefByNumber(l1Height)
	require.NotEqual(l1After.Hash, l1Before.Hash, "L1 must have reorged at the target epoch-boundary height")
	require.True(l1Config.IsAmsterdam(new(big.Int).SetUint64(l1Height), l1After.Time),
		"the reorged target block must still be post-Amsterdam (old AND new chain are past the boundary)")
	logger.Info("L1 reorged at a post-Amsterdam epoch boundary", "height", l1Height, "old", l1Before.Hash, "new", l1After.Hash)

	// The L2 must detect the L1 reorg and reorg its own chain: the L2 block that derived from
	// the old target-era L1 must change, and the chain must keep advancing (no wedge). Give it a
	// generous window: advance the clock so derivation re-runs, keeping the L1 a little ahead of
	// the L2 origin so there is always L1 to consume, without the strict in-step catch-up.
	require.Eventually(func() bool {
		sys.AdvanceTime(2 * time.Second)
		l2origin := sys.L2EL.BlockRefByLabel(eth.Unsafe).L1Origin.Number
		if l2origin+2 >= sys.L1EL.BlockRefByLabel(eth.Unsafe).Number {
			produceL1()
		}
		l2At := sys.L2EL.BlockRefByNumber(l2BeforeReorg.Number)
		l2Head := sys.L2EL.BlockRefByLabel(eth.Unsafe)
		return l2At.Hash != l2BeforeReorg.Hash && l2Head.Number > l2BeforeReorg.Number
	}, 240*time.Second, 300*time.Millisecond)

	l2At := sys.L2EL.BlockRefByNumber(l2BeforeReorg.Number)
	require.NotEqual(l2At.Hash, l2BeforeReorg.Hash,
		"L2 must have reorged the block that derived from the old epoch-boundary L1")
	logger.Info("L2 reorged and kept advancing after the post-Amsterdam L1 reorg",
		"l2Head", sys.L2EL.BlockRefByLabel(eth.Unsafe).Number)

	// Convergence is more than "the sequencer reorged and kept moving": the L2 must re-derive
	// onto the NEW canonical L1, and the INDEPENDENT verifier must reach the same re-derived
	// block. Without these, a sequencer that forked off alone or kept a stale L1 origin would
	// still pass the checks above.

	// (a) The re-derived L2 block's L1 origin must be a block on the NEW canonical L1 chain
	// (hash-matched), and still post-Amsterdam — it tracked the reorged L1, not a stale origin.
	canonOrigin := sys.L1EL.BlockRefByNumber(l2At.L1Origin.Number)
	require.Equal(l2At.L1Origin.Hash, canonOrigin.Hash,
		"reorged L2 block must derive from the NEW canonical L1 (not a stale pre-reorg origin)")
	require.True(l1Config.IsAmsterdam(new(big.Int).SetUint64(canonOrigin.Number), canonOrigin.Time),
		"the re-derived L2 block's L1 origin must still be post-Amsterdam after the reorg")

	// (b) INDEPENDENT verifier convergence. Both the sequencer and the verifier derive their SAFE
	// chain from L1 alone (not by gossiping unsafe blocks), so after the reorg the verifier must
	// independently re-derive the SAME post-reorg safe chain. A deep reorg makes the verifier's
	// pipeline lag, so the window is generous and we keep the clock and L1 moving while it catches
	// up; if the verifier cannot re-derive the sequencer's post-reorg safe block, this test fails.
	driveWhile := func(cond func() bool, timeout time.Duration, msg string) {
		require.Eventually(func() bool {
			sys.AdvanceTime(2 * time.Second)
			if sys.L2EL.BlockRefByLabel(eth.Unsafe).L1Origin.Number+2 >= sys.L1EL.BlockRefByLabel(eth.Unsafe).Number {
				produceL1()
			}
			return cond()
		}, timeout, 300*time.Millisecond, msg)
	}

	// (b-i) First drive the SEQUENCER's safe head PAST the reorged L1 block, so its safe chain
	// provably contains the re-derived epoch (not just unsafe blocks that never went through L1).
	driveWhile(func() bool {
		return sys.L2EL.BlockRefByLabel(eth.Safe).L1Origin.Number > l1Height
	}, 240*time.Second, "sequencer safe head must derive past the reorged L1 block")
	seqSafe := sys.L2EL.BlockRefByLabel(eth.Safe)
	require.Greater(seqSafe.Number, uint64(0), "sequencer must have a post-reorg safe head")

	// (b-ii) The verifier must INDEPENDENTLY reach that exact safe block — same height AND hash.
	// Waiting on the hash (not just the height) tolerates the verifier briefly sitting at that
	// height on the stale pre-reorg chain before its pipeline unwinds and re-derives.
	driveWhile(func() bool {
		vSafe := sys.L2ELB.BlockRefByLabel(eth.Safe)
		return vSafe.Number >= seqSafe.Number && sys.L2ELB.BlockRefByNumber(seqSafe.Number).Hash == seqSafe.Hash
	}, 300*time.Second, "verifier must independently re-derive the sequencer's post-reorg safe block")
	require.Equal(seqSafe.Hash, sys.L2ELB.BlockRefByNumber(seqSafe.Number).Hash,
		"sequencer and verifier must derive an identical safe L2 block at height %d after the post-Amsterdam reorg", seqSafe.Number)
	logger.Info("independent verifier re-derived the sequencer's post-reorg safe chain",
		"safeHeight", seqSafe.Number, "safeHash", seqSafe.Hash, "safeOrigin", seqSafe.L1Origin.Number)

	// (c) Epoch-boundary re-derivation: the epoch anchored at the reorged L1 block must re-open
	// on L2 and derive from the NEW canonical target block — not the stale pre-reorg one. The
	// epoch opener's L2 number may shift across the reorg, so re-scan for it from the current
	// unsafe head (the chain is converged by now).
	reOpener, ok := findEpochOpener(sys.L2EL.BlockRefByLabel(eth.Unsafe).Number, l1Height)
	require.True(ok, "the reorged L1 block must re-open its L2 epoch after re-derivation")
	require.Equal(l1After.Hash, reOpener.L1Origin.Hash,
		"the epoch anchored at the reorged L1 boundary must re-derive onto the NEW canonical L1 block")
	require.NotEqual(epochOpener.L1Origin.Hash, reOpener.L1Origin.Hash,
		"the epoch's L1 origin hash must actually have changed across the reorg (real re-derivation)")
	logger.Info("L2 re-derived the reorged epoch onto the new canonical L1",
		"height", l2BeforeReorg.Number, "hash", l2At.Hash, "epochOrigin", l1Height, "newOriginHash", reOpener.L1Origin.Hash)
}
