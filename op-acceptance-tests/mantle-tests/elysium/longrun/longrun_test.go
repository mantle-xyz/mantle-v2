package longrun

import (
	"math/big"
	"os"
	"strconv"
	"testing"
	"time"

	opforks "github.com/ethereum-optimism/optimism/op-core/forks"
	"github.com/ethereum-optimism/optimism/op-devstack/devtest"
	"github.com/ethereum-optimism/optimism/op-devstack/presets"
	"github.com/ethereum-optimism/optimism/op-service/eth"
)

// TestSystem_LongRun_AcrossUpgrade runs the L2 continuously for a sustained window after the L1
// has upgraded to Glamsterdam and asserts it stays healthy the whole time: the unsafe head keeps
// growing (no stall), the safe head keeps advancing and never regresses (derivation keeps up), a
// once-safe block never reorgs, and every sampled head stays Arsia.
//
// The window defaults to ~90s so it fits a CI run; set ELYSIUM_LONGRUN_SECONDS to run the full
// 30-minute soak against a real system (e.g. under sysext).
//
// Flips red if, at any sample over the run, the unsafe head stalls, the safe head regresses, a
// previously-safe block reorgs, or a header leaks an Amsterdam field.
func TestSystem_LongRun_AcrossUpgrade(gt *testing.T) {
	t := devtest.SerialT(gt)
	sys := presets.NewMantleMinimal(t)
	require := t.Require()
	ctx := t.Ctx()

	l1Config := sys.L1Network.Escape().ChainConfig()
	require.True(sys.L2Chain.IsMantleForkActive(opforks.MantleElysium), "L2 must run with Mantle Elysium active")
	require.NotNil(l1Config.AmsterdamTime, "L1 AmsterdamTime must be configured")

	runFor := 90 * time.Second
	if v := os.Getenv("ELYSIUM_LONGRUN_SECONDS"); v != "" {
		if s, err := strconv.Atoi(v); err == nil && s > 0 {
			runFor = time.Duration(s) * time.Second
		}
	}

	// Cross Amsterdam and wait for the L2 origin to cross it too.
	sys.L1EL.WaitForTime(*l1Config.AmsterdamTime)
	require.Eventually(func() bool {
		o := sys.L2EL.BlockRefByLabel(eth.Unsafe).L1Origin.Number
		r := sys.L1EL.BlockRefByNumber(o)
		return l1Config.IsAmsterdam(new(big.Int).SetUint64(o), r.Time)
	}, 120*time.Second, time.Second, "L2 unsafe origin must cross Amsterdam before the run")

	startUnsafe := sys.L2EL.BlockRefByLabel(eth.Unsafe).Number
	startSafe := sys.L2CL.SyncStatus().SafeL2
	prevUnsafe := startUnsafe
	prevSafe := startSafe.Number
	safeCheckpoint := startSafe

	samples := 0
	deadline := time.Now().Add(runFor)
	for time.Now().Before(deadline) {
		time.Sleep(6 * time.Second)

		unsafe := sys.L2EL.BlockRefByLabel(eth.Unsafe).Number
		ss := sys.L2CL.SyncStatus()

		require.GreaterOrEqualf(unsafe, prevUnsafe, "unsafe head must not regress (was %d)", prevUnsafe)
		require.GreaterOrEqualf(ss.SafeL2.Number, prevSafe, "safe head must not regress (was %d)", prevSafe)

		// A previously-safe block must never reorg.
		got := sys.L2EL.BlockRefByNumber(safeCheckpoint.Number)
		require.Equalf(safeCheckpoint.Hash, got.Hash,
			"a previously-safe block (#%d) must never reorg over the run", safeCheckpoint.Number)

		// The current head must stay Arsia.
		info, _, err := sys.L2EL.Escape().EthClient().InfoAndTxsByHash(ctx, sys.L2EL.BlockRefByLabel(eth.Unsafe).Hash)
		require.NoError(err, "read current unsafe head")
		require.Nil(info.Header().BlockAccessListHash, "head must stay Arsia over the run (no BAL)")
		require.Nil(info.Header().SlotNumber, "head must stay Arsia over the run (no SlotNumber)")

		prevUnsafe = unsafe
		prevSafe = ss.SafeL2.Number
		safeCheckpoint = ss.SafeL2
		samples++
	}

	// Over the whole run both heads advanced meaningfully and the safe origin is post-Amsterdam.
	require.Greaterf(prevUnsafe, startUnsafe+5, "unsafe head must advance over the run (start %d)", startUnsafe)
	require.Greaterf(prevSafe, startSafe.Number, "safe head must advance over the run (start %d)", startSafe.Number)
	require.GreaterOrEqual(samples, 3, "must have sampled several times over the run")
	finalOrigin := sys.L2CL.SyncStatus().SafeL2.L1Origin.Number
	finalRef := sys.L1EL.BlockRefByNumber(finalOrigin)
	require.True(l1Config.IsAmsterdam(new(big.Int).SetUint64(finalOrigin), finalRef.Time),
		"the safe head's L1 origin must be post-Amsterdam at the end of the run")
	t.Log("L2 stayed healthy over the long run", "samples", samples, "unsafe", prevUnsafe, "safe", prevSafe)
}
