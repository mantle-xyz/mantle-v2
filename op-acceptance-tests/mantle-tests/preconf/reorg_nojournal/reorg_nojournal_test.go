// Package reorgnojournal covers TC-RG4: a preconf reorg on a node running with
// the commitment journal DISABLED (the degraded replay path).
//
// With the journal enabled (see the sibling `reorg` package, TC-RG2/RG3) a
// reverted commitment is identified as a reorg replay and re-injected with the
// SLA gate bypassed, so it MUST re-land. With the journal OFF that
// identification is gone: reverted commitments fall back to a plain
// FIFO-membership check and MAY be legitimately dropped. So this test is a
// CHARACTERIZATION test, not a pass/fail must-land test — per the plan:
//
//	"逐笔如实记录重新上链情况——降级路径下部分承诺可能丢失（不上链即记录，不算
//	 执行失败）；输出与 journal 开启场景（TC-RG2）的对照差异表。"
//
// What we hard-assert (invariants that must hold even on the degraded path):
//   - a real reorg happened (the commitment block was reverted) — otherwise the
//     whole measurement is vacuous;
//   - for every commitment that DOES re-land, it lands exactly once on the
//     canonical chain (no duplicate / orphaned inclusion);
//   - the node stays alive and keeps producing blocks afterwards.
//
// What we DON'T assert (and must not, per the plan): that every commitment
// re-lands. Instead we record the re-land / dropped tally and log it so the
// loss ratio can be compared against journal-on (TC-RG2) and signed off with
// the developer.
//
// The reorg is driven exactly as in the `reorg` package: an in-process test
// sequencer rewrites an L1 block on an ancestor parent, and op-node cascades the
// L1 reorg into an L2 unsafe reorg. Requires the sysgo orchestrator and op-reth.
package reorgnojournal

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/ethereum-optimism/optimism/op-devstack/devtest"
	"github.com/ethereum-optimism/optimism/op-devstack/dsl"
	"github.com/ethereum-optimism/optimism/op-devstack/presets"
	"github.com/ethereum-optimism/optimism/op-devstack/stack"
	"github.com/ethereum-optimism/optimism/op-devstack/stack/match"
	"github.com/ethereum-optimism/optimism/op-service/eth"
	"github.com/ethereum-optimism/optimism/op-service/txplan"
	"github.com/ethereum-optimism/optimism/op-test-sequencer/sequencer/seqtypes"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/types"
)

// preconfEvent mirrors mantle-reth's PreconfTxEvent serde (crates/rpc-ext).
// The wire is camelCase (PreconfTxEvent derives serde(rename_all = "camelCase")),
// so the keys are txHash / blockHeight — NOT snake_case (tagging them snake_case
// silently decodes to zero). Duplicated from the `reorg` package.
type preconfEvent struct {
	TxHash      common.Hash    `json:"txHash"`
	Status      string         `json:"status"`
	Reason      string         `json:"reason"`
	BlockHeight hexutil.Uint64 `json:"blockHeight"`
}

// TestPreconfReorgWithJournalDisabled (TC-RG4) submits several preconf
// commitments, reorgs out the block(s) that carried them on a node whose
// commitment journal is OFF, and records — per commitment — whether it re-landed
// on the new canonical chain. It does NOT require every commitment to re-land;
// it records the tally for the journal-on-vs-off contrast table.
func TestPreconfReorgWithJournalDisabled(gt *testing.T) {
	if os.Getenv("DEVSTACK_L2EL_KIND") != "op-reth" {
		gt.Skip("preconf is only wired for op-reth; set DEVSTACK_L2EL_KIND=op-reth")
	}

	t := devtest.SerialT(gt)
	sys := presets.NewMantleSingleChainMultiNodeWithTestSeq(t)
	require := t.Require()
	logger := t.Logger()
	ctx := t.Ctx()

	// Freeze the safe head so the blocks carrying the commitments stay unsafe —
	// only unsafe blocks are reverted by the L1-origin rewrite below.
	sys.L2Batcher.Stop()
	gt.Cleanup(func() { sys.L2Batcher.Start() })

	ts := sys.TestSequencer.Escape().ControlAPI(sys.L1Network.ChainID())
	cl := sys.L1Network.Escape().L1CLNode(match.FirstL1CL)

	sys.L1Network.WaitForBlock()

	// Fund all senders FIRST — while L1/L2 still advance freely (before we freeze L1
	// below), so all 4 funding txs reliably get mined; and on an early L1 origin the
	// later reorg will NOT revert (else they are unfunded at the new tip and the
	// re-injected txs are rejected for insufficient funds, which would corrupt the
	// journal-off drop measurement below). A fresh EOA per commitment keeps nonces
	// independent.
	const numCommitments = 4
	// Fund sequentially (NOT NewFundedEOAs, which funds concurrently and races the
	// single faucet account's nonce — flaky for count>1).
	alices := make([]*dsl.EOA, numCommitments)
	for i := range alices {
		alices[i] = sys.FunderL2.NewFundedEOA(eth.OneTenthEther)
	}
	bob := sys.Wallet.NewEOA(sys.L2EL)
	fundOrigin := sys.L2EL.BlockRefByLabel(eth.Unsafe).L1Origin.Number

	sys.ControlPlane.FakePoSState(cl.ID(), stack.Stop)

	// Advance L1 until the L2 unsafe head sits on an L1 origin past our start.
	startL1Block := sys.L1EL.BlockRefByLabel(eth.Unsafe)
	require.Eventually(func() bool {
		require.NoError(ts.New(ctx, seqtypes.BuildOpts{Parent: common.Hash{}}))
		require.NoError(ts.Next(ctx))
		l2Unsafe := sys.L2EL.BlockRefByLabel(eth.Unsafe)
		return l2Unsafe.Number > 0 && l2Unsafe.L1Origin.Number > startL1Block.Number
	}, 120*time.Second, 2*time.Second)

	// Submit several commitments back-to-back so they land in the current unsafe
	// block (nominal TC-RG4 shape: "多笔交易 + 1 块 revert"). We do NOT advance L1
	// between them.
	type commitment struct {
		hash  common.Hash
		block eth.L2BlockRef
	}
	commitments := make([]commitment, 0, numCommitments)
	for i := range numCommitments {
		h := sendPreconf(t, ctx, sys.L2EL, alices[i], bob.Address(), eth.OneHundredthEther)
		rec := waitReceipt(t, sys.L2EL, h)
		blk := sys.L2EL.BlockRefByNumber(rec.BlockNumber.Uint64())
		require.NotZero(blk.L1Origin.Number, "commitment block must have a non-genesis L1 origin")
		commitments = append(commitments, commitment{hash: h, block: blk})
		logger.Info("commitment landed", "i", i, "hash", h, "l2", blk.Number, "l1Origin", blk.L1Origin.Number)
	}

	// Earliest commitment block — reverting its L1 origin reverts it and every
	// descendant, so the whole commitment set goes even if they span >1 block.
	earliest := commitments[0].block
	for _, c := range commitments {
		if c.block.Number < earliest.Number {
			earliest = c.block
		}
	}

	require.Greaterf(earliest.L1Origin.Number, fundOrigin,
		"earliest commitment origin %d must exceed funding origin %d", earliest.L1Origin.Number, fundOrigin)

	// Freeze the L2 head so the revert stays shallow (else deep-reorg churn would
	// corrupt the journal-off re-land/drop measurement); resume once the L1 reorg
	// has propagated past the old head so op-node applies the reset promptly.
	oldL1Head := sys.L1EL.BlockRefByLabel(eth.Unsafe).Number
	sys.L2CL.StopSequencer()

	// Trigger the reorg: rebuild the earliest commitment block's L1 origin on its
	// parent, then resume so the alternate L1 chain wins.
	l1Before := sys.L1EL.BlockRefByNumber(earliest.L1Origin.Number)
	logger.Info("triggering L1 reorg (journal OFF)", "l1", l1Before, "revertFromL2", earliest.Number)
	require.NoError(ts.New(ctx, seqtypes.BuildOpts{Parent: l1Before.ParentHash}))
	require.NoError(ts.Next(ctx))
	sys.ControlPlane.FakePoSState(cl.ID(), stack.Start)

	require.Eventuallyf(func() bool {
		h := sys.L1EL.BlockRefByLabel(eth.Unsafe)
		return h.Number > oldL1Head &&
			sys.L1EL.BlockRefByNumber(l1Before.Number).Hash != l1Before.Hash
	}, 120*time.Second, 2*time.Second, "L1 reorg must propagate past old head %d", oldL1Head)
	require.NotEqual(sys.L1EL.BlockRefByNumber(l1Before.Number).Hash, l1Before.Hash, "L1 must have reorged")
	sys.L2CL.StartSequencer()

	// Precondition: the earliest commitment block must actually be reverted,
	// otherwise the re-land measurement below is vacuous.
	waitReverted(t, sys.L2EL, earliest, 30)
	logger.Info("L2 reorg triggered (journal OFF)", "revertedFrom", earliest.Number)

	// Characterize: per commitment, record re-landed vs dropped. Do NOT fail on a
	// drop — on the degraded path that is a legitimate (if unfortunate) outcome.
	// For every one that DOES re-land, assert canonical uniqueness (no dup).
	var reLanded, dropped int
	for _, c := range commitments {
		rec := pollReceipt(t, sys.L2EL, c.hash, 15) // ~30s tolerant poll; nil = not re-landed
		if rec == nil {
			dropped++
			logger.Warn("commitment DROPPED after reorg (journal off)", "hash", c.hash, "origBlock", c.block.Number)
			continue
		}
		reLanded++
		canon := sys.L2EL.BlockRefByNumber(rec.BlockNumber.Uint64())
		require.Equalf(rec.BlockHash, canon.Hash,
			"re-landed tx %s must sit in the canonical chain (no orphaned/duplicate inclusion)", c.hash)
		logger.Info("commitment re-landed after reorg (journal off)", "hash", c.hash,
			"origBlock", c.block.Number, "newBlock", rec.BlockNumber)
	}

	// The contrast row for the TC-RG4 vs TC-RG2 diff table. Compare re-land ratio
	// against the journal-on run and sign the acceptable loss off with the dev.
	// TODO(TC-RG4): emit this as a machine-readable artifact (e.g. testlog / a
	//   JSON line) so the journal-on vs journal-off table can be assembled in CI
	//   rather than eyeballed from logs. Then decide with the developer whether a
	//   max-acceptable-drop threshold should become a hard assertion here.
	logger.Info("TC-RG4 result (journal OFF degraded path)",
		"submitted", numCommitments, "reLanded", reLanded, "dropped", dropped)

	// Liveness: whatever the replay outcome, the node must not have wedged.
	beforeTip := sys.L2EL.BlockRefByLabel(eth.Unsafe).Number
	require.Eventuallyf(func() bool {
		require.NoError(ts.New(ctx, seqtypes.BuildOpts{Parent: common.Hash{}}))
		require.NoError(ts.Next(ctx))
		return sys.L2EL.BlockRefByLabel(eth.Unsafe).Number > beforeTip
	}, 60*time.Second, 2*time.Second, "node must keep producing blocks after the reorg")
}

// --- helpers (duplicated from the `reorg` package; keep in sync) ------------

// sendPreconf submits a native-transfer preconf tx and asserts the success
// (must-land) commitment. Returns the tx hash.
func sendPreconf(t devtest.T, ctx context.Context, el *dsl.L2ELNode, from *dsl.EOA, to common.Address, amount eth.ETH) common.Hash {
	require := t.Require()
	ptx := txplan.NewPlannedTx(from.PlanTransfer(to, amount))
	signed, err := ptx.Signed.Eval(ctx)
	require.NoError(err, "must sign preconf tx")
	raw, err := signed.MarshalBinary()
	require.NoError(err, "must RLP-encode signed tx")
	txHash := signed.Hash()

	var ev preconfEvent
	require.NoError(
		el.Escape().L2EthClient().RPC().CallContext(ctx, &ev,
			"eth_sendRawTransactionWithPreconf", hexutil.Encode(raw)),
		"preconf submission must not error")
	require.Equalf("success", ev.Status,
		"preconf must return success (the must-land commitment); reason=%q", ev.Reason)
	require.Equal(txHash, ev.TxHash, "returned hash must match the submitted tx")
	return txHash
}

// waitReceipt polls until a receipt for hash is available (used for the INITIAL
// landing, which must succeed).
func waitReceipt(t devtest.T, el *dsl.L2ELNode, hash common.Hash) *types.Receipt {
	var rec *types.Receipt
	t.Require().Eventuallyf(func() bool {
		r, err := el.Escape().EthClient().TransactionReceipt(t.Ctx(), hash)
		if err != nil || r == nil {
			return false
		}
		rec = r
		return true
	}, 60*time.Second, 2*time.Second, "awaiting receipt for %s", hash)
	return rec
}

// pollReceipt is the TOLERANT variant used AFTER the reorg: it returns the
// receipt if the tx re-lands within `attempts` (2s each), or nil if it never
// does. Unlike waitReceipt it does not fail the test on absence — a dropped
// commitment is an expected outcome on the journal-off degraded path.
func pollReceipt(t devtest.T, el *dsl.L2ELNode, hash common.Hash, attempts int) *types.Receipt {
	for range attempts {
		r, err := el.Escape().EthClient().TransactionReceipt(t.Ctx(), hash)
		if err == nil && r != nil {
			return r
		}
		time.Sleep(2 * time.Second)
	}
	return nil
}

// waitReverted blocks until ref is no longer canonical at its height (reverted).
func waitReverted(t devtest.T, el *dsl.L2ELNode, ref eth.L2BlockRef, attempts int) {
	t.Require().Eventuallyf(func() bool {
		cur, err := el.Escape().EthClient().BlockRefByNumber(t.Ctx(), ref.Number)
		if err != nil {
			return true // chain transiently shorter than the reverted height
		}
		return cur.Hash != ref.Hash
	}, time.Duration(attempts)*2*time.Second, 2*time.Second,
		"awaiting revert of L2 block %d (%s)", ref.Number, ref.Hash)
}
