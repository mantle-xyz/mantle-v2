package l1reorg

import (
	"errors"
	"math/big"
	"testing"
	"time"

	opforks "github.com/ethereum-optimism/optimism/op-core/forks"
	"github.com/ethereum-optimism/optimism/op-devstack/devtest"
	"github.com/ethereum-optimism/optimism/op-devstack/presets"
	"github.com/ethereum-optimism/optimism/op-devstack/stack"
	"github.com/ethereum-optimism/optimism/op-devstack/stack/match"
	"github.com/ethereum-optimism/optimism/op-service/eth"
	"github.com/ethereum-optimism/optimism/op-test-sequencer/sequencer/seqtypes"
	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
)

const l1BlockTime = 6 * time.Second

// Despite the package name, this file does not create an L1 reorg. It covers
// derivation from a fakepos-built Glamsterdam L1; reorg coverage lives in the
// reorg, reorgepoch, and consecutivereorg packages.

// TestL2SafeHeadConsumesFakePoSAmsterdamL1 drives the L1 chain manually via the
// TestSequencer fakepos builder across Amsterdam, then proves the L2 safe head
// advances to a real post-Amsterdam L1 origin. A fakepos builder stall fails
// before derivation; an op-node regression leaves the safe head on a pre-fork
// origin and times out.
func TestL2SafeHeadConsumesFakePoSAmsterdamL1(gt *testing.T) {
	t := devtest.SerialT(gt)
	sys := presets.NewMantleSingleChainMultiNodeWithTestSeq(t)
	require := t.Require()
	logger := t.Logger()
	ctx := t.Ctx()

	rollupCfg := sys.L2Chain.Escape().RollupConfig()
	l1Config := sys.L1Network.Escape().ChainConfig()
	require.True(sys.L2Chain.IsMantleForkActive(opforks.MantleElysium), "L2 must run with Mantle Elysium active")
	require.NotNil(l1Config.AmsterdamTime, "L1 AmsterdamTime must be configured")

	ts := sys.TestSequencer.Escape().ControlAPI(sys.L1Network.ChainID())
	cl := sys.L1Network.Escape().L1CLNode(match.FirstL1CL)

	// Pass the L1 genesis.
	sys.L1Network.WaitForBlock()

	// Stop auto-advancing L1 so every subsequent block is produced by the
	// TestSequencer's fakepos builder (the code under test).
	sys.ControlPlane.FakePoSState(cl.ID(), stack.Stop)

	// amsterdamOffset is in seconds from genesis; fakepos uses a fixed L1 block time here.
	activationBlock := amsterdamOffset / uint64(l1BlockTime/time.Second)

	driveL1 := func(tag string, iter int) {
		require.NoErrorf(ts.New(ctx, seqtypes.BuildOpts{Parent: common.Hash{}}), "%s ts.New iter=%d", tag, iter)
		require.NoErrorf(ts.Next(ctx), "%s ts.Next iter=%d (amsterdam activates at L1 #%d)", tag, iter, activationBlock)
	}

	// Phase 1: the fakepos builder must cross the Amsterdam activation block.
	startL1 := sys.L1EL.BlockRefByLabel(eth.Unsafe)

	// The builder itself must produce the activation block, otherwise a boundary
	// stall would not be exercised.
	require.Lessf(startL1.Number, activationBlock,
		"L1 is already at block %d, at/past the Amsterdam activation block %d: the auto-FakePoS CL produced the boundary, so the fakepos builder under test never has to cross it. Raise amsterdamOffset.",
		startL1.Number, activationBlock)

	target := activationBlock + 4
	logger.Info("driving L1 via TestSequencer across Amsterdam",
		"start", startL1.Number, "activationBlock", activationBlock, "target", target)

	for i := 0; sys.L1EL.BlockRefByLabel(eth.Unsafe).Number < target; i++ {
		require.Lessf(uint64(i), target+8, "fakepos builder made no progress towards L1 #%d", target)
		driveL1("phase1", i)
		logger.Info("advanced L1", "iter", i, "l1", sys.L1EL.BlockRefByLabel(eth.Unsafe).Number)
	}

	l1head := sys.L1EL.BlockRefByLabel(eth.Unsafe)
	require.GreaterOrEqual(l1head.Number, target,
		"L1 must advance past the Amsterdam boundary via the fakepos builder")

	// The block built at activationBlock is the first Amsterdam block.
	actAt := sys.L1EL.BlockRefByNumber(activationBlock)
	actParent := sys.L1EL.BlockRefByNumber(activationBlock - 1)
	require.True(l1Config.IsAmsterdam(new(big.Int).SetUint64(activationBlock), actAt.Time),
		"the block the builder produced at L1 #%d must be post-Amsterdam", activationBlock)
	require.False(l1Config.IsAmsterdam(new(big.Int).SetUint64(activationBlock-1), actParent.Time),
		"L1 #%d must be the FIRST Amsterdam block — its parent must still be pre-Amsterdam", activationBlock)
	logger.Info("fakepos builder built the Amsterdam activation block and kept going",
		"activationBlock", activationBlock, "l1head", l1head.Number)

	// Phase 2: keep producing L1 blocks until the L2 safe origin is post-Amsterdam.
	l2BlockTime := time.Duration(rollupCfg.BlockTime) * time.Second
	for i := 0; ; i++ {
		l2SafeRef := sys.L2CL.SyncStatus().SafeL2
		l1Info, _, err := sys.L1EL.EthClient().InfoAndTxsByHash(ctx, l2SafeRef.L1Origin.Hash)
		if errors.Is(err, ethereum.NotFound) {
			logger.Info("L2 safe head references an L1 origin not found by L1 EL yet, waiting...", "origin", l2SafeRef.L1Origin)
		} else {
			require.NoError(err)
			if l1Config.IsAmsterdam(new(big.Int).SetUint64(l1Info.NumberU64()), l1Info.Time()) {
				header := l1Info.Header()
				require.NotNil(header.BlockAccessListHash, "L2 safe head's post-Amsterdam L1 origin must carry an EIP-7928 BlockAccessListHash")
				require.NotNil(header.SlotNumber, "L2 safe head's post-Amsterdam L1 origin must carry an EIP-7843 SlotNumber")
				logger.Info("L2 safe head consumed a Glamsterdam L1 origin",
					"l2Safe", l2SafeRef.Number, "l1Origin", l1Info.NumberU64())
				return
			}
			logger.Info("L2 safe head still references a pre-Amsterdam L1 origin, driving L1 forward...",
				"l2Safe", l2SafeRef.Number, "l1Origin", l2SafeRef.L1Origin.Number)
		}

		// Keep the fakepos builder producing L1 blocks so batcher batches get
		// posted and derivation keeps advancing the L2 safe head.
		driveL1("phase2", i)

		select {
		case <-time.After(l2BlockTime):
		case <-ctx.Done():
			require.Fail("L2 safe head never reached a post-Amsterdam L1 origin (derivation stalled at the Glamsterdam boundary)")
		}
	}
}
