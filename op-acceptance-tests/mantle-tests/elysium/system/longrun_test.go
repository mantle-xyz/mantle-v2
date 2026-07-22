package system

import (
	"math/big"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/ethereum-optimism/optimism/op-acceptance-tests/mantle-tests/elysium/internal/testhelpers"
	opforks "github.com/ethereum-optimism/optimism/op-core/forks"
	"github.com/ethereum-optimism/optimism/op-devstack/devtest"
	"github.com/ethereum-optimism/optimism/op-devstack/presets"
	"github.com/ethereum-optimism/optimism/op-service/eth"
)

// TestL1Glamsterdam_LongRun_30min is a soak/liveness test: it runs the L2 continuously for a
// sustained window while the L1 runs Glamsterdam (an external Amsterdam-capable geth is the
// environment) and asserts the chain stays healthy the whole time. The unsafe head keeps growing
// (no stall), the safe head keeps advancing and never regresses (derivation keeps up with the
// Glamsterdam L1), a once-safe block never reorgs, and at the end the safe head's L1 origin is
// post-Amsterdam (the run genuinely consumed Glamsterdam L1 blocks). The Glamsterdam L1 is the
// environment that could break Mantle's derivation, and these liveness assertions are what would
// catch that.
//
// This is the full 30-minute soak, so it is moved OUT of the CI gate: it skips unless ELYSIUM_HEAVY
// is set. The duration defaults to 30 minutes and can be overridden with ELYSIUM_LONGRUN_SECONDS.
//
// Reorg coverage uses both a fixed safe anchor and a rolling safe checkpoint. The fixed anchor
// catches deep rewrites, while the rolling checkpoint catches shallow rewrites near the safe head.
// Liveness is scaled to rollup block time instead of a fixed token count.
//
// Flips red if, at any sample over the run, the unsafe head stalls, the safe head regresses, or a
// previously-safe block reorgs; or if, over the whole run, either head advanced far slower than
// the configured block time implies.
func runSystemLongRunAcrossUpgrade(gt *testing.T) {
	if os.Getenv("ELYSIUM_HEAVY") == "" {
		gt.Skip("30-minute soak moved out of CI; run with ELYSIUM_HEAVY=1")
	}
	t := devtest.SerialT(gt)
	sys := presets.NewMantleMinimal(t)
	require := t.Require()

	l1Config := sys.L1Network.Escape().ChainConfig()
	rollupCfg := sys.L2Chain.Escape().RollupConfig()
	require.True(sys.L2Chain.IsMantleForkActive(opforks.MantleElysium), "L2 must run with Mantle Elysium active")
	require.NotNil(l1Config.AmsterdamTime, "L1 AmsterdamTime must be configured")
	require.NotZero(rollupCfg.BlockTime, "rollup config must declare an L2 block time")

	runFor := 30 * time.Minute
	if v := os.Getenv("ELYSIUM_LONGRUN_SECONDS"); v != "" {
		if s, err := strconv.Atoi(v); err == nil && s > 0 {
			runFor = time.Duration(s) * time.Second
		}
	}

	// Cross Amsterdam and wait for the L2 origin to cross it too.
	testhelpers.WaitForGlamsterdamL1(t, sys.L1EL, *l1Config.AmsterdamTime)
	require.Eventually(func() bool {
		o := sys.L2EL.BlockRefByLabel(eth.Unsafe).L1Origin.Number
		r := sys.L1EL.BlockRefByNumber(o)
		return l1Config.IsAmsterdam(new(big.Int).SetUint64(o), r.Time)
	}, 120*time.Second, time.Second, "L2 unsafe origin must cross Amsterdam before the run")

	startUnsafe := sys.L2EL.BlockRefByLabel(eth.Unsafe).Number
	startSafe := sys.L2CL.SyncStatus().SafeL2
	require.NotZero(startSafe.Number, "must start from a non-genesis safe head")
	prevUnsafe := startUnsafe
	prevSafe := startSafe.Number
	// Fixed anchor catches deep rewrites behind the safe head.
	safeAnchor := startSafe
	safeCheckpoint := startSafe

	samples := 0
	deadline := time.Now().Add(runFor)
	for time.Now().Before(deadline) {
		time.Sleep(6 * time.Second)

		unsafe := sys.L2EL.BlockRefByLabel(eth.Unsafe).Number
		ss := sys.L2CL.SyncStatus()

		require.GreaterOrEqualf(unsafe, prevUnsafe, "unsafe head must not regress (was %d)", prevUnsafe)
		require.GreaterOrEqualf(ss.SafeL2.Number, prevSafe, "safe head must not regress (was %d)", prevSafe)

		// The first safe block from this run must never be rewritten.
		anchorNow := sys.L2EL.BlockRefByNumber(safeAnchor.Number)
		require.Equalf(safeAnchor.Hash, anchorNow.Hash,
			"the run's first safe block (#%d) must never reorg over the run", safeAnchor.Number)

		// The previous sample's safe block catches shallow rewrites near the safe head.
		got := sys.L2EL.BlockRefByNumber(safeCheckpoint.Number)
		require.Equalf(safeCheckpoint.Hash, got.Hash,
			"the previous sample's safe block (#%d) must never reorg", safeCheckpoint.Number)

		prevUnsafe = unsafe
		prevSafe = ss.SafeL2.Number
		safeCheckpoint = ss.SafeL2
		samples++
	}

	// Over the whole run both heads advanced meaningfully and the safe origin is post-Amsterdam.
	//
	// Scale expected progress to the chain's block time, with slack for slow hosts and safe-head
	// derivation lag.
	const (
		minUnsafeGrowthNum = 1 // unsafe head: at least 1/2 of the expected block count
		minUnsafeGrowthDen = 2
		minSafeGrowthNum   = 1 // safe head: at least 1/4, it trails by design
		minSafeGrowthDen   = 4
	)
	expectedBlocks := uint64(runFor.Seconds()) / rollupCfg.BlockTime
	minUnsafeGrowth := expectedBlocks * minUnsafeGrowthNum / minUnsafeGrowthDen
	minSafeGrowth := expectedBlocks * minSafeGrowthNum / minSafeGrowthDen
	require.GreaterOrEqualf(prevUnsafe-startUnsafe, minUnsafeGrowth,
		"unsafe head must advance >= %d blocks over the %s run (block time %ds => ~%d expected); start %d, end %d",
		minUnsafeGrowth, runFor, rollupCfg.BlockTime, expectedBlocks, startUnsafe, prevUnsafe)
	require.GreaterOrEqualf(prevSafe-startSafe.Number, minSafeGrowth,
		"safe head must advance >= %d blocks over the %s run (block time %ds => ~%d expected); start %d, end %d",
		minSafeGrowth, runFor, rollupCfg.BlockTime, expectedBlocks, startSafe.Number, prevSafe)
	require.GreaterOrEqual(samples, 3, "must have sampled several times over the run")
	finalOrigin := sys.L2CL.SyncStatus().SafeL2.L1Origin.Number
	finalRef := sys.L1EL.BlockRefByNumber(finalOrigin)
	require.True(l1Config.IsAmsterdam(new(big.Int).SetUint64(finalOrigin), finalRef.Time),
		"the safe head's L1 origin must be post-Amsterdam at the end of the run")
	t.Log("L2 stayed healthy over the long run", "samples", samples, "unsafe", prevUnsafe, "safe", prevSafe)
}
