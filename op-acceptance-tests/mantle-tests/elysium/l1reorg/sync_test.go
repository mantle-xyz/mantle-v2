package l1reorg

import (
	"testing"

	"github.com/ethereum-optimism/optimism/op-devstack/devtest"
	"github.com/ethereum-optimism/optimism/op-devstack/presets"
	"github.com/ethereum-optimism/optimism/op-devstack/stack"
	"github.com/ethereum-optimism/optimism/op-devstack/stack/match"
	"github.com/ethereum-optimism/optimism/op-service/eth"
	"github.com/ethereum-optimism/optimism/op-test-sequencer/sequencer/seqtypes"
	"github.com/ethereum/go-ethereum/common"
)

// TestL1FakePoS_AcrossAmsterdam drives the L1 chain manually via the
// TestSequencer's fakepos builder (op-test-sequencer .../builders/fakepos) across
// the Amsterdam fork boundary.
//
// Before the engine-version selection + Amsterdam payload-attribute fix in that
// builder, ts.Next stalled at the first post-Amsterdam block: geth's
// forkchoiceUpdatedV4 rejects a build request whose attributes omit slotNumber /
// targetGasLimit (STATUS_INVALID, nil PayloadID). This asserts the builder now
// advances cleanly past the activation block.
func TestL1FakePoS_AcrossAmsterdam(gt *testing.T) {
	t := devtest.SerialT(gt)
	sys := presets.NewMantleSingleChainMultiNodeWithTestSeq(t)
	require := t.Require()
	logger := t.Logger()
	ctx := t.Ctx()

	ts := sys.TestSequencer.Escape().ControlAPI(sys.L1Network.ChainID())
	cl := sys.L1Network.Escape().L1CLNode(match.FirstL1CL)

	// Pass the L1 genesis.
	sys.L1Network.WaitForBlock()

	// Stop auto-advancing L1 so every subsequent block is produced by the
	// TestSequencer's fakepos builder (the code under test).
	sys.ControlPlane.FakePoSState(cl.ID(), stack.Stop)

	startL1 := sys.L1EL.BlockRefByLabel(eth.Unsafe)
	target := startL1.Number + amsterdamOffset + 4
	logger.Info("driving L1 via TestSequencer across Amsterdam",
		"start", startL1.Number, "amsterdamOffset", amsterdamOffset, "target", target)

	for i := 0; uint64(i) <= amsterdamOffset+4; i++ {
		require.NoError(ts.New(ctx, seqtypes.BuildOpts{Parent: common.Hash{}}), "ts.New iter=%d", i)
		require.NoError(ts.Next(ctx), "ts.Next iter=%d (amsterdam activates at L1 #%d)", i, startL1.Number+amsterdamOffset)
		l1head := sys.L1EL.BlockRefByLabel(eth.Unsafe)
		logger.Info("advanced L1", "iter", i, "l1", l1head.Number)
	}

	l1head := sys.L1EL.BlockRefByLabel(eth.Unsafe)
	require.GreaterOrEqual(l1head.Number, target,
		"L1 must advance past the Amsterdam boundary via the fakepos builder")
	logger.Info("L1 crossed Amsterdam via TestSequencer fakepos builder", "l1head", l1head.Number)
}
