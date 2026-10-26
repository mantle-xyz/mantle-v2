package derivepoch

import (
	"math/big"
	"testing"
	"time"

	"github.com/ethereum-optimism/optimism/op-acceptance-tests/mantle-tests/elysium/internal/testhelpers"
	opforks "github.com/ethereum-optimism/optimism/op-core/forks"
	"github.com/ethereum-optimism/optimism/op-devstack/devtest"
	"github.com/ethereum-optimism/optimism/op-devstack/presets"
	"github.com/ethereum-optimism/optimism/op-service/eth"
)

// minCrossedEpochs is the number of additional post-Amsterdam L1 origins the
// safe chain must derive across. OP-Stack derivation advances origins one L1
// block at a time, so this catches boundary stalls and skipped epochs.
const minCrossedEpochs = uint64(4)

// TestDerivation_L1EpochCross_PostUpgrade proves the Mantle L2 keeps deriving
// correctly across multiple post-Amsterdam L1 origins while the L2 stays on
// Mantle Elysium/Arsia rules.
//
// After activation it waits for the safe head to cross several consecutive L1
// origins, then walks the safe chain to prove the L1 origin axis advances by 0
// or 1, L2 blocks are contiguous and hash-linked, and every origin in the window
// matches the canonical post-Amsterdam L1 block.
func TestDerivation_L1EpochCross_PostUpgrade(gt *testing.T) {
	t := devtest.SerialT(gt)
	sys := presets.NewMantleMinimal(t)
	require := t.Require()

	rollupCfg := sys.L2Chain.Escape().RollupConfig()
	l1Config := sys.L1Network.Escape().ChainConfig()

	require.True(sys.L2Chain.IsMantleForkActive(opforks.MantleElysium),
		"L2 must run with Mantle Elysium active (Arsia rules in effect) across the boundary")
	require.NotNil(rollupCfg.MantleElysiumTime, "MantleElysiumTime must be configured")
	require.NotNil(l1Config.AmsterdamTime, "L1 AmsterdamTime must be configured")

	// isAmsterdamOrigin resolves an L1 origin number to its canonical L1 block and
	// reports whether that block is post-Amsterdam (Glamsterdam EL).
	isAmsterdamOrigin := func(originNum uint64) (bool, eth.L1BlockRef) {
		l1 := sys.L1EL.BlockRefByNumber(originNum)
		return l1Config.IsAmsterdam(new(big.Int).SetUint64(originNum), l1.Time), l1
	}

	t.Log("Waiting for L1 Amsterdam (Glamsterdam EL) to activate")
	testhelpers.WaitForGlamsterdamL1(t, sys.L1EL, *l1Config.AmsterdamTime)
	t.Log("L1 Amsterdam activated")

	l2BlockTime := time.Duration(rollupCfg.BlockTime) * time.Second

	// Step 1: wait until the L2 safe head references its first post-Amsterdam L1 origin.
	var startSafe eth.L2BlockRef
	require.Eventually(func() bool {
		startSafe = sys.L2CL.SyncStatus().SafeL2
		ams, _ := isAmsterdamOrigin(startSafe.L1Origin.Number)
		if !ams {
			t.Log("L2 safe head still on a pre-Amsterdam L1 origin, waiting...",
				"safe", startSafe.Number, "origin", startSafe.L1Origin.Number)
		}
		return ams
	}, 180*time.Second, l2BlockTime, "L2 safe head must reach a post-Amsterdam L1 origin")

	startOrigin := startSafe.L1Origin.Number
	targetOrigin := startOrigin + minCrossedEpochs

	// Step 2: wait until the safe head has crossed several post-Amsterdam origins.
	var endSafe eth.L2BlockRef
	require.Eventually(func() bool {
		endSafe = sys.L2CL.SyncStatus().SafeL2
		t.Log("crossing post-Amsterdam L1 epochs",
			"safe", endSafe.Number, "origin", endSafe.L1Origin.Number, "target", targetOrigin)
		return endSafe.L1Origin.Number >= targetOrigin
	}, 300*time.Second, l2BlockTime,
		"L2 safe head L1 origin must advance across >= 4 consecutive post-Amsterdam L1 blocks (no boundary stall)")

	endOrigin := endSafe.L1Origin.Number
	require.Greater(endSafe.Number, startSafe.Number,
		"L2 safe block number must advance while crossing epochs")
	require.GreaterOrEqual(endOrigin-startOrigin, minCrossedEpochs,
		"L2 safe head must cross >= 4 post-Amsterdam L1 origins")

	// Walk the safe chain and prove no L1-origin or L2-block gap.
	seenOrigins := make(map[uint64]bool)
	prev := startSafe
	seenOrigins[prev.L1Origin.Number] = true
	require.Equal(startOrigin, prev.L1Origin.Number, "walk must start at the recorded start origin")

	for n := startSafe.Number + 1; n <= endSafe.Number; n++ {
		cur := sys.L2EL.BlockRefByNumber(n)

		// L2 axis: contiguous, hash-linked safe chain (no missing/duplicated block, no reorg hole).
		require.Equalf(prev.Number+1, cur.Number, "L2 safe block numbers must be contiguous at %d", n)
		require.Equalf(prev.Hash, cur.ParentHash, "L2 safe block %d must chain onto its parent (no gap)", n)

		// L1 axis: origin advances by exactly 0 or 1 — op-node never skips an L1 epoch.
		require.GreaterOrEqualf(cur.L1Origin.Number, prev.L1Origin.Number,
			"L1 origin must be non-decreasing along the safe chain at L2 block %d", n)
		delta := cur.L1Origin.Number - prev.L1Origin.Number
		require.LessOrEqualf(delta, uint64(1),
			"each L2 block may advance the L1 origin by at most one epoch (no skipped L1 block) at L2 block %d", n)

		if delta == 1 {
			// First L2 block of a new epoch: the new origin must be a real, canonical,
			// post-Amsterdam L1 block that chains onto the previous origin.
			ams, l1 := isAmsterdamOrigin(cur.L1Origin.Number)
			require.Truef(ams, "crossed L1 origin %d must be post-Amsterdam", cur.L1Origin.Number)
			require.Equalf(cur.L1Origin.Hash, l1.Hash,
				"L2 safe block %d L1 origin must match canonical L1 block %d (derivation tracked real L1)",
				n, cur.L1Origin.Number)
			require.Equalf(prev.L1Origin.Number+1, cur.L1Origin.Number,
				"consecutive epochs must reference consecutive L1 blocks at L2 block %d", n)
		}
		seenOrigins[cur.L1Origin.Number] = true
		prev = cur
	}

	// Coverage: every integer origin in [startOrigin, endOrigin] must appear on the safe
	// chain and be post-Amsterdam — a genuine multi-epoch cross with no silently skipped epoch.
	for o := startOrigin; o <= endOrigin; o++ {
		require.Truef(seenOrigins[o],
			"L2 safe chain must derive every L1 epoch in the crossed window; missing origin %d", o)
		ams, _ := isAmsterdamOrigin(o)
		require.Truef(ams, "every crossed L1 origin must be Amsterdam; origin %d is not", o)
	}

	t.Log("L2 safe chain derived across consecutive post-Amsterdam L1 epochs with no gap",
		"startSafe", startSafe.Number, "endSafe", endSafe.Number,
		"startOrigin", startOrigin, "endOrigin", endOrigin, "epochsCrossed", endOrigin-startOrigin)
}
