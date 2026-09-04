// Diagnostic (step 1): isolate the batcher-stopped / unsafe-only-reorg variable
// from the ordering tests' re-land failure.
//
// The ordering tests stop the batcher (to keep the commitment block UNSAFE so an
// L1-origin rewrite can revert it). In those runs neither the preconf tx nor a
// plain normal tx re-lands after the reorg — reth's stock pool re-injection never
// fires (only the `reorg_drift` observe-warn). This test changes exactly ONE
// thing: it leaves the batcher RUNNING and waits for the tx's block to become
// SAFE before reverting its L1 origin, i.e. a SAFE reorg (op-node re-derives from
// L1) instead of a pure unsafe reorg.
//
//   - If the plain tx re-lands here → reth's re-injection works on safe reorgs;
//     the ordering-test failure is specific to the batcher-stopped/unsafe path
//     (a harness artifact, acceptable — the tests just need a safe-reorg shape).
//   - If it still does NOT re-land → reth's reorg re-injection is broken
//     regardless (a real must-land-across-reorg gap), independent of preconf.
//
// It uses only a NON-whitelisted normal tx (pure reth pool path), so preconf is
// not involved in the re-land — this isolates reth's native pool re-injection.
package reorgordering

import (
	"os"
	"testing"
	"time"

	"github.com/ethereum-optimism/optimism/op-devstack/devtest"
	"github.com/ethereum-optimism/optimism/op-devstack/presets"
	"github.com/ethereum-optimism/optimism/op-devstack/stack"
	"github.com/ethereum-optimism/optimism/op-devstack/stack/match"
	"github.com/ethereum-optimism/optimism/op-service/eth"
	"github.com/ethereum-optimism/optimism/op-test-sequencer/sequencer/seqtypes"
	"github.com/ethereum/go-ethereum/common"
)

// TestReinjectDiagNormalTxSafeReorg submits a single plain normal tx, waits for
// its block to become SAFE (batcher left running), reorgs that block's L1 origin,
// and asserts the tx re-lands. Pure reth pool path — no preconf involvement.
func TestReinjectDiagNormalTxSafeReorg(gt *testing.T) {
	if os.Getenv("DEVSTACK_L2EL_KIND") != "op-reth" {
		gt.Skip("preconf is only wired for op-reth; set DEVSTACK_L2EL_KIND=op-reth")
	}

	t := devtest.SerialT(gt)
	sys := presets.NewMantleSingleChainMultiNodeWithTestSeq(t)
	require := t.Require()
	logger := t.Logger()
	ctx := t.Ctx()

	// NOTE: batcher is deliberately NOT stopped — that is the isolated variable.

	ts := sys.TestSequencer.Escape().ControlAPI(sys.L1Network.ChainID())
	cl := sys.L1Network.Escape().L1CLNode(match.FirstL1CL)

	sys.L1Network.WaitForBlock()
	sys.ControlPlane.FakePoSState(cl.ID(), stack.Stop)

	// Advance L1 until the L2 SAFE head sits on an L1 origin past our start.
	startL1Block := sys.L1EL.BlockRefByLabel(eth.Unsafe)
	require.Eventually(func() bool {
		require.NoError(ts.New(ctx, seqtypes.BuildOpts{Parent: common.Hash{}}))
		require.NoError(ts.Next(ctx))
		l2Safe := sys.L2EL.BlockRefByLabel(eth.Safe)
		return l2Safe.Number > 0 && l2Safe.L1Origin.Number > startL1Block.Number
	}, 120*time.Second, 2*time.Second)

	// Submit a plain normal tx (non-whitelisted recipient → pure pool path).
	alice := sys.FunderL2.NewFundedEOA(eth.OneTenthEther)
	normTo := common.HexToAddress(normalRecipient)
	txN := submitNormal(t, ctx, sys.L2EL, alice, normTo, eth.OneHundredthEther, highTipWei)
	recN := waitReceipt(t, sys.L2EL, txN)
	blkN := sys.L2EL.BlockRefByNumber(recN.BlockNumber.Uint64())
	require.NotZero(blkN.L1Origin.Number, "tx block must have a non-genesis L1 origin")
	logger.Info("normal tx landed (unsafe)", "block", blkN.Number, "l1Origin", blkN.L1Origin.Number)

	// Keep advancing L1 (so the batcher can post + op-node can derive) until the
	// tx's block becomes SAFE — only then is reverting its L1 origin a safe reorg.
	require.Eventuallyf(func() bool {
		require.NoError(ts.New(ctx, seqtypes.BuildOpts{Parent: common.Hash{}}))
		require.NoError(ts.Next(ctx))
		return sys.L2EL.BlockRefByLabel(eth.Safe).Number >= blkN.Number
	}, 180*time.Second, 2*time.Second, "tx block %d must become safe", blkN.Number)
	logger.Info("tx block is now safe", "block", blkN.Number,
		"safeHead", sys.L2EL.BlockRefByLabel(eth.Safe).Number)

	// Reorg the tx block's L1 origin — a SAFE reorg (op-node re-derives).
	l1Before := sys.L1EL.BlockRefByNumber(blkN.L1Origin.Number)
	logger.Info("triggering L1 (safe) reorg", "l1", l1Before, "revertFromL2", blkN.Number)
	require.NoError(ts.New(ctx, seqtypes.BuildOpts{Parent: l1Before.ParentHash}))
	require.NoError(ts.Next(ctx))
	sys.ControlPlane.FakePoSState(cl.ID(), stack.Start)

	sys.L1EL.WaitForBlockNumber(l1Before.Number)
	require.NotEqual(sys.L1EL.BlockRefByNumber(l1Before.Number).Hash, l1Before.Hash, "L1 must have reorged")

	waitReverted(t, sys.L2EL, blkN, 30)
	require.NotEqual(blkN.Hash, sys.L2EL.BlockRefByNumber(blkN.Number).Hash,
		"the original tx block must no longer be canonical after the reorg")
	logger.Info("L2 safe reorg triggered; tx block reverted", "reverted", blkN.Number)

	// THE ISOLATION ASSERTION: does the reverted plain tx re-land? (reth pool
	// re-injection on a safe reorg.)
	reN := assertReLandedCanonical(t, sys.L2EL, txN)
	logger.Info("DIAG RESULT: normal tx re-landed after SAFE reorg",
		"hash", txN, "block", reN.BlockNumber, "idx", reN.TransactionIndex)
}
