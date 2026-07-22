package epoch

import (
	"math/big"
	"testing"
	"time"

	"github.com/ethereum-optimism/optimism/op-acceptance-tests/mantle-tests/elysium/internal/l1drive"
	"github.com/ethereum-optimism/optimism/op-devstack/devtest"
	"github.com/ethereum-optimism/optimism/op-devstack/presets"
	"github.com/ethereum-optimism/optimism/op-service/eth"
)

// postBoundaryMargin keeps the target and fork parent comfortably post-Amsterdam.
const postBoundaryMargin = uint64(8)

// reorgDepth keeps the target recent, non-finalized, and already consumed by L2.
const reorgDepth = uint64(4)

// TestL1Reorg_AtEpochBoundary_PostUpgrade reorgs a recent post-Amsterdam L1 block that opened an
// L2 sequencing epoch. The fork point and replacement block are both post-upgrade, so this
// complements the reorg package's boundary-straddling case.
//
// The sequencer must re-derive onto the new canonical L1, and the verifier must
// converge on the same safe L2 block. This package has one L1-driving test.
func TestL1Reorg_AtEpochBoundary_PostUpgrade(gt *testing.T) {
	t := devtest.SerialT(gt)
	sys := presets.NewMantleSingleChainMultiNodeWithTestSeq(t)
	require := t.Require()
	logger := t.Logger()

	l1Config := sys.L1Network.Escape().ChainConfig()
	require.NotNil(l1Config.AmsterdamTime, "L1 AmsterdamTime must be configured")

	drive := l1drive.New(t, sys)

	// Drive far enough past Amsterdam that the target and fork parent are post-Amsterdam.
	expectedBoundary := amsterdamOffset / uint64(l1BlockTime/time.Second)
	require.GreaterOrEqual(expectedBoundary, uint64(2), "offset must leave pre-Amsterdam blocks above genesis")

	// Keep this loop on the test goroutine so L1-production errors surface directly.
	const maxDriveIters = 96
	for i := 0; ; i++ {
		require.Lessf(i, maxDriveIters,
			"L2 origin never got %d epochs past the Amsterdam boundary (block %d) after %d L1 blocks",
			postBoundaryMargin, expectedBoundary, maxDriveIters)
		drive.InStep()
		l1head := sys.L1EL.BlockRefByLabel(eth.Unsafe).Number
		l2origin := sys.L2EL.BlockRefByLabel(eth.Unsafe).L1Origin.Number
		logger.Info("in-step progress", "l1", l1head, "l2Origin", l2origin, "lag", l1head-l2origin)
		if l2origin > expectedBoundary+postBoundaryMargin {
			break
		}
	}

	// Choose a recent post-Amsterdam target that L2 has already derived past.
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

	// Locate the L2 epoch opener so the same epoch can be checked after the reorg.
	epochOpener, ok := drive.EpochOpener(l2BeforeReorg.Number, l1Height)
	require.True(ok, "the target L1 block must open an L2 epoch before the reorg")
	require.Equal(l1Height, epochOpener.L1Origin.Number, "epoch opener must reference the target L1 block")
	require.Equal(l1Before.Hash, epochOpener.L1Origin.Hash,
		"before the reorg the epoch opener must derive from the OLD canonical target L1 block")
	logger.Info("reorg target = recent post-Amsterdam L1 epoch boundary",
		"l1Height", l1Height, "l1Head", oldHead, "epochOpenerL2", epochOpener.Number, "l2Unsafe", l2BeforeReorg.Number)

	// Build a competing chain from the post-Amsterdam parent and extend it past the old head.
	drive.Fork(l1Before.ParentHash)
	require.Eventually(func() bool {
		sys.AdvanceTime(2 * time.Second)
		return sys.L1EL.BlockRefByLabel(eth.Unsafe).Number >= l1Height
	}, 30*time.Second, 200*time.Millisecond)
	// Bound the loop so a stuck competing chain fails with context.
	for i := 0; sys.L1EL.BlockRefByLabel(eth.Unsafe).Number <= oldHead+1; i++ {
		require.Lessf(uint64(i), reorgDepth+16,
			"competing chain never overtook the old head %d (L1 head stuck at %d)",
			oldHead, sys.L1EL.BlockRefByLabel(eth.Unsafe).Number)
		drive.Produce()
		require.NoError(drive.Err(), "L1 block production failed while extending the competing chain")
	}

	l1After := sys.L1EL.BlockRefByNumber(l1Height)
	require.NotEqual(l1After.Hash, l1Before.Hash, "L1 must have reorged at the target epoch-boundary height")
	require.True(l1Config.IsAmsterdam(new(big.Int).SetUint64(l1Height), l1After.Time),
		"the reorged target block must still be post-Amsterdam (old AND new chain are past the boundary)")
	logger.Info("L1 reorged at a post-Amsterdam epoch boundary", "height", l1Height, "old", l1Before.Hash, "new", l1After.Hash)

	// The L2 must reorg the old target-era block and keep advancing.
	require.Eventually(func() bool {
		if drive.Err() != nil {
			return true // bail out of polling; the error is asserted below
		}
		sys.AdvanceTime(2 * time.Second)
		l2origin := sys.L2EL.BlockRefByLabel(eth.Unsafe).L1Origin.Number
		if l2origin+2 >= sys.L1EL.BlockRefByLabel(eth.Unsafe).Number {
			drive.Produce()
		}
		l2At := sys.L2EL.BlockRefByNumber(l2BeforeReorg.Number)
		l2Head := sys.L2EL.BlockRefByLabel(eth.Unsafe)
		return l2At.Hash != l2BeforeReorg.Hash && l2Head.Number > l2BeforeReorg.Number
	}, 240*time.Second, 300*time.Millisecond)
	require.NoError(drive.Err(), "L1 block production failed while waiting for the L2 to reorg")

	l2At := sys.L2EL.BlockRefByNumber(l2BeforeReorg.Number)
	require.NotEqual(l2At.Hash, l2BeforeReorg.Hash,
		"L2 must have reorged the block that derived from the old epoch-boundary L1")
	logger.Info("L2 reorged and kept advancing after the post-Amsterdam L1 reorg",
		"l2Head", sys.L2EL.BlockRefByLabel(eth.Unsafe).Number)

	// The re-derived L2 block must track the new canonical L1.
	canonOrigin := sys.L1EL.BlockRefByNumber(l2At.L1Origin.Number)
	require.Equal(l2At.L1Origin.Hash, canonOrigin.Hash,
		"reorged L2 block must derive from the NEW canonical L1 (not a stale pre-reorg origin)")
	require.True(l1Config.IsAmsterdam(new(big.Int).SetUint64(canonOrigin.Number), canonOrigin.Time),
		"the re-derived L2 block's L1 origin must still be post-Amsterdam after the reorg")

	// Drive the sequencer safe head past the reorged L1 block.
	drive.While(func() bool {
		return sys.L2EL.BlockRefByLabel(eth.Safe).L1Origin.Number > l1Height
	}, 240*time.Second, "sequencer safe head must derive past the reorged L1 block")
	seqSafe := sys.L2EL.BlockRefByLabel(eth.Safe)
	require.Greater(seqSafe.Number, uint64(0), "sequencer must have a post-reorg safe head")

	// The verifier must reach the same safe block by height and hash.
	drive.While(func() bool {
		vSafe := sys.L2ELB.BlockRefByLabel(eth.Safe)
		return vSafe.Number >= seqSafe.Number && sys.L2ELB.BlockRefByNumber(seqSafe.Number).Hash == seqSafe.Hash
	}, 300*time.Second, "verifier must independently re-derive the sequencer's post-reorg safe block")
	require.Equal(seqSafe.Hash, sys.L2ELB.BlockRefByNumber(seqSafe.Number).Hash,
		"sequencer and verifier must derive an identical safe L2 block at height %d after the post-Amsterdam reorg", seqSafe.Number)
	logger.Info("independent verifier re-derived the sequencer's post-reorg safe chain",
		"safeHeight", seqSafe.Number, "safeHash", seqSafe.Hash, "safeOrigin", seqSafe.L1Origin.Number)

	// Re-scan because the epoch opener's L2 number may shift across the reorg.
	reOpener, ok := drive.EpochOpener(sys.L2EL.BlockRefByLabel(eth.Unsafe).Number, l1Height)
	require.True(ok, "the reorged L1 block must re-open its L2 epoch after re-derivation")
	require.Equal(l1After.Hash, reOpener.L1Origin.Hash,
		"the epoch anchored at the reorged L1 boundary must re-derive onto the NEW canonical L1 block")
	require.NotEqual(epochOpener.L1Origin.Hash, reOpener.L1Origin.Hash,
		"the epoch's L1 origin hash must actually have changed across the reorg (real re-derivation)")
	logger.Info("L2 re-derived the reorged epoch onto the new canonical L1",
		"height", l2BeforeReorg.Number, "hash", l2At.Hash, "epochOrigin", l1Height, "newOriginHash", reOpener.L1Origin.Hash)
}
