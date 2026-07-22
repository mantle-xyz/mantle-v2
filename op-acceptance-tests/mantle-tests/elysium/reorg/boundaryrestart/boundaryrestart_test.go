package boundaryrestart

import (
	"context"
	"math/big"
	"testing"
	"time"

	"github.com/ethereum-optimism/optimism/op-acceptance-tests/mantle-tests/elysium/internal/testhelpers"
	"github.com/ethereum-optimism/optimism/op-devstack/devtest"
	"github.com/ethereum-optimism/optimism/op-devstack/presets"
	"github.com/ethereum-optimism/optimism/op-service/eth"
	"github.com/ethereum-optimism/optimism/op-service/retry"
)

// TestBoundary_L2RestartDuringL1Upgrade restarts op-node while L1 crosses the
// Amsterdam boundary. The restarted pipeline must consume the first Amsterdam
// headers without losing the pre-restart safe block.
func TestBoundary_L2RestartDuringL1Upgrade(gt *testing.T) {
	t := devtest.SerialT(gt)
	sys := presets.NewMantleMinimal(t)
	require := t.Require()
	ctx := t.Ctx()
	logger := t.Logger()

	l1Config := sys.L1Network.Escape().ChainConfig()
	require.NotNil(l1Config.AmsterdamTime, "L1 AmsterdamTime must be configured")

	// originIsAmsterdam reports whether an L2 block's L1 origin is a post-Amsterdam L1 block.
	originIsAmsterdam := func(ref eth.L2BlockRef) bool {
		if ref.Number == 0 {
			return false
		}
		l1 := sys.L1EL.BlockRefByNumber(ref.L1Origin.Number)
		return l1Config.IsAmsterdam(new(big.Int).SetUint64(ref.L1Origin.Number), l1.Time)
	}

	// 1) Stop op-node after it has a non-genesis pre-Amsterdam safe head.
	require.Eventually(func() bool {
		ss := sys.L2CL.SyncStatus()
		return ss.SafeL2.Number > 0 && !originIsAmsterdam(ss.SafeL2)
	}, 120*time.Second, 1*time.Second, "L2 must derive a pre-Amsterdam safe head before the restart window")

	before := sys.L2CL.SyncStatus()
	require.False(originIsAmsterdam(before.SafeL2),
		"pre-restart safe origin must still be pre-Amsterdam — the crossing must be left to the post-restart pipeline")
	logger.Info("stopping op-node before the boundary",
		"safe", before.SafeL2.Number, "safeOrigin", before.SafeL2.L1Origin.Number)

	// 2) Stop the op-node.
	sys.L2CL.Stop()
	{
		downCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		_, err := retry.Do[*eth.SyncStatus](downCtx, 10, retry.Fixed(500*time.Millisecond), func() (*eth.SyncStatus, error) {
			return sys.L2CL.Escape().RollupAPI().SyncStatus(downCtx)
		})
		cancel()
		require.Error(err, "op-node must be unreachable while stopped")
	}

	// 3) Let L1 cross Amsterdam while op-node is down.
	testhelpers.WaitForGlamsterdamL1(t, sys.L1EL, *l1Config.AmsterdamTime)
	logger.Info("L1 crossed Amsterdam while op-node was down")

	// The activation L1 block (first Amsterdam block).
	l1Head := sys.L1EL.BlockRefByLabel(eth.Unsafe).Number
	var activation uint64
	foundAct := false
	for n := uint64(1); n <= l1Head; n++ {
		ref := sys.L1EL.BlockRefByNumber(n)
		if l1Config.IsAmsterdam(new(big.Int).SetUint64(n), ref.Time) {
			activation = n
			foundAct = true
			break
		}
	}
	require.True(foundAct, "must find the activation L1 block")
	require.Greater(activation, before.SafeL2.L1Origin.Number,
		"the activation block must be ahead of the pre-restart safe origin (crossing is post-restart)")

	// 4) Restart the op-node.
	sys.L2CL.Start()
	require.Eventually(func() bool {
		reqCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		_, err := sys.L2CL.Escape().RollupAPI().SyncStatus(reqCtx)
		return err == nil
	}, 60*time.Second, 500*time.Millisecond, "op-node must come back online after restart")
	logger.Info("op-node back online after the boundary")

	// 5) The restarted op-node must re-derive past the activation block.
	require.Eventually(func() bool {
		return sys.L2CL.SyncStatus().SafeL2.L1Origin.Number > activation
	}, 180*time.Second, 1*time.Second,
		"op-node restarted across the transition must re-derive past the Amsterdam activation block")
	final := sys.L2CL.SyncStatus()

	// 6) No data loss: the pre-restart safe block survives unchanged.
	got := sys.L2EL.BlockRefByNumber(before.SafeL2.Number)
	require.Equal(before.SafeL2.Hash, got.Hash,
		"the pre-restart safe block must survive the restart unchanged (no data loss / reorg)")

	// 7) The crossed L1 origin must carry the new Amsterdam header fields.
	l1Info, _, err := sys.L1EL.EthClient().InfoAndTxsByHash(ctx, final.SafeL2.L1Origin.Hash)
	require.NoError(err, "must read the crossed L1 origin")
	require.True(l1Config.IsAmsterdam(new(big.Int).SetUint64(l1Info.NumberU64()), l1Info.Time()),
		"the post-restart safe head's L1 origin must be post-Amsterdam")
	require.NotNil(l1Info.Header().BlockAccessListHash,
		"the crossed L1 origin must carry an EIP-7928 BlockAccessListHash (genuine Glamsterdam)")
	require.NotNil(l1Info.Header().SlotNumber,
		"the crossed L1 origin must carry an EIP-7843 SlotNumber (genuine Glamsterdam)")
	logger.Info("op-node re-derived across the Amsterdam boundary after a restart spanning the transition",
		"safe", final.SafeL2.Number, "safeOrigin", final.SafeL2.L1Origin.Number, "activation", activation)
}
