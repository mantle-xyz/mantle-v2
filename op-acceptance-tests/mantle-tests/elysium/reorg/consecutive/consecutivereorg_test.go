package consecutive

import (
	"fmt"
	"math/big"
	"testing"
	"time"

	"github.com/ethereum-optimism/optimism/op-acceptance-tests/mantle-tests/elysium/internal/l1drive"
	"github.com/ethereum-optimism/optimism/op-devstack/devtest"
	"github.com/ethereum-optimism/optimism/op-devstack/presets"
	"github.com/ethereum-optimism/optimism/op-service/eth"
)

// postBoundaryMargin keeps every reorg target and fork parent post-Amsterdam.
const postBoundaryMargin = uint64(8)

// reorgDepth keeps each target recent, non-finalized, and already consumed by L2.
const reorgDepth = uint64(4)

// numReorgs is enough to prove the pipeline resets between reorgs.
const numReorgs = 2

// TestL1Reorg_Consecutive_PostUpgrade injects post-Amsterdam L1 reorgs
// back-to-back and requires the sequencer and verifier to converge after each
// round. The activation-boundary case is covered by reorg/.
func TestL1Reorg_Consecutive_PostUpgrade(gt *testing.T) {
	t := devtest.SerialT(gt)
	sys := presets.NewMantleSingleChainMultiNodeWithTestSeq(t)
	require := t.Require()
	logger := t.Logger()

	l1Config := sys.L1Network.Escape().ChainConfig()
	require.NotNil(l1Config.AmsterdamTime, "L1 AmsterdamTime must be configured")

	drive := l1drive.New(t, sys)

	// Drive far enough past Amsterdam that every reorg target is post-Amsterdam.
	expectedBoundary := amsterdamOffset / uint64(l1BlockTime/time.Second)
	require.GreaterOrEqual(expectedBoundary, uint64(2), "offset must leave pre-Amsterdam blocks above genesis")

	// Keep this loop on the test goroutine so L1-production errors surface directly.
	const maxDriveIters = 96
	for i := 0; ; i++ {
		require.Lessf(i, maxDriveIters,
			"L2 origin never got %d epochs past the Amsterdam boundary (block %d) after %d L1 blocks",
			postBoundaryMargin, expectedBoundary, maxDriveIters)
		drive.InStep()
		if sys.L2EL.BlockRefByLabel(eth.Unsafe).L1Origin.Number > expectedBoundary+postBoundaryMargin {
			break
		}
	}
	logger.Info("driven past Amsterdam; starting consecutive reorgs",
		"l1", sys.L1EL.BlockRefByLabel(eth.Unsafe).Number,
		"l2Origin", sys.L2EL.BlockRefByLabel(eth.Unsafe).L1Origin.Number)

	// reorgOnce injects one post-Amsterdam reorg and waits for both nodes to converge.
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

		// Build a competing chain from the target's parent and extend it past the old head.
		drive.Fork(l1Before.ParentHash)
		require.Eventually(func() bool {
			sys.AdvanceTime(2 * time.Second)
			return sys.L1EL.BlockRefByLabel(eth.Unsafe).Number >= l1Height
		}, 30*time.Second, 200*time.Millisecond)
		// Bound the loop so a stuck competing chain fails with context.
		for i := 0; sys.L1EL.BlockRefByLabel(eth.Unsafe).Number <= oldHead+1; i++ {
			require.Lessf(uint64(i), reorgDepth+16,
				"round %d: competing chain never overtook the old head %d (L1 head stuck at %d)",
				round, oldHead, sys.L1EL.BlockRefByLabel(eth.Unsafe).Number)
			drive.Produce()
			require.NoError(drive.Err(), "round %d: L1 block production failed while extending the competing chain", round)
		}

		l1After := sys.L1EL.BlockRefByNumber(l1Height)
		require.NotEqualf(l1After.Hash, l1Before.Hash, "round %d: L1 must have reorged at the target height", round)
		require.Truef(l1Config.IsAmsterdam(new(big.Int).SetUint64(l1Height), l1After.Time),
			"round %d: the reorged target must still be post-Amsterdam", round)
		logger.Info("L1 reorged", "round", round, "height", l1Height, "old", l1Before.Hash, "new", l1After.Hash)

		// The L2 must reorg the old block and keep advancing.
		drive.While(func() bool {
			l2At := sys.L2EL.BlockRefByNumber(l2BeforeReorg.Number)
			l2Head := sys.L2EL.BlockRefByLabel(eth.Unsafe)
			return l2At.Hash != l2BeforeReorg.Hash && l2Head.Number > l2BeforeReorg.Number
		}, 240*time.Second, fmt.Sprintf("round %d: L2 must reorg and keep advancing", round))

		// The sequencer safe head must derive from the new canonical L1.
		drive.While(func() bool {
			return sys.L2EL.BlockRefByLabel(eth.Safe).L1Origin.Number > l1Height
		}, 240*time.Second, fmt.Sprintf("round %d: sequencer safe head must derive past the reorged block", round))
		seqSafe := sys.L2EL.BlockRefByLabel(eth.Safe)
		canonOrigin := sys.L1EL.BlockRefByNumber(seqSafe.L1Origin.Number)
		require.Equalf(seqSafe.L1Origin.Hash, canonOrigin.Hash,
			"round %d: sequencer safe head must derive from the new canonical L1, not a stale origin", round)

		// The verifier must reach the same post-reorg safe block by height and hash.
		drive.While(func() bool {
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
	drive.While(func() bool {
		return sys.L2EL.BlockRefByLabel(eth.Unsafe).Number > finalBefore.Number+2
	}, 60*time.Second, "L2 must keep advancing after the consecutive reorgs (no wedge)")
	logger.Info("L2 survived consecutive post-Amsterdam L1 reorgs and stayed live",
		"reorgs", numReorgs, "finalUnsafe", sys.L2EL.BlockRefByLabel(eth.Unsafe).Number)
}
