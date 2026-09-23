package preconf

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/ethereum-optimism/optimism/op-rpc-compat/pkg/rpc"
	"github.com/ethereum-optimism/optimism/op-rpc-compat/pkg/tx"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
)

// promise records a preconf that returned status=success, for later on-chain reconciliation.
// This is the "承诺账本" that turns the HARD invariant (Success ⟹ on-chain + receipt match) into a
// post-hoc check across any batch of preconf txs.
type promise struct {
	hash       common.Hash
	preLogs    string // receiptLogsRaw at preconf time ("null"/"[]"/"[...]" or "")
	preGasUsed uint64
	hasGasUsed bool // preconf response carried receipt.gasUsed (native success usually does NOT)
}

// promiseFrom builds a promise from a success PreconfResponse.
func promiseFrom(resp *tx.PreconfResponse) promise {
	p := promise{hash: common.HexToHash(resp.TxHash), preLogs: receiptLogsRaw(resp)}
	if len(resp.Receipt) > 0 {
		var rc struct {
			GasUsed string `json:"gasUsed"`
		}
		if json.Unmarshal(resp.Receipt, &rc) == nil && rc.GasUsed != "" {
			if g, err := hexutil.DecodeUint64(rc.GasUsed); err == nil {
				p.preGasUsed, p.hasGasUsed = g, true
			}
		}
	}
	return p
}

// onChainReceiptRaw fetches a receipt once (no poll). found=false if not yet mined (result null).
// New raw call — deliberately NOT extending tx.WaitForReceipt (its TxReceipt drops logs).
func (r *Runner) onChainReceiptRaw(ctx context.Context, client *rpc.Client, hash common.Hash) (status, blk, gasUsed uint64, logs string, found bool) {
	resp := client.Call(ctx, rpc.NewRequest("eth_getTransactionReceipt", []interface{}{hash.Hex()}))
	if resp.Error != nil || resp.Response == nil || resp.Response.Error != nil {
		return 0, 0, 0, "", false
	}
	if len(resp.Response.Result) == 0 || strings.TrimSpace(string(resp.Response.Result)) == "null" {
		return 0, 0, 0, "", false
	}
	var rc struct {
		Status      string          `json:"status"`
		BlockNumber string          `json:"blockNumber"`
		GasUsed     string          `json:"gasUsed"`
		Logs        json.RawMessage `json:"logs"`
	}
	if json.Unmarshal(resp.Response.Result, &rc) != nil {
		return 0, 0, 0, "", false
	}
	status, _ = hexutil.DecodeUint64(rc.Status)
	blk, _ = hexutil.DecodeUint64(rc.BlockNumber)
	gasUsed, _ = hexutil.DecodeUint64(rc.GasUsed)
	return status, blk, gasUsed, strings.TrimSpace(string(rc.Logs)), true
}

// verifyOnChain asserts every promised (status==success) preconf landed on-chain with a matching
// receipt. Records a FAIL on the first violation. Returns the per-hash landing block and ok=true if
// all promises were honored. Assertion set: 已上链 / status==1 / logs 一致 /（若响应带则）gasUsed 一致.
// "块==预测" is deliberately NOT asserted here (see doc §4 基建).
func (r *Runner) verifyOnChain(ctx context.Context, name string, ps []promise) (map[common.Hash]uint64, bool) {
	blocks := map[common.Hash]uint64{}
	for _, p := range ps {
		var status, blk, gasUsed uint64
		var logs string
		found := false
		for i := 0; i < 30; i++ {
			status, blk, gasUsed, logs, found = r.onChainReceiptRaw(ctx, r.seq, p.hash)
			if found {
				break
			}
			time.Sleep(time.Second)
		}
		if !found {
			r.record(name, false, "tx %s: 已回 Success 但链上无 receipt（假 Success）", p.hash.Hex())
			return blocks, false
		}
		if status != 1 {
			r.record(name, false, "tx %s: 链上 status=%d，预确认=success", p.hash.Hex(), status)
			return blocks, false
		}
		if !logsEquivalent(p.preLogs, logs) {
			r.record(name, false, "tx %s: logs 不一致 pre=%q chain=%q", p.hash.Hex(), p.preLogs, logs)
			return blocks, false
		}
		if p.hasGasUsed && gasUsed != p.preGasUsed {
			r.record(name, false, "tx %s: gasUsed 链上 %d != 预确认 %d", p.hash.Hex(), gasUsed, p.preGasUsed)
			return blocks, false
		}
		blocks[p.hash] = blk
	}
	return blocks, true
}

// logsEquivalent treats "", "null", "[]" as "no logs" (native transfers produce none), else compares
// exactly. Native-transfer receipts differ harmlessly between "[]" (chain) and "" (absent) forms.
func logsEquivalent(a, b string) bool {
	noneA := a == "" || a == "null" || a == "[]"
	noneB := b == "" || b == "null" || b == "[]"
	if noneA && noneB {
		return true
	}
	return a == b
}

// normalizeLogs reduces a receipt "logs" JSON to just the semantic content (address/topics/data) of
// each log, dropping block-position metadata (blockNumber/transactionHash/logIndex/…) that only
// exists after inclusion. Lets A6 compare a preconf receipt's logs to the on-chain receipt's logs
// without failing on the positional fields the preconf response legitimately lacks.
func normalizeLogs(raw string) string {
	if raw == "" || raw == "null" {
		return "[]"
	}
	var arr []map[string]json.RawMessage
	if json.Unmarshal([]byte(raw), &arr) != nil {
		return raw
	}
	var b strings.Builder
	for _, m := range arr {
		b.WriteString("{addr=")
		b.Write(m["address"])
		b.WriteString(",topics=")
		b.Write(m["topics"])
		b.WriteString(",data=")
		b.Write(m["data"])
		b.WriteString("}")
	}
	return strings.ToLower(b.String())
}

// rawNonce reads eth_getTransactionCount(addr, tag) off the sequencer.
func (r *Runner) rawNonce(ctx context.Context, addr common.Address, tag string) (uint64, bool) {
	resp := r.seq.Call(ctx, rpc.NewRequest("eth_getTransactionCount", []interface{}{addr.Hex(), tag}))
	if resp.Error != nil || resp.Response == nil || resp.Response.Error != nil {
		return 0, false
	}
	var h string
	if json.Unmarshal(resp.Response.Result, &h) != nil {
		return 0, false
	}
	n, err := hexutil.DecodeUint64(h)
	return n, err == nil
}

// waitQuiesce blocks until `addr` has no in-flight txs (latest nonce == pending nonce), so a
// subsequent balance snapshot isn't polluted by earlier scenarios' still-settling transfers.
func (r *Runner) waitQuiesce(ctx context.Context, addr common.Address) {
	for i := 0; i < 40; i++ {
		lat, ok1 := r.rawNonce(ctx, addr, "latest")
		pen, ok2 := r.rawNonce(ctx, addr, "pending")
		if ok1 && ok2 && lat == pen {
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
}
