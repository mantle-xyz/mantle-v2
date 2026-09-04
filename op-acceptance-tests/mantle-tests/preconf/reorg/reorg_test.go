// Package reorg verifies the Mantle preconf must-land guarantee across a
// chain reorg.
//
// A preconf tx that returns `success` is a must-land commitment: the client
// was promised the tx will end up on chain. If a reorg reverts the (unsafe)
// block that carried it, the commitment must still hold — the tx has to
// re-land on the new canonical chain (same hash, exactly once). Internally
// this relies on the reverted tx being re-injected into the pool and
// re-applied as a replayed commitment, but these tests only assert the
// black-box outcome.
//
// A real reorg is driven the same way as the sync/elsync reorg tests: with an
// in-process test sequencer we rewrite an L1 block on an ancestor parent, and
// op-node cascades the L1 reorg into an L2 unsafe reorg that reverts the block
// carrying the commitment. This requires the in-process sysgo orchestrator and
// op-reth as the L2 EL (preconf is only wired for op-reth).
//
// Coverage maps to the "Reorg 专项 (TC-RG)" cases of the preconf test plan:
//
//   - TestPreconfCommitmentSurvivesReorg           — TC-RG2 (1-block shallow reorg)
//   - TestPreconfCommitmentsSurviveMultiBlockReorg  — TC-RG3 (2~3 block shallow reorg,
//     multi-commitment list; no commitment dropped)
//   - TestPreconfCommitmentSurvivesDroppedPayload   — TC-RG1 (carryover; skipped, see
//     its doc comment for the harness limitation)
//
// The `reorg_drift` warn log that TC-RG2 also mentions is emitted by the
// op-reth subprocess (Rust tracing); asserting it from the Go harness is
// brittle, so it is left to reth's own integration tests — here we assert only
// the black-box chain state (re-land + no duplicate inclusion).
package reorg

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

// preconfEvent decodes the subset of `eth_sendRawTransactionWithPreconf`'s
// result we assert on. Field names match mantle-reth's PreconfTxEvent serde
// (crates/rpc-ext/src/lib.rs); the omitted `receipt` field is ignored.
// The wire is camelCase: mantle-reth's PreconfTxEvent derives
// `#[serde(rename_all = "camelCase")]` (crates/rpc-ext/src/lib.rs), so the
// JSON keys are `txHash` / `blockHeight`, not snake_case. Tagging these
// `tx_hash` / `block_height` silently decodes them to zero.
type preconfEvent struct {
	TxHash      common.Hash    `json:"txHash"`
	Status      string         `json:"status"` // "success" | "failed" | "timeout" | "waiting"
	Reason      string         `json:"reason"`
	BlockHeight hexutil.Uint64 `json:"blockHeight"`
}

// TestPreconfCommitmentSurvivesReorg commits a preconf tx into an unsafe
// block, reorgs that block out, and asserts the tx re-lands on the new chain.
func TestPreconfCommitmentSurvivesReorg(gt *testing.T) {
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

	// Fund the sender FIRST — while L1/L2 still advance freely (before we freeze L1
	// below), so the funding tx reliably gets mined; and on an early L1 origin that
	// the later reorg will NOT revert. Otherwise the sender is unfunded at the new
	// tip and reth (correctly) rejects the re-injected tx for insufficient funds, so
	// it never re-lands.
	alice := sys.FunderL2.NewFundedEOA(eth.OneTenthEther)
	bob := sys.Wallet.NewEOA(sys.L2EL)
	fundOrigin := sys.L2EL.BlockRefByLabel(eth.Unsafe).L1Origin.Number

	sys.ControlPlane.FakePoSState(cl.ID(), stack.Stop)

	// Advance L1 one block at a time until the L2 unsafe head sits on an L1
	// origin past our starting point — that origin is what we later reorg.
	startL1Block := sys.L1EL.BlockRefByLabel(eth.Unsafe)
	require.Eventually(func() bool {
		require.NoError(ts.New(ctx, seqtypes.BuildOpts{Parent: common.Hash{}}))
		require.NoError(ts.Next(ctx))
		l2Unsafe := sys.L2EL.BlockRefByLabel(eth.Unsafe)
		return l2Unsafe.Number > 0 && l2Unsafe.L1Origin.Number > startL1Block.Number
	}, 120*time.Second, 2*time.Second)

	// Submit a preconf tx. It must be accepted (the must-land commitment) and
	// land in the current unsafe block.
	txHash := sendPreconf(t, ctx, sys.L2EL, alice, bob.Address(), eth.OneHundredthEther)

	// Locate the unsafe block that carried the committed tx.
	rec := waitReceipt(t, sys.L2EL, txHash)
	l2WithTx := sys.L2EL.BlockRefByNumber(rec.BlockNumber.Uint64())
	require.NotZero(l2WithTx.L1Origin.Number,
		"commitment block must have a non-genesis L1 origin to reorg")
	logger.Info("commitment landed", "l2", l2WithTx, "l1Origin", l2WithTx.L1Origin)

	// The reorg origin must be strictly above the funding origin, else the sender
	// loses its funding in the reorg (see the funding comment above).
	require.Greaterf(l2WithTx.L1Origin.Number, fundOrigin,
		"commitment origin %d must exceed funding origin %d", l2WithTx.L1Origin.Number, fundOrigin)

	// Reorg out the L1 origin of the commitment block: rebuild that L1 height on
	// its parent, then resume so the alternate L1 chain wins. op-node detects the
	// L1 reorg and cascades it into an L2 unsafe reorg that reverts the commitment.
	//
	// Freeze the L2 head first so it does NOT keep sequencing (~2s/block) for the
	// ~20s the L1 rebuild takes — that would pile blocks onto the commitment origin
	// and produce a DEEP reorg. Resume only once the L1 reorg has propagated past
	// the old head, so op-node applies the reset promptly.
	oldL1Head := sys.L1EL.BlockRefByLabel(eth.Unsafe).Number
	sys.L2CL.StopSequencer()

	l1Before := sys.L1EL.BlockRefByNumber(l2WithTx.L1Origin.Number)
	logger.Info("triggering L1 reorg", "l1", l1Before)
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

	// Depth-tolerant revert wait: the L1-origin reorg may span >1 unsafe block, so
	// the strict parent-preserving ReorgTriggered check is not applicable here.
	waitReverted(t, sys.L2EL, l2WithTx, 30)
	// The block that originally carried the commitment must be gone from the
	// canonical chain — otherwise there was no real reorg and the re-land
	// assertion below would be vacuous.
	require.NotEqual(l2WithTx.Hash, sys.L2EL.BlockRefByNumber(l2WithTx.Number).Hash,
		"the original commitment block must no longer be canonical after the reorg")
	logger.Info("L2 reorg triggered; commitment block reverted", "reverted", l2WithTx)

	// SLA: the committed tx must re-land on the new canonical chain, same hash,
	// exactly once. Poll for its receipt to reappear, then confirm the block it
	// now sits in is canonical (a tx hash can only be canonical in one block).
	reRec := assertReLandedCanonical(t, sys.L2EL, txHash)
	logger.Info("commitment re-landed after reorg", "hash", txHash, "block", reRec.BlockNumber)
}

// TestPreconfCommitmentsSurviveMultiBlockReorg (TC-RG3) commits several preconf
// txs spread across a few consecutive unsafe blocks, reorgs all of those blocks
// out at once, and asserts every commitment re-lands on the new chain — none
// dropped, none duplicated. This exercises the deeper (2~3 block) reorg path
// that the single-block TC-RG2 test does not.
func TestPreconfCommitmentsSurviveMultiBlockReorg(gt *testing.T) {
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
	// later reorg will NOT revert (else they are unfunded at the new tip and reth
	// rejects the re-injected txs for insufficient funds). A fresh EOA per
	// commitment keeps nonces independent.
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

	// Submit several preconf commitments, advancing L1 (and hence the L2 unsafe
	// head) between each so they land in distinct, consecutive unsafe blocks.
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

		// Advance L1 by one and wait for the L2 unsafe head to move past this
		// block, so the next commitment lands strictly later.
		if i < numCommitments-1 {
			require.NoError(ts.New(ctx, seqtypes.BuildOpts{Parent: common.Hash{}}))
			require.NoError(ts.Next(ctx))
			require.Eventuallyf(func() bool {
				return sys.L2EL.BlockRefByLabel(eth.Unsafe).Number > blk.Number
			}, 60*time.Second, 2*time.Second, "awaiting unsafe head to advance past block %d", blk.Number)
		}
	}

	// Identify the shallowest (earliest) and deepest commitment blocks.
	earliest, deepest := commitments[0].block, commitments[0].block
	for _, c := range commitments {
		if c.block.Number < earliest.Number {
			earliest = c.block
		}
		if c.block.Number > deepest.Number {
			deepest = c.block
		}
	}
	require.Greater(deepest.Number, earliest.Number,
		"commitments must span >=2 unsafe blocks to exercise a multi-block reorg")

	// Reorg out the earliest commitment block's L1 origin. Because every later
	// commitment block was built on an L1 origin >= that height, reverting it
	// reverts the earliest block and all its descendants — the whole commitment
	// set in one shot.
	require.Greaterf(earliest.L1Origin.Number, fundOrigin,
		"earliest commitment origin %d must exceed funding origin %d", earliest.L1Origin.Number, fundOrigin)

	// Freeze the L2 head so the revert stays bounded to the commitment blocks (see
	// TC-RG2); resume once the L1 reorg has propagated past the old head so op-node
	// applies the reset promptly.
	oldL1Head := sys.L1EL.BlockRefByLabel(eth.Unsafe).Number
	sys.L2CL.StopSequencer()

	l1Before := sys.L1EL.BlockRefByNumber(earliest.L1Origin.Number)
	logger.Info("triggering multi-block L1 reorg", "l1", l1Before,
		"revertFromL2", earliest.Number, "throughL2", deepest.Number)
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

	// Wait for the earliest (shallowest) commitment block to be reverted. Its
	// descendants — every other commitment block — are then necessarily reverted
	// too (a block cannot stay canonical if its ancestor is gone).
	waitReverted(t, sys.L2EL, earliest, 30)
	logger.Info("L2 multi-block reorg triggered", "reverted from", earliest.Number)

	// SLA: every commitment must re-land on the new canonical chain, same hash,
	// exactly once — none dropped, none duplicated.
	for _, c := range commitments {
		reRec := assertReLandedCanonical(t, sys.L2EL, c.hash)
		logger.Info("commitment re-landed after multi-block reorg",
			"hash", c.hash, "origBlock", c.block.Number, "newBlock", reRec.BlockNumber)
	}
}

// TestPreconfCommitmentSurvivesDroppedPayload (TC-RG1) asserts the carryover
// guarantee: a preconf commitment whose payload is built but then discarded
// *without a reorg* (the payload is never committed) must still re-land on the
// next build via FIFO carryover — the same hash, exactly once.
//
// Unlike TC-RG2/RG3, this is NOT a reorg: no committed block is reverted. The
// mechanism under test (reth's in-memory `replay_fifo_carryover` Success-branch,
// `reset_success_to_waiting`) fires only when a Success FIFO entry survives an
// *uncommitted* payload — a state op-node's normal auto-sequencing never leaves
// behind (it commits every block it builds). So we take L2 sequencing away from
// op-node and drive it through the test sequencer's L2 control API, entirely
// over the CL path (op-node opstack API -> engine API):
//
//  1. Open a build on parent H, commit a preconf tx into it (Success = must-land).
//  2. Cancel the build — abandon it without sealing (no getPayload) or committing.
//     Head stays at H; the tx is not on chain; the Success FIFO entry survives
//     (its nonce is un-consumed on H).
//  3. Open a *fresh* build on the SAME parent H. For reth to run a new
//     build_payload (and thus the carryover pass) rather than return the cached
//     build#1 job, the attributes must differ, so build#2 uses the next L1 origin
//     O2 (distinct L1-info tx -> distinct payloadId). Carryover re-dispatches the
//     surviving Success entry into build#2.
//
// The `landed.L1Origin == O2` assertion is what makes this non-vacuous: it proves
// the tx re-landed in a genuinely new build (build#2), not a cached build#1.
func TestPreconfCommitmentSurvivesDroppedPayload(gt *testing.T) {
	if os.Getenv("DEVSTACK_L2EL_KIND") != "op-reth" {
		gt.Skip("preconf is only wired for op-reth; set DEVSTACK_L2EL_KIND=op-reth")
	}

	t := devtest.SerialT(gt)
	sys := presets.NewMantleSingleChainMultiNodeWithTestSeq(t)
	require := t.Require()
	logger := t.Logger()
	ctx := t.Ctx()

	// Fund the preconf sender BEFORE we stop op-node's sequencer — funding needs
	// a block to be mined, which only op-node produces while it is sequencing.
	alice := sys.FunderL2.NewFundedEOA(eth.OneTenthEther)
	bob := sys.Wallet.NewEOA(sys.L2EL)

	// Let op-node advance the L2 chain onto a non-genesis L1 origin before we take
	// over — build#2 needs a *next* origin O2, which only exists once the head's
	// origin is past genesis and the L1 head runs ahead of it.
	require.Eventuallyf(func() bool {
		h := sys.L2EL.BlockRefByLabel(eth.Unsafe)
		return h.L1Origin.Number > 0 && sys.L1EL.BlockRefByLabel(eth.Unsafe).Number > h.L1Origin.Number
	}, 90*time.Second, 1*time.Second, "L2 head must reach a non-genesis L1 origin with L1 ahead")

	// Take L2 sequencing away from op-node so we can build-then-drop deterministically.
	sys.L2CL.StopSequencer()
	gt.Cleanup(func() { sys.L2CL.StartSequencer() })

	tsL2 := sys.TestSequencer.Escape().ControlAPI(sys.L2Chain.ChainID())
	require.NotNil(tsL2, "L2 control API must be wired")

	// A just-stopped op-node may still have an in-flight block committing; wait for
	// the head to settle before we treat it as the parent.
	head0 := waitL2HeadSettled(t, sys.L2EL)
	o1 := sys.L1EL.BlockRefByNumber(head0.L1Origin.Number)
	require.NotZero(o1.Number, "parent must have a non-genesis L1 origin")
	require.Greater(sys.L1EL.BlockRefByLabel(eth.Unsafe).Number, o1.Number,
		"L1 head must run ahead of the L2 origin so the next origin O2 exists")
	o2 := sys.L1EL.BlockRefByNumber(o1.Number + 1)
	o1Hash, o2Hash := o1.Hash, o2.Hash

	// Advance L2 (pinned to origin O1, so the origin does not auto-advance) until
	// the head's time reaches O2's time — only then is O2 a valid origin for the
	// next block (L2 time must be >= its L1 origin time).
	require.Eventuallyf(func() bool {
		head := sys.L2EL.BlockRefByLabel(eth.Unsafe)
		if head.Time >= o2.Time {
			return true
		}
		require.NoError(tsL2.New(ctx, seqtypes.BuildOpts{Parent: head.Hash, L1Origin: &o1Hash}))
		require.NoError(tsL2.Next(ctx))
		return false
	}, 60*time.Second, 500*time.Millisecond, "L2 head time must reach O2 time %d", o2.Time)

	parent := sys.L2EL.BlockRefByLabel(eth.Unsafe)
	require.GreaterOrEqual(parent.Time, o2.Time)
	logger.Info("drop-scenario parent", "l2", parent.Number, "time", parent.Time,
		"o1", o1.Number, "o2", o2.Number, "o2Time", o2.Time)

	// ── build#1 (dropped): open on parent H with origin O1, commit a preconf tx
	// into it (Success = must-land commitment), then ABANDON it without sealing.
	// `ensure_only_one_payload` guarantees this Open cancels any leftover op-node
	// build on the same parent, so the preconf lands in THIS build (not a lingering
	// orphan that never seals).
	require.NoError(tsL2.New(ctx, seqtypes.BuildOpts{Parent: parent.Hash, L1Origin: &o1Hash}), "New build#1")
	require.NoError(tsL2.Open(ctx), "Open build#1")

	// Let the build subscribe to the fifo broadcast before we push. A preconf
	// pushed before the build subscribes falls into the "dead window" and this
	// build misses it (reth's builder interval is ~100ms; op-node normally holds
	// the build a full slot before getPayload).
	time.Sleep(600 * time.Millisecond)

	txHash := sendPreconf(t, ctx, sys.L2EL, alice, bob.Address(), eth.OneHundredthEther)
	logger.Info("preconf committed into build#1 (Success)", "hash", txHash)

	require.NoError(tsL2.Cancel(ctx), "Cancel build#1 (drop payload: no getPayload, no commit)")

	// The dropped payload never committed: the head must still be H, and the
	// committed tx must NOT be on chain yet.
	require.Equal(parent.Hash, sys.L2EL.BlockRefByLabel(eth.Unsafe).Hash,
		"head must not advance after the payload is dropped")
	_, err := sys.L2EL.Escape().EthClient().TransactionReceipt(ctx, txHash)
	require.Error(err, "dropped-payload tx must not be on chain before carryover")

	// ── build#2 (carryover): a fresh build on the SAME parent H but with origin
	// O2 (distinct attrs -> distinct payloadId -> a real new build_payload). Its
	// carryover preamble re-dispatches the surviving Success entry from the fifo,
	// and `ensure_only_one_payload` cancels build#1's now-abandoned reth job so
	// build#2 is the sole live build. Use the granular Open->wait->Seal path — NOT
	// Next(), which chains Open->Seal in ~µs, sealing before carryover (a state
	// query + EVM execute) can run.
	require.NoError(tsL2.New(ctx, seqtypes.BuildOpts{Parent: parent.Hash, L1Origin: &o2Hash}), "New build#2")
	require.NoError(tsL2.Open(ctx), "Open build#2")
	time.Sleep(600 * time.Millisecond)
	require.NoError(tsL2.Seal(ctx), "Seal build#2")
	require.NoError(tsL2.Sign(ctx), "Sign build#2")
	require.NoError(tsL2.Commit(ctx), "Commit build#2")
	require.NoError(tsL2.Publish(ctx), "Publish build#2")

	// SLA: the commitment re-lands via carryover, same hash, exactly once, in the
	// block right after H — and that block is genuinely build#2 (origin O2), which
	// proves carryover ran a fresh build rather than the test vacuously reusing a
	// cached build#1 payload.
	rec := waitReceipt(t, sys.L2EL, txHash)
	landed := sys.L2EL.BlockRefByNumber(rec.BlockNumber.Uint64())
	require.Equal(rec.BlockHash, landed.Hash, "re-landed tx must be in the canonical chain")
	require.Equal(parent.Number+1, landed.Number, "commitment must land in the block right after H")
	require.Equalf(o2.Number, landed.L1Origin.Number,
		"re-landed block must be build#2 (origin O2=%d), not a cached build#1 (origin O1=%d) — "+
			"otherwise carryover was never exercised", o2.Number, o1.Number)
	logger.Info("commitment re-landed via carryover after dropped payload",
		"hash", txHash, "block", landed.Number, "l1Origin", landed.L1Origin.Number)
}

// sendPreconf submits a native-transfer preconf tx from `from` and asserts it
// returns the success (must-land) commitment. It returns the tx hash.
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

// assertReLandedCanonical waits for hash to reappear on chain and asserts it
// sits in exactly one canonical block (a tx hash can only be canonical in one
// block, so this rejects orphaned or duplicate inclusion). It returns the
// receipt.
func assertReLandedCanonical(t devtest.T, el *dsl.L2ELNode, hash common.Hash) *types.Receipt {
	rec := waitReceipt(t, el, hash)
	t.Require().Equal(hash, rec.TxHash, "same hash must re-land")
	canon := el.BlockRefByNumber(rec.BlockNumber.Uint64())
	t.Require().Equalf(rec.BlockHash, canon.Hash,
		"re-landed tx %s must be in the canonical chain (no orphaned/duplicate inclusion)", hash)
	return rec
}

// waitReverted blocks until ref is no longer the canonical block at its height
// (hash changed, or the chain is momentarily shorter), i.e. the reorg reverted
// it. attempts polls at 2s each.
func waitReverted(t devtest.T, el *dsl.L2ELNode, ref eth.L2BlockRef, attempts int) {
	t.Require().Eventuallyf(func() bool {
		cur, err := el.Escape().EthClient().BlockRefByNumber(t.Ctx(), ref.Number)
		if err != nil {
			// "not found" means the chain is (transiently) shorter than the
			// reverted height — the block is gone.
			return true
		}
		return cur.Hash != ref.Hash
	}, time.Duration(attempts)*2*time.Second, 2*time.Second,
		"awaiting revert of L2 block %d (%s)", ref.Number, ref.Hash)
}

// waitReceipt polls the L2 EL until a receipt for hash is available.
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
