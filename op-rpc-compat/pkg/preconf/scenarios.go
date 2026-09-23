package preconf

import (
	"context"
	"encoding/json"
	"math/big"
	"strings"
	"time"

	"github.com/ethereum-optimism/optimism/op-rpc-compat/pkg/rpc"
	"github.com/ethereum-optimism/optimism/op-rpc-compat/pkg/tx"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
)

var (
	// smallValue is the amount for native preconf transfers (0.0001 MNT).
	smallValue = big.NewInt(100_000_000_000_000)
	// hugeAmount far exceeds any ERC20 allowance/balance, so TestPay.transferTo reverts.
	hugeAmount = new(big.Int).Exp(big.NewInt(10), big.NewInt(30), nil)
)

// scenarioValidNativeSuccess: a whitelisted native preconf transfer must return status=success.
func (r *Runner) scenarioValidNativeSuccess(ctx context.Context) {
	const name = "valid_native_success"
	resp, err := r.sendPreconfNative(ctx, r.seq, r.funder, Addr2, smallValue)
	if err != nil {
		r.record(name, false, "send error: %v", err)
		return
	}
	if resp.Status != "success" {
		r.record(name, false, "status=%s reason=%q", resp.Status, derefReason(resp))
		return
	}
	r.record(name, true, "status=success blockHeight=%s", resp.BlockHeight)
}

// scenarioReasons: each distinct preconf failure surfaces the expected reason / error.
func (r *Runner) scenarioReasons(ctx context.Context) {
	// allowance insufficient: amount must exceed Addr3's (large, approved) allowance, so read it and
	// go one over. transferTo checks allowance before balance, so this reverts with the allowance
	// message even though amount also exceeds the balance.
	if allowance, err := r.callERC20Uint(ctx, ERC20AllowanceCalldata(Addr3, TestPayAddr)); err != nil {
		r.record("reason_allowance_insufficient", false, "read allowance: %v", err)
	} else {
		data := PayTransferToCalldata(Addr3, Addr2, new(big.Int).Add(allowance, big.NewInt(1)))
		if resp, err := r.sendPreconfCallIncluded(ctx, r.seq, r.addr1, TestPayAddr, data, 500_000); err != nil {
			r.record("reason_allowance_insufficient", false, "send error: %v", err)
		} else if resp.Status != "failed" || !strings.Contains(derefReason(resp), "allowance insufficient") {
			r.record("reason_allowance_insufficient", false, "status=%s reason=%q", resp.Status, derefReason(resp))
		} else {
			r.record("reason_allowance_insufficient", true, "reason=%q", derefReason(resp))
		}
	}

	// out of gas: with allowance+balance in place (ensureERC20State), a small-amount transferTo runs
	// past the checks into transferFrom, then a low gas cap runs it out of gas mid-execution.
	oogData := PayTransferToCalldata(Addr3, Addr2, big.NewInt(1))
	if resp, err := r.sendPreconfCallIncluded(ctx, r.seq, r.addr1, TestPayAddr, oogData, 30_000); err != nil {
		r.record("reason_out_of_gas", false, "send error: %v", err)
	} else if resp.Status != "failed" || !strings.Contains(derefReason(resp), "out of gas") {
		r.record("reason_out_of_gas", false, "status=%s reason=%q", resp.Status, derefReason(resp))
	} else {
		r.record("reason_out_of_gas", true, "reason=%q", derefReason(resp))
	}

	// underflow balance sender: allowance is sufficient (approved) but amount exceeds Addr3's ERC20
	// balance → the balance check reverts.
	if bal, err := r.callERC20Uint(ctx, ERC20BalanceOfCalldata(Addr3)); err != nil {
		r.record("reason_underflow_balance", false, "read balance: %v", err)
	} else {
		overData := PayTransferToCalldata(Addr3, Addr2, new(big.Int).Add(bal, big.NewInt(1)))
		if resp, err := r.sendPreconfCallIncluded(ctx, r.seq, r.addr1, TestPayAddr, overData, 500_000); err != nil {
			r.record("reason_underflow_balance", false, "send error: %v", err)
		} else if resp.Status != "failed" || !strings.Contains(derefReason(resp), "underflow balance sender") {
			r.record("reason_underflow_balance", false, "status=%s reason=%q", resp.Status, derefReason(resp))
		} else {
			r.record("reason_underflow_balance", true, "reason=%q", derefReason(resp))
		}
	}

	// intrinsic gas too low: gas below intrinsic → RPC error before inclusion.
	if _, err := r.sendPreconfCall(ctx, r.seq, r.addr1, TestPayAddr, PayTransferToCalldata(Addr3, Addr2, big.NewInt(1)), 1); err == nil {
		r.record("reason_intrinsic_gas_too_low", false, "expected an RPC error, got success")
	} else if !strings.Contains(err.Error(), "intrinsic gas too low") {
		r.record("reason_intrinsic_gas_too_low", false, "unexpected error: %v", err)
	} else {
		r.record("reason_intrinsic_gas_too_low", true, "error contains 'intrinsic gas too low'")
	}

	// insufficient funds: native transfer with value > balance → RPC error.
	bal, err := r.tester.GetBalance(ctx, r.seq, r.addr1.Address(), "latest")
	if err != nil {
		r.record("reason_insufficient_value", false, "get balance: %v", err)
	} else {
		over := new(big.Int).Add(bal, oneMNT)
		if _, err := r.sendPreconfNative(ctx, r.seq, r.addr1, Addr2, over); err == nil {
			r.record("reason_insufficient_value", false, "expected an RPC error, got success")
		} else if !strings.Contains(err.Error(), "insufficient funds") {
			r.record("reason_insufficient_value", false, "unexpected error: %v", err)
		} else {
			r.record("reason_insufficient_value", true, "error contains 'insufficient funds'")
		}
	}

	// nonce too low: resubmit an already-consumed nonce → RPC error (front-running / ordering guard,
	// ported from transfer.go). Uses the funder, whose state nonce is well above 0 by now.
	if n, err := r.nonce(ctx, r.funder.Address()); err != nil {
		r.record("reason_nonce_too_low", false, "nonce: %v", err)
	} else if n == 0 {
		r.record("reason_nonce_too_low", false, "funder nonce is 0, cannot form a too-low nonce")
	} else {
		gp, _ := r.gasPrice(ctx)
		signed, err := r.signedLegacy(r.funder, &Addr2, smallValue, nil, 21000, n-1, gp)
		if err != nil {
			r.record("reason_nonce_too_low", false, "build: %v", err)
		} else if _, err := r.tester.SendRawTransactionWithPreconf(ctx, r.seq, signed, "nonce_too_low"); err == nil {
			r.record("reason_nonce_too_low", false, "expected an RPC error, got success")
		} else if !strings.Contains(err.Error(), "nonce too low") {
			r.record("reason_nonce_too_low", false, "unexpected error: %v", err)
		} else {
			r.record("reason_nonce_too_low", true, "error contains 'nonce too low'")
		}
	}
}

// scenarioPredictedBlockMatches: the preconf-predicted blockHeight equals the actual inclusion block.
func (r *Runner) scenarioPredictedBlockMatches(ctx context.Context) {
	const name = "predicted_block_matches_actual"
	const rounds = 3
	for i := 0; i < rounds; i++ {
		resp, err := r.sendPreconfNative(ctx, r.seq, r.funder, Addr2, smallValue)
		if err != nil {
			r.record(name, false, "round %d send error: %v", i, err)
			return
		}
		predicted, err := hexutil.DecodeUint64(resp.BlockHeight)
		if err != nil {
			r.record(name, false, "round %d bad blockHeight %q: %v", i, resp.BlockHeight, err)
			return
		}
		status, actual, err := r.waitReceiptStatus(ctx, common.HexToHash(resp.TxHash), 30*time.Second)
		if err != nil {
			r.record(name, false, "round %d receipt: %v", i, err)
			return
		}
		if status != 1 {
			r.record(name, false, "round %d on-chain status=%d (expected success)", i, status)
			return
		}
		if predicted != actual {
			r.record(name, false, "round %d predicted block %d != actual %d", i, predicted, actual)
			return
		}
	}
	r.record(name, true, "%d/%d preconf txs landed in their predicted block", rounds, rounds)
}

// scenarioGethRethParity: a reverting preconf tx returns byte-identical receipt.logs shape on the
// geth sequencer and the reth forwarding node (both `null`). Guards the logs:null fix.
func (r *Runner) scenarioGethRethParity(ctx context.Context) {
	const name = "geth_reth_parity_null_logs"
	payData := PayTransferToCalldata(Addr3, Addr2, hugeAmount)

	gp, err := r.gasPrice(ctx)
	if err != nil {
		r.record(name, false, "gas price: %v", err)
		return
	}
	n, err := r.nonce(ctx, r.addr1.Address())
	if err != nil {
		r.record(name, false, "nonce: %v", err)
		return
	}

	// nonce n via the geth sequencer, nonce n+1 via the reth node (sequential to avoid a nonce gap).
	gethTx, err := r.signedLegacy(r.addr1, &TestPayAddr, nil, payData, 500_000, n, gp)
	if err != nil {
		r.record(name, false, "build geth tx: %v", err)
		return
	}
	rethTx, err := r.signedLegacy(r.addr1, &TestPayAddr, nil, payData, 500_000, n+1, gp)
	if err != nil {
		r.record(name, false, "build reth tx: %v", err)
		return
	}

	gethResp, err := r.tester.SendRawTransactionWithPreconf(ctx, r.seq, gethTx, name+"/geth")
	if err != nil {
		r.record(name, false, "geth send error: %v", err)
		return
	}
	// wait for nonce n to land before submitting n+1 via reth (avoid a nonce gap)
	_, _ = r.tester.WaitForReceipt(ctx, r.seq, common.HexToHash(gethResp.TxHash), 30*time.Second)
	rethResp, err := r.tester.SendRawTransactionWithPreconf(ctx, r.reth, rethTx, name+"/reth")
	if err != nil {
		r.record(name, false, "reth send error (regression? pre-fix this was -32000): %v", err)
		return
	}

	if gethResp.Status != rethResp.Status {
		r.record(name, false, "status differs: geth=%s reth=%s", gethResp.Status, rethResp.Status)
		return
	}
	if derefReason(gethResp) != derefReason(rethResp) {
		r.record(name, false, "reason differs: geth=%q reth=%q", derefReason(gethResp), derefReason(rethResp))
		return
	}
	gethLogs := receiptLogsRaw(gethResp)
	rethLogs := receiptLogsRaw(rethResp)
	if gethLogs != rethLogs {
		r.record(name, false, "receipt.logs shape differs: geth=%s reth=%s", gethLogs, rethLogs)
		return
	}
	r.record(name, true, "status=%s reason=%q receipt.logs=%s (identical)", gethResp.Status, derefReason(gethResp), gethLogs)
}

// ─── small helpers ───────────────────────────────────────────────────────────

// sendPreconf signs a tx from `b` (fresh pending nonce) and submits it via
// eth_sendRawTransactionWithPreconf on `client`.
func (r *Runner) sendPreconf(ctx context.Context, client *rpc.Client, b *tx.Builder, to *common.Address, value *big.Int, data []byte, gas uint64) (*tx.PreconfResponse, error) {
	gp, err := r.gasPrice(ctx)
	if err != nil {
		return nil, err
	}
	n, err := r.tester.GetNonce(ctx, r.seq, b.Address())
	if err != nil {
		return nil, err
	}
	signed, err := r.signedLegacy(b, to, value, data, gas, n, gp)
	if err != nil {
		return nil, err
	}
	return r.tester.SendRawTransactionWithPreconf(ctx, client, signed, "preconf")
}

// sendPreconfNative submits a native-transfer preconf.
func (r *Runner) sendPreconfNative(ctx context.Context, client *rpc.Client, b *tx.Builder, to common.Address, value *big.Int) (*tx.PreconfResponse, error) {
	return r.sendPreconf(ctx, client, b, &to, value, nil, 21000)
}

// sendPreconfCall submits a contract-call preconf with an explicit gas cap.
func (r *Runner) sendPreconfCall(ctx context.Context, client *rpc.Client, b *tx.Builder, to common.Address, data []byte, gas uint64) (*tx.PreconfResponse, error) {
	return r.sendPreconf(ctx, client, b, &to, nil, data, gas)
}

// sendPreconfCallIncluded is like sendPreconfCall but, on success, waits for the tx to land so the
// sender's state nonce advances before the next send (avoids a "nonce too high" gap when firing
// several preconf txs from the same account back-to-back).
func (r *Runner) sendPreconfCallIncluded(ctx context.Context, client *rpc.Client, b *tx.Builder, to common.Address, data []byte, gas uint64) (*tx.PreconfResponse, error) {
	resp, err := r.sendPreconfCall(ctx, client, b, to, data, gas)
	if err != nil {
		return resp, err
	}
	_, _ = r.tester.WaitForReceipt(ctx, r.seq, common.HexToHash(resp.TxHash), 30*time.Second)
	return resp, nil
}

func derefReason(resp *tx.PreconfResponse) string {
	if resp != nil && resp.Reason != nil {
		return *resp.Reason
	}
	return ""
}

// receiptLogsRaw returns the raw JSON of the receipt's "logs" field ("null", "[]", or "[...]").
func receiptLogsRaw(resp *tx.PreconfResponse) string {
	if resp == nil || len(resp.Receipt) == 0 {
		return ""
	}
	var r struct {
		Logs json.RawMessage `json:"logs"`
	}
	if err := json.Unmarshal(resp.Receipt, &r); err != nil {
		return ""
	}
	return strings.TrimSpace(string(r.Logs))
}
