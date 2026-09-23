package preconf

import (
	"context"
	"math/big"
	"strings"
	"time"

	"github.com/ethereum-optimism/optimism/op-rpc-compat/pkg/tx"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/holiman/uint256"
)

// throwaway7702Key is a dedicated disposable key. It is used ONLY as the EIP-7702 authorization
// authority, so the delegation lands on this account — never on funder/Addr1 (which must stay
// non-delegated, or geth throttles all their txs → poisons the whole suite).
const throwaway7702Key = "0xd1d1d1d1d1d1d1d1d1d1d1d1d1d1d1d1d1d1d1d1d1d1d1d1d1d1d1d1d1d1d1d1"

// signed1559 builds and signs an EIP-1559 (DynamicFee) tx.
func (r *Runner) signed1559(b *tx.Builder, to *common.Address, value *big.Int, data []byte, gas, nonce uint64, feeCap, tip *big.Int) (*types.Transaction, error) {
	unsigned, err := b.BuildEIP1559Tx(&tx.TxParams{
		Nonce: nonce, MaxFeePerGas: feeCap, MaxPriorityFeePerGas: tip, Gas: gas, To: to, Value: value, Data: data,
	})
	if err != nil {
		return nil, err
	}
	return b.SignTx(unsigned)
}

// fee1559 returns (feeCap, tip) for a 1559 tx from the current gas price.
func (r *Runner) fee1559(ctx context.Context) (*big.Int, *big.Int) {
	gp, _ := r.gasPrice(ctx)
	tip := gp
	if tip == nil || tip.Sign() == 0 {
		tip = big.NewInt(1_000_000_000)
	}
	return new(big.Int).Mul(tip, big.NewInt(2)), tip
}

// A12 valid_1559_preconf: an EIP-1559 native preconf must succeed and land (typed-tx coverage; the
// suite is otherwise legacy-only, but 1559 is the production default).
func (r *Runner) scenarioValid1559Preconf(ctx context.Context) {
	const name = "valid_1559_preconf"
	feeCap, tip := r.fee1559(ctx)
	n, err := r.nonce(ctx, r.funder.Address())
	if err != nil {
		r.record(name, false, "nonce: %v", err)
		return
	}
	signed, err := r.signed1559(r.funder, &Addr2, smallValue, nil, 21000, n, feeCap, tip)
	if err != nil {
		r.record(name, false, "build: %v", err)
		return
	}
	resp, err := r.tester.SendRawTransactionWithPreconf(ctx, r.seq, signed, "1559")
	if err != nil {
		r.record(name, false, "send: %v", err)
		return
	}
	if resp.Status != "success" {
		r.record(name, false, "status=%s reason=%q", resp.Status, derefReason(resp))
		return
	}
	if _, ok := r.verifyOnChain(ctx, name, []promise{promiseFrom(resp)}); ok {
		r.record(name, true, "EIP-1559 preconf success + 链上兑现 blockHeight=%s", resp.BlockHeight)
	}
}

// NOTE: EIP-7702 preconf is intentionally NOT tested on this devnet. The fork isn't
// preconf-eligible here (op-geth returns "can't be submitted as preconf tx"), and worse, the
// rejected 7702 tx still enters the mempool and mines — delegating the sender's account, after which
// geth throttles all further txs from it ("in-flight transaction limit reached for delegated
// accounts"), poisoning every downstream funder-based scenario. Revisit on a 7702-enabled devnet
// with a dedicated throwaway whitelisted sender.

// A13 valid_7702_preconf (poison-safe): funder (whitelisted sender, pays gas, stays non-delegated)
// sends an EIP-7702 tx to Addr2 (already in topreconfs) whose authorization is signed by a disposable
// throwaway key — so the delegation lands on the throwaway account, never on funder/Addr1. If the
// 7702 fork isn't enabled / the type isn't preconf-eligible, records INCONCLUSIVE (funder unharmed).
func (r *Runner) scenarioValid7702Preconf(ctx context.Context) {
	const name = "valid_7702_preconf"
	z, err := tx.NewBuilder(r.tester.ChainID(), throwaway7702Key)
	if err != nil {
		r.recordInconclusive(name, "throwaway key: %v", err)
		return
	}
	zNonce, err := r.tester.GetNonce(ctx, r.seq, z.Address())
	if err != nil {
		r.record(name, false, "throwaway nonce: %v", err)
		return
	}
	// throwaway Z delegates its code to Addr2 (arbitrary; Z is disposable).
	auth, err := z.SignSetCodeAuth(types.SetCodeAuthorization{
		ChainID: *uint256.MustFromBig(r.tester.ChainID()),
		Address: Addr2,
		Nonce:   zNonce,
	})
	if err != nil {
		r.recordInconclusive(name, "sign 7702 auth: %v", err)
		return
	}
	feeCap, tip := r.fee1559(ctx)
	fn, err := r.nonce(ctx, r.funder.Address())
	if err != nil {
		r.record(name, false, "funder nonce: %v", err)
		return
	}
	to := Addr2 // whitelisted target (topreconfs)
	unsigned, err := r.funder.BuildEIP7702Tx(&tx.TxParams{
		Nonce: fn, MaxFeePerGas: feeCap, MaxPriorityFeePerGas: tip, Gas: 200_000,
		To: &to, Value: big.NewInt(0), AuthList: []types.SetCodeAuthorization{auth},
	})
	if err != nil {
		r.recordInconclusive(name, "build 7702: %v", err)
		return
	}
	signed, err := r.funder.SignTx(unsigned)
	if err != nil {
		r.recordInconclusive(name, "sign 7702: %v", err)
		return
	}
	resp, err := r.tester.SendRawTransactionWithPreconf(ctx, r.seq, signed, "7702")
	if err != nil {
		r.recordInconclusive(name, "7702 preconf 被拒（fork 未启用或类型不 eligible）: %v", err)
		return
	}
	if resp.Status != "success" {
		r.recordInconclusive(name, "7702 status=%s reason=%q", resp.Status, derefReason(resp))
		return
	}
	if _, ok := r.verifyOnChain(ctx, name, []promise{promiseFrom(resp)}); ok {
		r.record(name, true, "EIP-7702 preconf success + 链上兑现 blockHeight=%s（授权委托到 throwaway，funder 未被毒化）", resp.BlockHeight)
	}
}

// A14 preconf_nonce_gap: a gapped-nonce preconf (nonce+1, skipping the pending nonce) must not yield
// a false Success. geth queues/rejects it; reth synchronously rejects with NonceGap (rpc.rs:160). The
// gap is filled afterwards to keep funder state clean.
func (r *Runner) scenarioPreconfNonceGap(ctx context.Context) {
	const name = "preconf_nonce_gap"
	gp, err := r.gasPrice(ctx)
	if err != nil {
		r.record(name, false, "gas price: %v", err)
		return
	}
	r.waitQuiesce(ctx, r.funder.Address())
	n, err := r.nonce(ctx, r.funder.Address())
	if err != nil {
		r.record(name, false, "nonce: %v", err)
		return
	}
	gapped, err := r.signedLegacy(r.funder, &Addr2, smallValue, nil, 21000, n+1, gp)
	if err != nil {
		r.record(name, false, "build gapped: %v", err)
		return
	}
	resp, err := r.tester.SendRawTransactionWithPreconf(ctx, r.seq, gapped, "gap")
	falseSuccess := err == nil && resp != nil && resp.Status == "success"

	// fill nonce n so state stays clean (n executes; a queued n+1 then promotes)
	if fill, e := r.signedLegacy(r.funder, &Addr2, smallValue, nil, 21000, n, gp); e == nil {
		_, _ = r.tester.SendRawTransactionWithPreconf(ctx, r.seq, fill, "gap-fill")
	}

	if falseSuccess {
		// claimed success ⟹ must actually land (after the fill promotes it), else it's a false Success.
		if _, ok := r.verifyOnChain(ctx, name, []promise{promiseFrom(resp)}); !ok {
			return
		}
		r.record(name, true, "geth 对 gapped nonce 回 success 且补齐后确实上链（非假 Success）")
		return
	}
	got := "error: " + errStr(err)
	if err == nil {
		got = "status=" + resp.Status
	}
	r.record(name, true, "geth 对 gapped nonce 未发假 Success（%s）；reth 预期同步 NonceGap 拒", got)
}

// A15 preconf_gas_cap_over_2m: a preconf whose gas LIMIT exceeds reth's 2M per-tx cap. geth has no
// such cap → admits; documents the geth side of the Part B divergence (reth expected to reject).
func (r *Runner) scenarioPreconfGasCapOver2M(ctx context.Context) {
	const name = "preconf_gas_cap_over_2m"
	gp, err := r.gasPrice(ctx)
	if err != nil {
		r.record(name, false, "gas price: %v", err)
		return
	}
	n, err := r.nonce(ctx, r.funder.Address())
	if err != nil {
		r.record(name, false, "nonce: %v", err)
		return
	}
	// native transfer, gas LIMIT 3M (> reth 2M cap); executes ~21k, geth refunds the rest.
	signed, err := r.signedLegacy(r.funder, &Addr2, smallValue, nil, 3_000_000, n, gp)
	if err != nil {
		r.record(name, false, "build: %v", err)
		return
	}
	resp, err := r.tester.SendRawTransactionWithPreconf(ctx, r.seq, signed, "gascap")
	if err != nil {
		r.record(name, true, "geth 拒 3M-gas preconf（与预期不符，记录）: %v", err)
		return
	}
	if resp.Status != "success" {
		r.record(name, false, "3M-gas 原生 preconf 未 success: status=%s reason=%q", resp.Status, derefReason(resp))
		return
	}
	if _, ok := r.verifyOnChain(ctx, name, []promise{promiseFrom(resp)}); ok {
		r.record(name, true, "geth 无 per-tx gas cap：3M-gas 原生 preconf 被接纳并兑现（reth 2M cap 预期拒）")
	}
}

// A16 preconf_create_rejected: a contract-creation (to=nil) preconf must be rejected — IsPreconfTx's
// nil-to guard makes CREATE non-preconf-eligible.
func (r *Runner) scenarioPreconfCreateRejected(ctx context.Context) {
	const name = "preconf_create_rejected"
	gp, err := r.gasPrice(ctx)
	if err != nil {
		r.record(name, false, "gas price: %v", err)
		return
	}
	n, err := r.nonce(ctx, r.funder.Address())
	if err != nil {
		r.record(name, false, "nonce: %v", err)
		return
	}
	// to=nil (creation), minimal init code.
	signed, err := r.signedLegacy(r.funder, nil, big.NewInt(0), common.FromHex("0x6001600101"), 100_000, n, gp)
	if err != nil {
		r.record(name, false, "build: %v", err)
		return
	}
	_, err = r.tester.SendRawTransactionWithPreconf(ctx, r.seq, signed, "create")
	if err == nil {
		r.record(name, false, "CREATE(to=nil) preconf 被受理（应因 nil-to 非 preconf 而拒）")
		return
	}
	if !strings.Contains(err.Error(), "can't be submitted as preconf") {
		r.record(name, false, "CREATE 被拒但错误意外: %v", err)
		return
	}
	r.record(name, true, "CREATE(to=nil) preconf 被拒（nil-to guard）: %v", err)
}

// B verifier_forward_parity compares two verifiers forwarding to the same sequencer,
// so any difference is at the verifier layer. Extends B1's
// null-logs check to full-shape parity over a revert AND a success. Anchors the revert to `null` so
// two verifiers normalizing identically wouldn't false-pass.
func (r *Runner) scenarioVerifierForwardParity(ctx context.Context) {
	const name = "verifier_forward_parity"
	baselineLabel, targetLabel := r.baselineVerifier.Name(), r.targetVerifier.Name()
	gp, err := r.gasPrice(ctx)
	if err != nil {
		r.record(name, false, "gas price: %v", err)
		return
	}
	// (a) revert: TestPay.transferTo with a huge amount → "underflow balance sender".
	// (b) success: TestPay.transferTo(Addr3, Addr2, 1) → emits a Transfer log.
	revData := PayTransferToCalldata(Addr3, Addr2, hugeAmount)
	okData := PayTransferToCalldata(Addr3, Addr2, big.NewInt(1))

	for _, tc := range []struct {
		label      string
		data       []byte
		wantStatus string
	}{
		{"revert", revData, "failed"},
		{"success", okData, "success"},
	} {
		n1, e1 := r.tester.GetNonce(ctx, r.baselineVerifier, r.addr1.Address())
		if e1 != nil {
			r.record(name, false, "%s: addr1 nonce: %v", tc.label, e1)
			return
		}
		gTx, _ := r.signedLegacy(r.addr1, &TestPayAddr, nil, tc.data, 500_000, n1, gp)
		gResp, err := r.tester.SendRawTransactionWithPreconf(ctx, r.baselineVerifier, gTx, name+"/"+baselineLabel)
		if err != nil {
			r.record(name, false, "%s: %s send: %v", tc.label, baselineLabel, err)
			return
		}
		// Wait for n1 before the target-verifier send at n1+1 to avoid a nonce gap.
		_, _ = r.tester.WaitForReceipt(ctx, r.seq, common.HexToHash(gResp.TxHash), 30*time.Second)
		rTx, _ := r.signedLegacy(r.addr1, &TestPayAddr, nil, tc.data, 500_000, n1+1, gp)
		rResp, err := r.tester.SendRawTransactionWithPreconf(ctx, r.targetVerifier, rTx, name+"/"+targetLabel)
		if err != nil {
			r.record(name, false, "%s: %s send: %v", tc.label, targetLabel, err)
			return
		}
		_, _ = r.tester.WaitForReceipt(ctx, r.seq, common.HexToHash(rResp.TxHash), 30*time.Second)

		if gResp.Status != tc.wantStatus || rResp.Status != tc.wantStatus {
			r.record(name, false, "%s: status %s=%s %s=%s (want %s)", tc.label, baselineLabel, gResp.Status, targetLabel, rResp.Status, tc.wantStatus)
			return
		}
		if derefReason(gResp) != derefReason(rResp) {
			r.record(name, false, "%s: reason differs %s=%q %s=%q", tc.label, baselineLabel, derefReason(gResp), targetLabel, derefReason(rResp))
			return
		}
		if normalizeLogs(receiptLogsRaw(gResp)) != normalizeLogs(receiptLogsRaw(rResp)) {
			r.record(name, false, "%s: logs differ %s=%s %s=%s", tc.label, baselineLabel, receiptLogsRaw(gResp), targetLabel, receiptLogsRaw(rResp))
			return
		}
		// anchor: revert receipt.logs must be the expected `null` shape (not both normalized to []).
		if tc.label == "revert" && receiptLogsRaw(gResp) != "null" {
			r.record(name, false, "revert: %s receipt.logs=%q (expected null)", baselineLabel, receiptLogsRaw(gResp))
			return
		}
	}
	r.record(name, true, "%s vs %s forwarding parity: revert and success reasons and logs match", baselineLabel, targetLabel)
}

func errStr(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
