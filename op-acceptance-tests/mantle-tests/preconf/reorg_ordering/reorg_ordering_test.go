// Package reorgordering strengthens the TC-RG2 reorg attribution: it proves a
// reverted preconf commitment re-lands via the PRECONF path (the carryover
// preamble), not merely via reth's native pool re-injection.
//
// Why this is needed: the `reorg` package asserts a reverted commitment's
// receipt reappears on the new chain. But that only proves the OUTCOME (on
// chain), not the CAUSE. The tx under test is a plain transfer, and reth
// natively re-injects reverted-block txs into the pool — so a simple transfer
// would re-land via ordinary pool mechanics even if the preconf must-land
// machinery were deleted. The re-land assertion can therefore pass vacuously.
//
// The discriminator (black-box, no log scraping): the preconf builder applies
// carryover/replay entries in a preamble BEFORE the normal pool best-tx loop
// (mantle-reth crates/preconf/src/builder/payload_builder.rs), and the preconf
// FIFO is arrival-ordered, tip-AGNOSTIC. A normal pool tx is placed later, in
// tip order. So we co-submit:
//
//   - P: a preconf tx with a LOW tip (whitelisted sender→recipient), and
//   - N: a normal tx with a HIGH tip (non-whitelisted recipient → pool path),
//
// let a reorg revert the block carrying both, and assert that after the reorg P
// is ordered AHEAD of the higher-tip N. Under pure pool ordering the higher-tip
// N would win; P winning proves the carryover path placed it. This requires a
// RESTRICTED (from,to) whitelist (see the whitelist preset) — under
// --preconf.all every tx is preconf and there is no tip-ordered tx to contrast.
//
// The reorg is driven exactly as in the `reorg` package: an in-process test
// sequencer rewrites an L1 block on an ancestor parent, and op-node cascades the
// L1 reorg into an L2 unsafe reorg. Requires the sysgo orchestrator and op-reth.
package reorgordering

import (
	"context"
	"math/big"
	"os"
	"testing"
	"time"

	"github.com/ethereum-optimism/optimism/op-devstack/devtest"
	"github.com/ethereum-optimism/optimism/op-devstack/dsl"
	"github.com/ethereum-optimism/optimism/op-devstack/presets"
	"github.com/ethereum-optimism/optimism/op-devstack/stack"
	"github.com/ethereum-optimism/optimism/op-devstack/stack/match"
	"github.com/ethereum-optimism/optimism/op-devstack/sysgo"
	"github.com/ethereum-optimism/optimism/op-service/eth"
	"github.com/ethereum-optimism/optimism/op-service/txplan"
	"github.com/ethereum-optimism/optimism/op-test-sequencer/sequencer/seqtypes"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
)

// preconfEvent decodes the subset of eth_sendRawTransactionWithPreconf's result
// we assert on. The wire is camelCase (mantle-reth's PreconfTxEvent derives
// serde(rename_all = "camelCase")), so the keys are txHash / blockHeight — NOT
// snake_case (tagging them snake_case silently decodes to zero).
type preconfEvent struct {
	TxHash      common.Hash    `json:"txHash"`
	Status      string         `json:"status"`
	Reason      string         `json:"reason"`
	BlockHeight hexutil.Uint64 `json:"blockHeight"`
}

// normalRecipient is a fixed address that is NOT in the preconf recipient
// whitelist (sysgo.PreconfWhitelistRecipientAddr), so a transfer to it stays a
// tip-ordered pool tx rather than becoming preconf-eligible.
const normalRecipient = "0x00000000000000000000000000000000000000B2"

// lowTipWei is the preconf tx's priority fee (the txplan default, 1 gwei);
// highTipWei is the normal tx's — two orders of magnitude higher, so under pure
// pool ordering the normal tx would be placed first.
var (
	lowTipWei  = big.NewInt(1e9)   // 1 gwei (matches txplan default)
	highTipWei = big.NewInt(200e9) // 200 gwei
)

// TestPreconfReorgOrderingInversion (TC-RG2, attribution) co-submits a low-tip
// preconf tx P and a high-tip normal tx N into the same unsafe block, reorgs
// that block out, and asserts that after the reorg P is ordered AHEAD of N —
// proving P re-landed via the preconf carryover path, not tip-ordered pool
// re-injection.
func TestPreconfReorgOrderingInversion(gt *testing.T) {
	if os.Getenv("DEVSTACK_L2EL_KIND") != "op-reth" {
		gt.Skip("preconf is only wired for op-reth; set DEVSTACK_L2EL_KIND=op-reth")
	}

	t := devtest.SerialT(gt)
	sys := presets.NewMantleSingleChainMultiNodeWithTestSeq(t)
	require := t.Require()
	logger := t.Logger()
	ctx := t.Ctx()

	// Freeze the safe head so the block carrying P and N stays unsafe — only
	// unsafe blocks are reverted by the L1-origin rewrite below.
	sys.L2Batcher.Stop()
	gt.Cleanup(func() { sys.L2Batcher.Start() })

	ts := sys.TestSequencer.Escape().ControlAPI(sys.L1Network.ChainID())
	cl := sys.L1Network.Escape().L1CLNode(match.FirstL1CL)

	sys.L1Network.WaitForBlock()
	sys.ControlPlane.FakePoSState(cl.ID(), stack.Stop)

	// Fund the senders FIRST, on the current (early) L1 origin — BEFORE advancing
	// L1 to the reorg origin. This is CRITICAL: the reorg reverts the block
	// carrying P/N and everything on that L1 origin. If the senders' funding txs
	// were on that same (reverted) origin, the senders would be unfunded at the
	// new tip, and reth would (correctly) reject the re-injected P/N for
	// insufficient funds — so they'd never re-land. Funding on an earlier origin
	// keeps the senders funded across the reorg. The whitelisted preconf sender is
	// a fixed key (its address is --preconf.from); the normal tx uses a dynamic EOA.
	priv, err := crypto.HexToECDSA(sysgo.PreconfWhitelistSenderPrivHex)
	require.NoError(err, "must parse whitelisted preconf sender key")
	kpc := dsl.NewKey(t, priv).User(sys.L2EL)
	require.Equalf(common.HexToAddress(sysgo.PreconfWhitelistSenderAddr), kpc.Address(),
		"fixed preconf sender key must derive the whitelisted address")
	sys.FunderL2.Fund(kpc, eth.OneTenthEther)
	aliceN := sys.FunderL2.NewFundedEOA(eth.OneTenthEther)
	fundOrigin := sys.L2EL.BlockRefByLabel(eth.Unsafe).L1Origin.Number
	logger.Info("senders funded", "fundL1Origin", fundOrigin)

	// Advance L1 until the L2 unsafe head sits on an L1 origin past our start —
	// that (later) origin is what we later reorg, leaving the funding intact.
	startL1Block := sys.L1EL.BlockRefByLabel(eth.Unsafe)
	require.Eventually(func() bool {
		require.NoError(ts.New(ctx, seqtypes.BuildOpts{Parent: common.Hash{}}))
		require.NoError(ts.Next(ctx))
		l2Unsafe := sys.L2EL.BlockRefByLabel(eth.Unsafe)
		return l2Unsafe.Number > 0 && l2Unsafe.L1Origin.Number > startL1Block.Number
	}, 120*time.Second, 2*time.Second)

	rpcTo := common.HexToAddress(sysgo.PreconfWhitelistRecipientAddr) // whitelisted → preconf
	normTo := common.HexToAddress(normalRecipient)                    // not whitelisted → pool

	// Submit P (preconf, low tip) then N (normal, high tip) back-to-back with NO
	// L1 advance between them, so op-node's next build carries both in one unsafe
	// block: P via the preconf FIFO, N via the pool.
	txP := submitPreconf(t, ctx, sys.L2EL, kpc, rpcTo, eth.OneHundredthEther)
	txN := submitNormal(t, ctx, sys.L2EL, aliceN, normTo, eth.OneHundredthEther, highTipWei)
	logger.Info("submitted preconf P (low tip) and normal N (high tip)", "P", txP, "N", txN)

	recP := waitReceipt(t, sys.L2EL, txP)
	recN := waitReceipt(t, sys.L2EL, txN)
	blkP := sys.L2EL.BlockRefByNumber(recP.BlockNumber.Uint64())
	blkN := sys.L2EL.BlockRefByNumber(recN.BlockNumber.Uint64())
	// Guard the invariant above: the reorg target origin must be strictly above
	// the funding origin, else the senders lose their funding in the reorg.
	require.Greaterf(blkP.L1Origin.Number, fundOrigin,
		"P origin %d must exceed funding origin %d so the reorg keeps senders funded",
		blkP.L1Origin.Number, fundOrigin)
	require.Greaterf(blkN.L1Origin.Number, fundOrigin,
		"N origin %d must exceed funding origin %d so the reorg keeps senders funded",
		blkN.L1Origin.Number, fundOrigin)
	require.NotZero(blkP.L1Origin.Number, "P's block must have a non-genesis L1 origin to reorg")
	require.NotZero(blkN.L1Origin.Number, "N's block must have a non-genesis L1 origin to reorg")
	logger.Info("P and N landed", "P.block", blkP.Number, "N.block", blkN.Number,
		"P.idx", recP.TransactionIndex, "N.idx", recN.TransactionIndex)

	earliest := blkP
	if blkN.Number < earliest.Number {
		earliest = blkN
	}

	// Freeze the L2 unsafe head before triggering the reorg. Otherwise op-node
	// keeps sequencing (~2s/block) throughout the multi-second reorg-cascade
	// window and piles blocks onto the commitment's L1 origin; reverting that
	// origin then produces a DEEP reorg whose churn stops the committed tx from
	// re-landing (empirically the deep-reorg run never re-lands P). With the
	// sequencer stopped no blocks pile up, so the revert stays shallow.
	oldL1Head := sys.L1EL.BlockRefByLabel(eth.Unsafe).Number
	sys.L2CL.StopSequencer()

	// Reorg out the earliest of the two blocks' L1 origin; reverting it reverts
	// that block (and any descendant), so both P and N go.
	l1Before := sys.L1EL.BlockRefByNumber(earliest.L1Origin.Number)
	logger.Info("triggering L1 reorg", "l1", l1Before, "revertFromL2", earliest.Number, "oldL1Head", oldL1Head)
	require.NoError(ts.New(ctx, seqtypes.BuildOpts{Parent: l1Before.ParentHash}))
	require.NoError(ts.Next(ctx))
	sys.ControlPlane.FakePoSState(cl.ID(), stack.Start)

	// Wait for the alternate L1 chain to OVERTAKE the old head (and the target
	// height to actually carry a different block). Only then has op-node's L1 view
	// reorged, so restarting the sequencer applies the reset promptly instead of
	// free-running (and rebuilding a deep pile) for ~20s while L1 catches up — the
	// bug that made an earlier StartSequencer re-create the deep reorg.
	require.Eventuallyf(func() bool {
		h := sys.L1EL.BlockRefByLabel(eth.Unsafe)
		return h.Number > oldL1Head &&
			sys.L1EL.BlockRefByNumber(l1Before.Number).Hash != l1Before.Hash
	}, 120*time.Second, 2*time.Second, "L1 reorg must propagate past old head %d", oldL1Head)
	logger.Info("L1 reorg propagated", "l1Head", sys.L1EL.BlockRefByLabel(eth.Unsafe).Number)

	// Resume sequencing so op-node APPLIES the L1-reorg reset. A stopped sequencer
	// receives the reset signal but aborts without reverting the unsafe head
	// ("Sequencer encountered reset signal, aborting work"), so the revert only
	// materialises once sequencing resumes — which then also re-lands P via the
	// carryover path and N via the pool. The head was frozen throughout the reorg
	// window, so the revert is shallow (no deep-reorg churn).
	sys.L2CL.StartSequencer()

	waitReverted(t, sys.L2EL, earliest, 30)
	require.NotEqual(earliest.Hash, sys.L2EL.BlockRefByNumber(earliest.Number).Hash,
		"the original block carrying P/N must no longer be canonical after the reorg")
	logger.Info("L2 reorg triggered; block reverted", "reverted", earliest.Number)

	// Both must re-land, each in exactly one canonical block.
	reP := assertReLandedCanonical(t, sys.L2EL, txP)
	reN := assertReLandedCanonical(t, sys.L2EL, txN)
	logger.Info("re-landed after reorg",
		"P.block", reP.BlockNumber, "P.idx", reP.TransactionIndex,
		"N.block", reN.BlockNumber, "N.idx", reN.TransactionIndex)

	// Attribution: P (low tip) must be ordered AHEAD of N (high tip) on the new
	// chain. Compare canonically as (blockNumber, txIndex); P preceding N proves
	// the carryover path placed P — under pure pool ordering the higher-tip N
	// would come first. N-before-P would signal a carryover regression.
	require.Truef(precedes(reP, reN),
		"preconf P (tip=%s) must be ordered before normal N (tip=%s) after reorg, but got "+
			"P=(block %d, idx %d) vs N=(block %d, idx %d) — re-land went through tip-ordered "+
			"pool, not the preconf carryover path",
		lowTipWei, highTipWei,
		reP.BlockNumber.Uint64(), reP.TransactionIndex,
		reN.BlockNumber.Uint64(), reN.TransactionIndex)

	// When co-located in one block, the index inversion is the cleanest signal:
	// P's index < N's index despite N's higher tip.
	if reP.BlockNumber.Uint64() == reN.BlockNumber.Uint64() {
		require.Lessf(reP.TransactionIndex, reN.TransactionIndex,
			"in the shared re-land block %d, low-tip preconf P (idx %d) must precede high-tip "+
				"normal N (idx %d)", reP.BlockNumber.Uint64(), reP.TransactionIndex, reN.TransactionIndex)
		logger.Info("clean in-block ordering inversion confirmed",
			"block", reP.BlockNumber, "P.idx", reP.TransactionIndex, "N.idx", reN.TransactionIndex)
	} else {
		logger.Info("P and N re-landed in different blocks; canonical ordering still preconf-first",
			"P.block", reP.BlockNumber, "N.block", reN.BlockNumber)
	}
}

// precedes reports whether receipt a is canonically ordered before b, comparing
// (blockNumber, transactionIndex) lexicographically.
func precedes(a, b *types.Receipt) bool {
	if c := a.BlockNumber.Cmp(b.BlockNumber); c != 0 {
		return c < 0
	}
	return a.TransactionIndex < b.TransactionIndex
}

// --- submission helpers ------------------------------------------------------

// signedTransfer builds a signed native-transfer tx from `from` to `to` for
// `amount`, applying any extra txplan options (e.g. a tip override). It returns
// the RLP-encoded raw bytes and the tx hash.
func signedTransfer(t devtest.T, ctx context.Context, from *dsl.EOA, to common.Address, amount eth.ETH, opts ...txplan.Option) ([]byte, common.Hash) {
	require := t.Require()
	planOpts := append([]txplan.Option{from.PlanTransfer(to, amount)}, opts...)
	ptx := txplan.NewPlannedTx(txplan.Combine(planOpts...))
	signed, err := ptx.Signed.Eval(ctx)
	require.NoError(err, "must sign tx")
	raw, err := signed.MarshalBinary()
	require.NoError(err, "must RLP-encode signed tx")
	return raw, signed.Hash()
}

// submitPreconf submits a preconf tx (default 1 gwei tip) via
// eth_sendRawTransactionWithPreconf and asserts the success (must-land)
// commitment. Returns the tx hash.
func submitPreconf(t devtest.T, ctx context.Context, el *dsl.L2ELNode, from *dsl.EOA, to common.Address, amount eth.ETH) common.Hash {
	require := t.Require()
	raw, txHash := signedTransfer(t, ctx, from, to, amount)
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

// submitNormal submits a plain tx (with an explicit priority-fee tip) via
// eth_sendRawTransaction — the tip-ordered pool path. The recipient must NOT be
// preconf-whitelisted, else the pool listener would mirror it into the FIFO.
func submitNormal(t devtest.T, ctx context.Context, el *dsl.L2ELNode, from *dsl.EOA, to common.Address, amount eth.ETH, tip *big.Int) common.Hash {
	require := t.Require()
	raw, txHash := signedTransfer(t, ctx, from, to, amount, txplan.WithGasTipCap(tip))
	var res common.Hash
	require.NoError(
		el.Escape().L2EthClient().RPC().CallContext(ctx, &res,
			"eth_sendRawTransaction", hexutil.Encode(raw)),
		"normal tx submission must not error")
	return txHash
}

// --- shared assertion/poll helpers (kept in sync with the `reorg` package) ---

// assertReLandedCanonical waits for hash to reappear on chain and asserts it sits
// in exactly one canonical block (a tx hash is canonical in at most one block, so
// this rejects orphaned/duplicate inclusion). Returns the receipt.
func assertReLandedCanonical(t devtest.T, el *dsl.L2ELNode, hash common.Hash) *types.Receipt {
	rec := waitReceipt(t, el, hash)
	t.Require().Equal(hash, rec.TxHash, "same hash must re-land")
	canon := el.BlockRefByNumber(rec.BlockNumber.Uint64())
	t.Require().Equalf(rec.BlockHash, canon.Hash,
		"re-landed tx %s must be in the canonical chain (no orphaned/duplicate inclusion)", hash)
	return rec
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

// waitReverted blocks until ref is no longer the canonical block at its height
// (hash changed, or the chain is momentarily shorter), i.e. the reorg reverted it.
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
