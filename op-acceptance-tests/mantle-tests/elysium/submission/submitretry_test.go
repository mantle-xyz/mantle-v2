package submission

import (
	"context"
	"math/big"
	"sync/atomic"
	"testing"

	"github.com/ethereum-optimism/optimism/op-acceptance-tests/mantle-tests/elysium/internal/testhelpers"
	opforks "github.com/ethereum-optimism/optimism/op-core/forks"
	"github.com/ethereum-optimism/optimism/op-devstack/devtest"
	"github.com/ethereum-optimism/optimism/op-devstack/presets"
	"github.com/ethereum-optimism/optimism/op-service/eth"
	"github.com/ethereum-optimism/optimism/op-service/txmgr"
	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	gethtypes "github.com/ethereum/go-ethereum/core/types"
)

// submitSink is an arbitrary destination for the probe txs these tests submit.
var submitSink = common.HexToAddress("0x000000000000000000000000000000005B117E77")

// underEstimatingBackend underestimates once, then reports the real gas need.
// This reproduces "the gas limit we committed to was not enough" — the failure EIP-7976 makes
// newly plausible by raising the calldata floor cost — without needing to catch the L1 mid-repricing.
type underEstimatingBackend struct {
	txmgr.ETHBackend
	lied     atomic.Bool
	reported atomic.Uint64
}

func (b *underEstimatingBackend) EstimateGas(ctx context.Context, msg ethereum.CallMsg) (uint64, error) {
	real, err := b.ETHBackend.EstimateGas(ctx, msg)
	if err != nil {
		return 0, err
	}
	if b.lied.CompareAndSwap(false, true) {
		// Half of the true requirement: below the intrinsic/floor cost, so the L1 must refuse it.
		low := real / 2
		b.reported.Store(low)
		return low, nil
	}
	return real, nil
}

// underPricingBackend underprices once, then reports the real fee data.
// A tx crafted from it cannot be mined at the prevailing base fee, which is the "stuck in the
// mempool" condition the fee-bump path exists to recover from.
type underPricingBackend struct {
	txmgr.ETHBackend
	liedTip  atomic.Bool
	liedBase atomic.Bool
}

func (b *underPricingBackend) SuggestGasTipCap(ctx context.Context) (*big.Int, error) {
	if b.liedTip.CompareAndSwap(false, true) {
		return big.NewInt(1), nil
	}
	return b.ETHBackend.SuggestGasTipCap(ctx)
}

func (b *underPricingBackend) HeaderByNumber(ctx context.Context, number *big.Int) (*gethtypes.Header, error) {
	head, err := b.ETHBackend.HeaderByNumber(ctx, number)
	if err != nil {
		return nil, err
	}
	if b.liedBase.CompareAndSwap(false, true) {
		spoofed := gethtypes.CopyHeader(head)
		spoofed.BaseFee = big.NewInt(1)
		return spoofed, nil
	}
	return head, nil
}

// TestSubmit_RecoverFromUnderestimatedGas proves txmgr recovers when the first
// submission uses a gas limit that the Glamsterdam L1 refuses.
//
// The backend injects one low estimate, then returns the real chain estimate.
// Send must land a successful tx at the originally reserved nonce with a mined
// gas limit above the injected estimate and enough gas for actual execution.
func TestSubmit_RecoverFromUnderestimatedGas(gt *testing.T) {
	t := devtest.SerialT(gt)
	sys := presets.NewMantleMinimal(t)
	require := t.Require()
	ctx := t.Ctx()

	l1Config := sys.L1Network.Escape().ChainConfig()
	require.True(sys.L2Chain.IsMantleForkActive(opforks.MantleElysium), "L2 must run with Mantle Elysium active")
	require.NotNil(l1Config.AmsterdamTime, "L1 AmsterdamTime must be configured")

	t.Log("Waiting for L1 Amsterdam to activate")
	testhelpers.WaitForGlamsterdamL1(t, sys.L1EL, *l1Config.AmsterdamTime)
	t.Log("L1 Amsterdam activated")

	l1Eth := sys.L1EL.EthClient()
	eoa := sys.FunderL1.NewFundedEOA(eth.OneEther)
	recorder := &elBackend{cl: l1Eth}
	backend := &underEstimatingBackend{ETHBackend: recorder}
	mgr := newTxMgr(t, backend, eoa.Key().Priv(), eoa.Address(), sys.L1Network.ChainID().ToBig())
	defer mgr.Close()

	startNonce, err := l1Eth.PendingNonceAt(ctx, eoa.Address())
	require.NoError(err, "read the submitter's starting nonce")

	// Calldata-heavy payload: under EIP-7976 its floor cost dominates, so an under-estimate is
	// rejected by the L1 rather than silently mined.
	rcpt, err := mgr.Send(ctx, txmgr.TxCandidate{
		To:     &submitSink,
		TxData: make([]byte, 8_192),
	})
	require.NoError(err, "the submission path must recover from an underestimated gas limit and land the tx")
	require.Equal(gethtypes.ReceiptStatusSuccessful, rcpt.Status, "the recovered tx must be mined successfully")

	badEstimate := backend.reported.Load()
	require.Greater(badEstimate, uint64(0), "the test must have injected an under-estimate")

	mined, _, err := l1Eth.InfoAndTxsByHash(ctx, rcpt.BlockHash)
	require.NoError(err, "read the L1 block that included the recovered tx")
	require.True(l1Config.IsAmsterdam(new(big.Int).SetUint64(mined.NumberU64()), mined.Time()),
		"the recovered tx must land on a post-Amsterdam L1 block")

	minedTx := findTx(t, l1Eth, rcpt)

	// Observe the attempts rather than infer them. badEstimate above is only what the injected
	// backend REPORTED; what matters is the gas limit txmgr actually signed the first attempt
	// with, and whether it made a second attempt at all.
	published := recorder.distinctPublishedTxs()
	require.GreaterOrEqualf(len(published), 2,
		"txmgr must have published a DISTINCT second attempt after the under-estimated first one; it published %d distinct tx(s)", len(published))
	first := published[0]
	require.Equalf(badEstimate, first.Gas(),
		"the first published tx must carry the injected under-estimate (%d), otherwise the scenario was never created", badEstimate)
	require.NotEqual(first.Hash(), minedTx.Hash(), "the mined tx must be a re-estimated resubmission, not the first attempt")
	require.Greaterf(minedTx.Gas(), first.Gas(),
		"the mined tx must carry a re-estimated gas limit, higher than the %d the first published attempt committed to", first.Gas())
	require.Equal(startNonce, minedTx.Nonce(), "the recovery must reuse the reserved nonce, not skip it")
	t.Log("submission recovered from an underestimated gas limit",
		"distinctAttempts", len(published), "firstAttemptGasLimit", first.Gas(), "reportedUnderEstimate", badEstimate,
		"minedGasLimit", minedTx.Gas(), "gasUsed", rcpt.GasUsed, "l1Block", mined.NumberU64())
}

// TestBatcher_FeeBump_PostGlamsterdam proves txmgr fee-bumps an underpriced
// submission against a post-Amsterdam L1 until it lands.
//
// The backend injects one near-zero fee estimate. Send must land a successful tx
// at the reserved nonce with a higher fee cap that covers the inclusion block's
// base fee, and the account nonce must advance by exactly one.
func TestBatcher_FeeBump_PostGlamsterdam(gt *testing.T) {
	t := devtest.SerialT(gt)
	sys := presets.NewMantleMinimal(t)
	require := t.Require()
	ctx := t.Ctx()

	l1Config := sys.L1Network.Escape().ChainConfig()
	require.True(sys.L2Chain.IsMantleForkActive(opforks.MantleElysium), "L2 must run with Mantle Elysium active")
	require.NotNil(l1Config.AmsterdamTime, "L1 AmsterdamTime must be configured")

	t.Log("Waiting for L1 Amsterdam to activate")
	testhelpers.WaitForGlamsterdamL1(t, sys.L1EL, *l1Config.AmsterdamTime)
	t.Log("L1 Amsterdam activated")

	l1Eth := sys.L1EL.EthClient()
	eoa := sys.FunderL1.NewFundedEOA(eth.OneEther)
	base := &elBackend{cl: l1Eth}
	backend := &underPricingBackend{ETHBackend: base}
	mgr := newTxMgr(t, backend, eoa.Key().Priv(), eoa.Address(), sys.L1Network.ChainID().ToBig())
	defer mgr.Close()

	// The fee cap the first attempt is EXPECTED to be signed with: tip + 2*baseFee, both spoofed
	// to 1 wei. This is a prediction about txmgr's fee arithmetic, so it is checked against the
	// recorded first attempt below rather than used as the comparison baseline.
	const firstFeeCap = 3

	startNonce, err := l1Eth.PendingNonceAt(ctx, eoa.Address())
	require.NoError(err, "read the submitter's starting nonce")

	rcpt, err := mgr.Send(ctx, txmgr.TxCandidate{
		To:     &submitSink,
		TxData: make([]byte, 1_024),
	})
	require.NoError(err, "the submission path must fee-bump an underpriced tx until it lands")
	require.Equal(gethtypes.ReceiptStatusSuccessful, rcpt.Status, "the bumped tx must be mined successfully")

	mined, _, err := l1Eth.InfoAndTxsByHash(ctx, rcpt.BlockHash)
	require.NoError(err, "read the L1 block that included the bumped tx")
	require.True(l1Config.IsAmsterdam(new(big.Int).SetUint64(mined.NumberU64()), mined.Time()),
		"the bumped tx must land on a post-Amsterdam L1 block")

	minedTx := findTx(t, l1Eth, rcpt)

	// Observe the attempts rather than infer them. Without this the case could not tell a genuine
	// bump from a first attempt that happened to be priced high enough all along.
	published := base.distinctPublishedTxs()
	require.GreaterOrEqualf(len(published), 2,
		"txmgr must have published a DISTINCT bumped resubmission after the underpriced first one; it published %d distinct tx(s)", len(published))
	first := published[0]
	// Bound the first attempt rather than pin it to an exact number: what the scenario needs is
	// that it was priced FAR below the real market, not that txmgr's fee formula is still exactly
	// gasTipCap + 2*baseFee. An equality here would go red on a legitimate change to calcGasFeeCap
	// while the underpricing it guards was still injected perfectly.
	require.LessOrEqualf(first.GasFeeCap().Uint64(), uint64(firstFeeCap),
		"the first published tx must carry the spoofed near-zero fee cap (<= %d wei from tip=1, baseFee=1), otherwise the underpricing was never injected; got %s",
		firstFeeCap, first.GasFeeCap())
	require.Positivef(mined.BaseFee().Cmp(first.GasFeeCap()),
		"the first attempt must be unmineable at the prevailing base fee %s, otherwise there was nothing to bump from", mined.BaseFee())
	require.NotEqual(first.Hash(), minedTx.Hash(), "the mined tx must be the bumped resubmission, not the first attempt")
	require.Positivef(minedTx.GasFeeCap().Cmp(first.GasFeeCap()),
		"the mined tx must carry a bumped fee cap, higher than the %s wei the first published attempt was signed with", first.GasFeeCap())
	require.GreaterOrEqual(minedTx.GasFeeCap().Cmp(mined.BaseFee()), 0,
		"the bumped fee cap must at least cover the base fee of the block that included it")
	require.Equal(startNonce, minedTx.Nonce(), "the bumped tx must reuse the reserved nonce")
	require.Equal(first.Nonce(), minedTx.Nonce(), "the bump must replace the first attempt at its own nonce, not open a new one")

	endNonce, err := l1Eth.NonceAt(ctx, eoa.Address(), nil)
	require.NoError(err, "read the submitter's final nonce")
	require.Equalf(startNonce+1, endNonce,
		"the underpriced tx must have been replaced, not duplicated or abandoned: nonce went %d -> %d", startNonce, endNonce)
	t.Log("submission recovered from an underpriced first attempt",
		"distinctAttempts", len(published), "firstAttemptFeeCap", first.GasFeeCap(),
		"minedFeeCap", minedTx.GasFeeCap(), "blockBaseFee", mined.BaseFee(), "l1Block", mined.NumberU64())
}

// findTx returns the mined transaction the receipt refers to.
func findTx(t devtest.T, l1Eth interface {
	InfoAndTxsByHash(ctx context.Context, hash common.Hash) (eth.BlockInfo, gethtypes.Transactions, error)
}, rcpt *gethtypes.Receipt) *gethtypes.Transaction {
	_, txs, err := l1Eth.InfoAndTxsByHash(t.Ctx(), rcpt.BlockHash)
	t.Require().NoError(err, "read the block containing the mined tx")
	for _, tx := range txs {
		if tx.Hash() == rcpt.TxHash {
			return tx
		}
	}
	t.Require().FailNow("receipt tx not found in its own block", "txHash", rcpt.TxHash)
	return nil
}
