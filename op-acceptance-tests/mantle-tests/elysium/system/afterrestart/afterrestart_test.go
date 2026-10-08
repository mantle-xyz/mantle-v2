package afterrestart

import (
	"context"
	"math/big"
	"testing"
	"time"

	"github.com/ethereum-optimism/optimism/op-acceptance-tests/mantle-tests/elysium/internal/testhelpers"
	opforks "github.com/ethereum-optimism/optimism/op-core/forks"
	"github.com/ethereum-optimism/optimism/op-devstack/devtest"
	"github.com/ethereum-optimism/optimism/op-devstack/presets"
	"github.com/ethereum-optimism/optimism/op-service/eth"
	"github.com/ethereum-optimism/optimism/op-service/retry"
)

// TestL1Glamsterdam_AfterRestart covers steady-state restart recovery after the
// L1 is already post-Amsterdam.
//
// The op-node must stop, become unreachable, start again, recover the identical
// pre-restart safe block, and continue producing/deriving new blocks from the
// Glamsterdam L1. The harder "L1 crosses while op-node is down" case lives in
// boundaryrestart.
func TestL1Glamsterdam_AfterRestart(gt *testing.T) {
	t := devtest.SerialT(gt)
	sys := presets.NewMantleMinimal(t)
	require := t.Require()
	ctx := t.Ctx()
	logger := t.Logger()

	l1Config := sys.L1Network.Escape().ChainConfig()
	require.True(sys.L2Chain.IsMantleForkActive(opforks.MantleElysium), "L2 must run with Mantle Elysium active")
	require.NotNil(l1Config.AmsterdamTime, "L1 AmsterdamTime must be configured (L1 must be a Glamsterdam chain)")

	// originIsAmsterdam is a timestamp/config guard, not a payload-decode proof.
	originIsAmsterdam := func(ref eth.L2BlockRef) bool {
		if ref.Number == 0 {
			return false
		}
		l1Info, _, err := sys.L1EL.EthClient().InfoAndTxsByHash(ctx, ref.L1Origin.Hash)
		if err != nil {
			return false
		}
		return l1Config.IsAmsterdam(new(big.Int).SetUint64(l1Info.NumberU64()), l1Info.Time())
	}

	// 1) Wait for the L1 to activate Amsterdam (Glamsterdam EL).
	testhelpers.WaitForGlamsterdamL1(t, sys.L1EL, *l1Config.AmsterdamTime)
	logger.Info("L1 Amsterdam activated")

	// 2) Let the L2 genuinely derive across the boundary: wait for a non-genesis safe head
	//    whose L1 origin is post-Amsterdam. This makes the pre-restart state meaningful and
	//    ties the whole scenario to a live Glamsterdam L1.
	require.Eventually(func() bool {
		ss := sys.L2CL.SyncStatus()
		return ss.SafeL2.Number > 0 && originIsAmsterdam(ss.SafeL2)
	}, 180*time.Second, 1*time.Second, "L2 must derive a post-Amsterdam (Glamsterdam) safe head before the restart")

	before := sys.L2CL.SyncStatus()
	recordedUnsafe := before.UnsafeL2
	recordedSafe := before.SafeL2
	require.Greater(recordedSafe.Number, uint64(0), "must record a non-genesis safe head")
	logger.Info("recorded pre-restart heads",
		"unsafe", recordedUnsafe.Number, "safe", recordedSafe.Number, "safeL1Origin", recordedSafe.L1Origin.Number)

	// 3) Restart the L2 op-node via the ControlPlane.
	logger.Info("stopping L2 op-node", "id", sys.L2CL.ID())
	sys.L2CL.Stop()

	// While stopped, the op-node must be unreachable — this proves the restart is genuine and
	// not a no-op. (Use the raw RollupAPI, not the DSL SyncStatus() which asserts NoError.)
	{
		downCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		_, err := retry.Do[*eth.SyncStatus](downCtx, 10, retry.Fixed(500*time.Millisecond), func() (*eth.SyncStatus, error) {
			return sys.L2CL.Escape().RollupAPI().SyncStatus(downCtx)
		})
		cancel()
		require.Error(err, "op-node must be unreachable while stopped")
	}

	logger.Info("starting L2 op-node", "id", sys.L2CL.ID())
	sys.L2CL.Start()

	// 4) The op-node must come back online (its RPC answers again).
	require.Eventually(func() bool {
		reqCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		_, err := sys.L2CL.Escape().RollupAPI().SyncStatus(reqCtx)
		return err == nil
	}, 60*time.Second, 500*time.Millisecond, "op-node must come back online after restart")
	logger.Info("L2 op-node back online after restart")

	// 5) It must re-sync to at least the pre-restart safe head...
	require.Eventually(func() bool {
		return sys.L2CL.SyncStatus().SafeL2.Number >= recordedSafe.Number
	}, 180*time.Second, 1*time.Second, "L2 must re-sync to at least the pre-restart safe head after restart")

	// ...and that safe block must be unchanged: deterministic re-derivation under the same
	// Glamsterdam L1 must reproduce the identical block (no data loss, no reorg).
	got := sys.L2EL.BlockRefByNumber(recordedSafe.Number)
	require.Equal(recordedSafe.Hash, got.Hash,
		"the pre-restart safe block must survive the restart unchanged (no reorg / no data loss)")

	// 6) Liveness: the L2 must keep PRODUCING (unsafe advances -> sequencer resumed) AND keep
	//    DERIVING (safe advances past the recorded safe head -> derivation resumed under the
	//    Glamsterdam L1). Both past the recorded heads.
	require.Eventually(func() bool {
		ss := sys.L2CL.SyncStatus()
		return ss.UnsafeL2.Number > recordedUnsafe.Number && ss.SafeL2.Number > recordedSafe.Number
	}, 180*time.Second, 1*time.Second,
		"L2 must keep producing and deriving new blocks past the recorded heads after restart")

	final := sys.L2CL.SyncStatus()
	// No separate re-assertion that final.SafeL2's origin is post-Amsterdam: L1 origins are
	// monotonically non-decreasing, so once the pre-restart safe head (step 2) is post-Amsterdam
	// and the post-restart safe head advances past it (step 6), a fresh originIsAmsterdam(final)
	// check is already implied — it would only ever restate what steps 2 + 6 established.
	logger.Info("L2 op-node recovered after restart and kept deriving from a Glamsterdam L1",
		"unsafe", final.UnsafeL2.Number, "safe", final.SafeL2.Number, "safeL1Origin", final.SafeL2.L1Origin.Number)
}
