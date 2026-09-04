package reorg

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

// TestPreconfCommitmentNotDuplicatedByReplayAfterReorg is the op-acceptance-tests
// realization of TC-RG5.
//
// The test plan marks TC-RG5 as "QA does not execute" because a *single*
// sequencer cannot isolate the internal conflict it targets: when a reorg
// reverts the commitment block, the reverted tx's nonce is un-consumed on the
// new fork, so preconf replay simply re-lands it — you never get "the new chain
// already contains the tx AND replay hits an already-consumed nonce" in one
// build. That pure internal-skip path is pinned by the reth integration test
// `reorg_replay_nonce_consumed.rs`.
//
// What IS observable end-to-end here is the *contract* RG5 protects — "链上该
// hash 有且仅有一笔、节点不崩溃": after a real reorg re-lands the commitment, a
// lingering journal/replay entry for it must NOT resurrect or duplicate the tx
// in any subsequent block, and the sender must keep working. Concretely, once
// the tx re-lands and the chain advances, the sender's nonce is consumed, so
// `sync_fifo_forward_to_head` must forward past (and drop) the stale entry on
// every later build — the same consumed-nonce cleanup RG5 is about, exercised
// through the black-box chain state.
//
// This goes beyond TC-RG2, which stops at the single re-land: here we keep
// building, assert the commitment stays in exactly one canonical block, and
// assert the same sender's next nonce still lands.
func TestPreconfCommitmentNotDuplicatedByReplayAfterReorg(gt *testing.T) {
	if os.Getenv("DEVSTACK_L2EL_KIND") != "op-reth" {
		gt.Skip("preconf is only wired for op-reth; set DEVSTACK_L2EL_KIND=op-reth")
	}

	t := devtest.SerialT(gt)
	sys := presets.NewMantleSingleChainMultiNodeWithTestSeq(t)
	require := t.Require()
	logger := t.Logger()
	ctx := t.Ctx()

	// Freeze the safe head so the block carrying the commitment stays unsafe —
	// only unsafe blocks are reverted by the L1-origin rewrite below.
	sys.L2Batcher.Stop()
	gt.Cleanup(func() { sys.L2Batcher.Start() })

	ts := sys.TestSequencer.Escape().ControlAPI(sys.L1Network.ChainID())
	cl := sys.L1Network.Escape().L1CLNode(match.FirstL1CL)

	sys.L1Network.WaitForBlock()
	sys.ControlPlane.FakePoSState(cl.ID(), stack.Stop)

	// Advance L1 until the L2 unsafe head sits on an L1 origin past our start.
	startL1Block := sys.L1EL.BlockRefByLabel(eth.Unsafe)
	require.Eventually(func() bool {
		require.NoError(ts.New(ctx, seqtypes.BuildOpts{Parent: common.Hash{}}))
		require.NoError(ts.Next(ctx))
		l2Unsafe := sys.L2EL.BlockRefByLabel(eth.Unsafe)
		return l2Unsafe.Number > 0 && l2Unsafe.L1Origin.Number > startL1Block.Number
	}, 120*time.Second, 2*time.Second)

	// Submit a preconf commitment and locate the unsafe block that carried it.
	// Reuse the same funded sender for the continuity check below — PlanTransfer
	// derives the nonce from pending state, so a later send picks up nonce+1.
	alice := sys.FunderL2.NewFundedEOA(eth.OneTenthEther)
	bob := sys.Wallet.NewEOA(sys.L2EL)
	txHash := sendPreconf(t, ctx, sys.L2EL, alice, bob.Address(), eth.OneHundredthEther)

	rec := waitReceipt(t, sys.L2EL, txHash)
	l2WithTx := sys.L2EL.BlockRefByNumber(rec.BlockNumber.Uint64())
	require.NotZero(l2WithTx.L1Origin.Number,
		"commitment block must have a non-genesis L1 origin to reorg")
	logger.Info("commitment landed", "l2", l2WithTx, "l1Origin", l2WithTx.L1Origin)

	// Reorg out the commitment block's L1 origin, exactly as TC-RG2 does.
	l1Before := sys.L1EL.BlockRefByNumber(l2WithTx.L1Origin.Number)
	logger.Info("triggering L1 reorg", "l1", l1Before)
	require.NoError(ts.New(ctx, seqtypes.BuildOpts{Parent: l1Before.ParentHash}))
	require.NoError(ts.Next(ctx))
	sys.ControlPlane.FakePoSState(cl.ID(), stack.Start)

	sys.L1EL.WaitForBlockNumber(l1Before.Number)
	require.NotEqual(sys.L1EL.BlockRefByNumber(l1Before.Number).Hash, l1Before.Hash,
		"L1 must have reorged")

	sys.L2EL.ReorgTriggered(l2WithTx, 30)
	require.NotEqual(l2WithTx.Hash, sys.L2EL.BlockRefByNumber(l2WithTx.Number).Hash,
		"the original commitment block must no longer be canonical after the reorg")

	// The commitment re-lands, same hash, in exactly one canonical block.
	reRec := assertReLandedCanonical(t, sys.L2EL, txHash)
	reLandBlock := reRec.BlockNumber.Uint64()
	logger.Info("commitment re-landed after reorg", "hash", txHash, "block", reLandBlock)

	// ── RG5 core: keep building past the re-land. The sender's nonce is now
	// consumed on the canonical chain, so any lingering replay/journal entry for
	// this commitment must be forwarded past and skipped on every later build —
	// never re-applied. Advancing the chain is what makes that cleanup
	// observable as black-box chain state.
	const extraBlocks = 4
	require.Eventuallyf(func() bool {
		require.NoError(ts.New(ctx, seqtypes.BuildOpts{Parent: common.Hash{}}))
		require.NoError(ts.Next(ctx))
		return sys.L2EL.BlockRefByLabel(eth.Unsafe).Number >= reLandBlock+extraBlocks
	}, 90*time.Second, 2*time.Second,
		"unsafe head must advance >=%d blocks past the re-land (block %d)", extraBlocks, reLandBlock)

	// The commitment must still be canonical in exactly ONE block, and the SAME
	// one it re-landed in — a stale replay entry must not resurrect it into a
	// later block or duplicate it. assertReLandedCanonical rejects orphaned/
	// duplicate inclusion; the block-number equality rejects silent relocation.
	reRec2 := assertReLandedCanonical(t, sys.L2EL, txHash)
	require.Equalf(reLandBlock, reRec2.BlockNumber.Uint64(),
		"commitment must stay in its single re-landed block %d, not move/duplicate (now %d)",
		reLandBlock, reRec2.BlockNumber.Uint64())

	// Sender continuity: a fresh commitment from the same sender (next nonce)
	// must still land canonically — proving the lingering entry did not wedge
	// nonce accounting and the node is healthy after the repeated cleanup.
	txHash2 := sendPreconf(t, ctx, sys.L2EL, alice, bob.Address(), eth.OneHundredthEther)
	require.NotEqual(txHash, txHash2, "second commitment must be a distinct tx (next nonce)")
	reRec3 := assertReLandedCanonical(t, sys.L2EL, txHash2)
	logger.Info("same-sender next-nonce commitment landed after reorg cleanup",
		"hash", txHash2, "block", reRec3.BlockNumber)
}
