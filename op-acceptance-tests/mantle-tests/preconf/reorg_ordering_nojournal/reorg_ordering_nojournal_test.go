// Package reorgorderingnojournal is the journal-OFF (TC-RG4 degraded path)
// counterpart of the `reorg_ordering` package.
//
// With the journal ON (see `reorg_ordering`, TC-RG2) a reverted preconf
// commitment is identified as a reorg replay and re-dispatched through the
// carryover preamble, so it re-lands AHEAD of a higher-tip normal tx — a clean
// attribution signal. With the journal OFF that identification is gone: the
// re-injected commitment is pushed as an Rpc-source entry subject to the SLA /
// gas gates, so it MAY not get front-of-block carryover treatment, or may be
// legitimately dropped. So this is a CHARACTERIZATION test, mirroring the
// `reorg_nojournal` convention:
//
// Hard-asserted invariants (must hold even on the degraded path):
//   - a real reorg happened (the block carrying P and N was reverted);
//   - the high-tip normal tx N re-lands exactly once on the canonical chain;
//   - the node stays alive and keeps producing blocks.
//
// Characterized (recorded, not required): whether the low-tip preconf tx P
// re-lands at all, whether it co-locates with N, and its ordering. The ONLY
// conditional invariant asserted about P: if it re-lands in the same block as N,
// it must still precede N (a tip-order inversion the other way — high-tip N
// before low-tip P in the same block — would be normal pool behaviour and is the
// expected degraded outcome, so different blocks / a drop are NOT failures).
package reorgorderingnojournal

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

// preconfEvent mirrors mantle-reth's PreconfTxEvent serde (camelCase wire).
type preconfEvent struct {
	TxHash      common.Hash    `json:"txHash"`
	Status      string         `json:"status"`
	Reason      string         `json:"reason"`
	BlockHeight hexutil.Uint64 `json:"blockHeight"`
}

const normalRecipient = "0x00000000000000000000000000000000000000B2"

// highTipWei is the normal tx's priority fee — far above the preconf tx's
// default 1 gwei, so under pure pool ordering the normal tx would be placed first.
var highTipWei = big.NewInt(200e9) // 200 gwei

// TestPreconfReorgOrderingInversionJournalOff (TC-RG4, characterization) runs the
// same low-tip-preconf-vs-high-tip-normal ordering probe as `reorg_ordering` but
// on a journal-OFF node, and records P's fate/ordering instead of hard-asserting
// the inversion.
func TestPreconfReorgOrderingInversionJournalOff(gt *testing.T) {
	if os.Getenv("DEVSTACK_L2EL_KIND") != "op-reth" {
		gt.Skip("preconf is only wired for op-reth; set DEVSTACK_L2EL_KIND=op-reth")
	}

	t := devtest.SerialT(gt)
	sys := presets.NewMantleSingleChainMultiNodeWithTestSeq(t)
	require := t.Require()
	logger := t.Logger()
	ctx := t.Ctx()

	sys.L2Batcher.Stop()
	gt.Cleanup(func() { sys.L2Batcher.Start() })

	ts := sys.TestSequencer.Escape().ControlAPI(sys.L1Network.ChainID())
	cl := sys.L1Network.Escape().L1CLNode(match.FirstL1CL)

	sys.L1Network.WaitForBlock()
	sys.ControlPlane.FakePoSState(cl.ID(), stack.Stop)

	// Fund the senders FIRST, on the current (early) L1 origin — BEFORE advancing
	// to the reorg origin — so their funding is NOT reverted by the reorg;
	// otherwise the senders would be unfunded at the new tip and the re-injected
	// txs would be rejected for insufficient funds (never re-landing).
	priv, err := crypto.HexToECDSA(sysgo.PreconfWhitelistSenderPrivHex)
	require.NoError(err, "must parse whitelisted preconf sender key")
	kpc := dsl.NewKey(t, priv).User(sys.L2EL)
	require.Equalf(common.HexToAddress(sysgo.PreconfWhitelistSenderAddr), kpc.Address(),
		"fixed preconf sender key must derive the whitelisted address")
	sys.FunderL2.Fund(kpc, eth.OneTenthEther)
	aliceN := sys.FunderL2.NewFundedEOA(eth.OneTenthEther)
	fundOrigin := sys.L2EL.BlockRefByLabel(eth.Unsafe).L1Origin.Number

	startL1Block := sys.L1EL.BlockRefByLabel(eth.Unsafe)
	require.Eventually(func() bool {
		require.NoError(ts.New(ctx, seqtypes.BuildOpts{Parent: common.Hash{}}))
		require.NoError(ts.Next(ctx))
		l2Unsafe := sys.L2EL.BlockRefByLabel(eth.Unsafe)
		return l2Unsafe.Number > 0 && l2Unsafe.L1Origin.Number > startL1Block.Number
	}, 120*time.Second, 2*time.Second)

	rpcTo := common.HexToAddress(sysgo.PreconfWhitelistRecipientAddr)
	normTo := common.HexToAddress(normalRecipient)

	txP := submitPreconf(t, ctx, sys.L2EL, kpc, rpcTo, eth.OneHundredthEther)
	txN := submitNormal(t, ctx, sys.L2EL, aliceN, normTo, eth.OneHundredthEther, highTipWei)
	logger.Info("submitted preconf P (low tip) and normal N (high tip)", "P", txP, "N", txN)

	recP := waitReceipt(t, sys.L2EL, txP)
	recN := waitReceipt(t, sys.L2EL, txN)
	blkP := sys.L2EL.BlockRefByNumber(recP.BlockNumber.Uint64())
	blkN := sys.L2EL.BlockRefByNumber(recN.BlockNumber.Uint64())
	require.NotZero(blkP.L1Origin.Number, "P's block must have a non-genesis L1 origin to reorg")
	require.NotZero(blkN.L1Origin.Number, "N's block must have a non-genesis L1 origin to reorg")
	require.Greaterf(blkN.L1Origin.Number, fundOrigin,
		"N origin %d must exceed funding origin %d so the reorg keeps senders funded",
		blkN.L1Origin.Number, fundOrigin)

	earliest := blkP
	if blkN.Number < earliest.Number {
		earliest = blkN
	}

	// Freeze the L2 head so it does not run ahead during the reorg window (keeps
	// the revert shallow), then resume once the L1 reorg has propagated so op-node
	// applies the reset. See the journal-on ordering test for the full rationale.
	oldL1Head := sys.L1EL.BlockRefByLabel(eth.Unsafe).Number
	sys.L2CL.StopSequencer()

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

	// Hard invariant: a real reorg happened.
	waitReverted(t, sys.L2EL, earliest, 30)
	require.NotEqual(earliest.Hash, sys.L2EL.BlockRefByNumber(earliest.Number).Hash,
		"the original block carrying P/N must no longer be canonical after the reorg")
	logger.Info("L2 reorg triggered (journal OFF)", "reverted", earliest.Number)

	// Hard invariant: the normal tx N re-lands exactly once (native pool
	// re-injection does not depend on the preconf journal).
	reN := assertReLandedCanonical(t, sys.L2EL, txN)

	// Characterize P: it MAY drop or land late on the degraded path.
	reP := pollReceipt(t, sys.L2EL, txP, 15) // ~30s tolerant poll; nil = not re-landed
	switch {
	case reP == nil:
		logger.Warn("TC-RG4: preconf P DROPPED after reorg (journal off, expected-possible)",
			"P", txP, "N.reland.block", reN.BlockNumber)
	default:
		canon := sys.L2EL.BlockRefByNumber(reP.BlockNumber.Uint64())
		require.Equalf(reP.BlockHash, canon.Hash,
			"re-landed preconf P %s must sit in the canonical chain (no orphaned/duplicate inclusion)", txP)
		sameBlock := reP.BlockNumber.Uint64() == reN.BlockNumber.Uint64()
		if sameBlock {
			// The only conditional invariant: co-located, P must still precede N.
			require.Lessf(reP.TransactionIndex, reN.TransactionIndex,
				"journal-off: when P re-lands in N's block %d, low-tip P (idx %d) must still "+
					"precede high-tip N (idx %d)", reP.BlockNumber.Uint64(), reP.TransactionIndex, reN.TransactionIndex)
		}
		logger.Info("TC-RG4: preconf P re-landed (journal off)",
			"P.block", reP.BlockNumber, "P.idx", reP.TransactionIndex,
			"N.block", reN.BlockNumber, "N.idx", reN.TransactionIndex,
			"sameBlockAsN", sameBlock, "preconfFirstWhenColocated", sameBlock)
	}

	// The contrast row for the TC-RG4 vs TC-RG2 diff table.
	// TODO(TC-RG4): emit as a machine-readable artifact so the journal-on vs -off
	//   ordering table can be assembled in CI rather than eyeballed from logs.
	logger.Info("TC-RG4 ordering result (journal OFF degraded path)",
		"preconfReLanded", reP != nil,
		"coLocatedWithNormal", reP != nil && reN != nil && reP.BlockNumber.Uint64() == reN.BlockNumber.Uint64())

	// Liveness: whatever the replay outcome, the node must not have wedged.
	beforeTip := sys.L2EL.BlockRefByLabel(eth.Unsafe).Number
	require.Eventuallyf(func() bool {
		require.NoError(ts.New(ctx, seqtypes.BuildOpts{Parent: common.Hash{}}))
		require.NoError(ts.Next(ctx))
		return sys.L2EL.BlockRefByLabel(eth.Unsafe).Number > beforeTip
	}, 60*time.Second, 2*time.Second, "node must keep producing blocks after the reorg")
}

// --- submission helpers (duplicated from reorg_ordering; keep in sync) --------

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

func assertReLandedCanonical(t devtest.T, el *dsl.L2ELNode, hash common.Hash) *types.Receipt {
	rec := waitReceipt(t, el, hash)
	t.Require().Equal(hash, rec.TxHash, "same hash must re-land")
	canon := el.BlockRefByNumber(rec.BlockNumber.Uint64())
	t.Require().Equalf(rec.BlockHash, canon.Hash,
		"re-landed tx %s must be in the canonical chain (no orphaned/duplicate inclusion)", hash)
	return rec
}

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

// pollReceipt is the TOLERANT variant used AFTER the reorg for P: it returns the
// receipt if the tx re-lands within `attempts` (2s each), or nil if it never
// does. A dropped commitment is an expected outcome on the journal-off path.
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
