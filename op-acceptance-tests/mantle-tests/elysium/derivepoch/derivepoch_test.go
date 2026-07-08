package derivepoch

import (
	"math/big"
	"testing"
	"time"

	opforks "github.com/ethereum-optimism/optimism/op-core/forks"
	"github.com/ethereum-optimism/optimism/op-devstack/devtest"
	"github.com/ethereum-optimism/optimism/op-devstack/presets"
	"github.com/ethereum-optimism/optimism/op-service/eth"
)

// minCrossedEpochs is how many ADDITIONAL consecutive post-Amsterdam L1 origins the
// L2 safe chain must derive across after the Glamsterdam activation boundary.
//
// In OP-Stack derivation one L1 block == one L2 sequencing epoch: op-node fetches an
// L1 origin's receipts only when the L1 origin changes (attributes.go: the first block
// of an epoch is where l2Parent.L1Origin.Number != epoch.Number), and it advances the
// origin one block at a time — SafeL2.L1Origin.Number therefore increases by exactly 0
// or 1 from one L2 safe block to the next, never skipping an L1 block. Requiring the
// safe head's L1Origin.Number to advance by >= 4 proves the derivation window keeps
// stepping forward across MANY post-Glamsterdam L1 epochs rather than stalling at the
// boundary.
const minCrossedEpochs = uint64(4)

// TestDerivation_L1EpochCross_PostUpgrade proves the Mantle L2 keeps deriving
// correctly across the L1 Glamsterdam (Amsterdam EL) boundary. L1 is a vanilla
// subprocess Amsterdam geth (DEVSTACK_L1EL_KIND=geth) driven by auto-FakePoS; the L2
// stays on its own Mantle fork rules (Elysium, which implies Arsia is active).
//
// After L1 Amsterdam activates, the test waits (no manual L1/sequencer driving) until
// the L2 SAFE head's L1 origin has crossed >= minCrossedEpochs consecutive
// post-Amsterdam L1 blocks, then walks the safe chain block-by-block and asserts:
//
//   - No derivation gap on the L1 axis: consecutive L2 safe blocks advance their
//     L1Origin.Number by exactly 0 or 1 (op-node consumes L1 epochs one at a time and
//     never skips an L1 block).
//   - No gap on the L2 axis: safe block numbers are contiguous and hash-linked
//     (parentHash chains), i.e. the safe chain advances monotonically with no hole.
//   - Every L1 origin in the crossed window [startOrigin, endOrigin] is post-Amsterdam
//     via l1Config.IsAmsterdam AND matches the canonical L1 block hash at that height,
//     so the L2 tracked the real post-Glamsterdam L1 chain.
//   - Every integer origin in the window actually appears on the safe chain — a real
//     "cross" of many epochs, not a single jump that silently skipped epochs.
//
// Discriminating: a boundary-stall (derivation wedged at the first Amsterdam origin),
// a skipped L1 epoch, or an L2 safe-chain hole all fail here; only a pipeline that
// derives every consecutive post-Glamsterdam L1 epoch in order passes.
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
	sys.L1EL.WaitForTime(*l1Config.AmsterdamTime)
	t.Log("L1 Amsterdam activated")

	l2BlockTime := time.Duration(rollupCfg.BlockTime) * time.Second

	// Step 1: wait until the L2 SAFE head references its first post-Amsterdam L1 origin.
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

	// Step 2: wait until the safe head has crossed >= minCrossedEpochs more consecutive
	// post-Amsterdam origins (SafeL2.L1Origin.Number advanced by >= minCrossedEpochs).
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

	// Walk the L2 safe chain block-by-block across the crossed window and prove no gap
	// on either axis, and that every crossed L1 origin is a real post-Amsterdam block.
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
