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

// TestL2SafeHeadConsumesFakePoSAmsterdamL1 drives the L1 chain manually via the
// TestSequencer's fakepos builder (op-test-sequencer .../builders/fakepos) across
// the Amsterdam (Glamsterdam EL) fork boundary, then proves the Mantle L2
// genuinely consumes that Glamsterdam L1: its safe head advances to an L2 block
// whose L1 origin is a real post-Amsterdam L1 block.
//
// Phase 1 — the fakepos builder must cross the boundary. Before the
// engine-version selection + Amsterdam payload-attribute fix in that builder,
// ts.Next stalled at the first post-Amsterdam block: geth's forkchoiceUpdatedV4
// rejects a build request whose attributes omit slotNumber / targetGasLimit
// (STATUS_INVALID, nil PayloadID). We stop the L1 CL's auto-advance so the
// fakepos builder is the sole L1 block producer, drive past the activation block,
// and assert the L1 head advanced (a builder stall here fails immediately).
//
// Phase 2 — the L2 must consume the Glamsterdam L1. With auto-advance stopped, we
// keep driving L1 one block at a time (each driven block is built via the engine
// API and therefore carries the op-batcher's L1 batch transactions from the
// txpool) until the L2 safe head's L1 origin is a genuinely post-Amsterdam L1
// block. That origin header must carry the Amsterdam header fields (EIP-7928
// BlockAccessListHash + EIP-7843 SlotNumber), proving op-node derived a safe L2
// block from real Glamsterdam L1 data rather than stalling at the boundary.
//
// Discriminating:
//   - a fakepos-builder stall at the Amsterdam boundary fails Phase 1;
//   - an op-node unable to consume Amsterdam L1 headers for derivation leaves the
//     L2 safe head stuck at a pre-Amsterdam origin, so Phase 2 never completes and
//     the test fails on context timeout.
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

	driveL1 := func(tag string, iter int) {
		require.NoError(ts.New(ctx, seqtypes.BuildOpts{Parent: common.Hash{}}), "%s ts.New iter=%d", tag, iter)
		require.NoError(ts.Next(ctx), "%s ts.Next iter=%d (amsterdam activates at L1 #%d)", tag, iter, iter)
	}

	// Phase 1: drive L1 across the Amsterdam activation block via the fakepos
	// builder and assert the builder does not stall at the boundary.
	startL1 := sys.L1EL.BlockRefByLabel(eth.Unsafe)
	target := startL1.Number + amsterdamOffset + 4
	logger.Info("driving L1 via TestSequencer across Amsterdam",
		"start", startL1.Number, "amsterdamOffset", amsterdamOffset, "target", target)

	for i := 0; uint64(i) <= amsterdamOffset+4; i++ {
		driveL1("phase1", i)
		l1head := sys.L1EL.BlockRefByLabel(eth.Unsafe)
		logger.Info("advanced L1", "iter", i, "l1", l1head.Number)
	}

	l1head := sys.L1EL.BlockRefByLabel(eth.Unsafe)
	require.GreaterOrEqual(l1head.Number, target,
		"L1 must advance past the Amsterdam boundary via the fakepos builder")
	logger.Info("L1 crossed Amsterdam via TestSequencer fakepos builder", "l1head", l1head.Number)

	// Phase 2: prove the L2 consumes the Glamsterdam L1. Keep driving L1 one block
	// at a time so batcher transactions land and derivation can advance, until the
	// L2 safe head's L1 origin is a genuinely post-Amsterdam L1 block.
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
