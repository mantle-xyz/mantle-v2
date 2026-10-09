package tx

import (
	"context"
	"fmt"
	"math/big"
	"time"

	"github.com/ethereum-optimism/optimism/op-rpc-compat/pkg/diff"
	"github.com/ethereum-optimism/optimism/op-rpc-compat/pkg/report"
	"github.com/ethereum-optimism/optimism/op-rpc-compat/pkg/rpc"

	"github.com/ethereum/go-ethereum/common"
)

// traceTransferGasLimit leaves gas unused above the 21000 intrinsic cost of a
// plain transfer, so a root frame's gas and gasUsed carry different values.
const traceTransferGasLimit = 50000

// TraceTracerConfig names one tracer configuration for debug_traceTransaction.
type TraceTracerConfig struct {
	Label   string
	Options map[string]interface{}
}

// DefaultTraceTracerConfigs lists the built-in tracer configurations compared
// on a mined user transaction. A plain transfer has no execution gas, so the
// root gasUsed of every configuration is the transaction's intrinsic cost.
func DefaultTraceTracerConfigs() []TraceTracerConfig {
	return []TraceTracerConfig{
		{Label: "callTracer", Options: map[string]interface{}{"tracer": "callTracer"}},
		{Label: "flatCallTracer", Options: map[string]interface{}{"tracer": "flatCallTracer"}},
		{Label: "muxTracer_callTracer_flatCallTracer", Options: map[string]interface{}{
			"tracer": "muxTracer",
			"tracerConfig": map[string]interface{}{
				"callTracer":     map[string]interface{}{},
				"flatCallTracer": map[string]interface{}{},
			},
		}},
	}
}

// TestTraceTransfer sends one native transfer through the baseline endpoint,
// waits until both endpoints have its receipt, and compares
// debug_traceTransaction for that same transaction hash under each tracer
// configuration. Unlike the transfer tests, both sides trace one transaction,
// so every field of the trace, including hashes and block position, must match.
func (t *Tester) TestTraceTransfer(
	ctx context.Context,
	recipient common.Address,
	amount *big.Int,
	configs []TraceTracerConfig,
) []*TxTestResult {
	const testName = "trace_transfer"
	failAll := func(reason string, inconclusive bool) []*TxTestResult {
		results := make([]*TxTestResult, 0, len(configs))
		for _, cfg := range configs {
			results = append(results, &TxTestResult{
				TestName:     fmt.Sprintf("%s_%s", testName, cfg.Label),
				TxType:       "Trace",
				Inconclusive: inconclusive,
				Error:        reason,
			})
		}
		return results
	}

	nonce, err := t.GetNonce(ctx, t.baselineClient, t.builder.Address())
	if err != nil {
		return failAll(fmt.Sprintf("get nonce: %v", err), false)
	}
	gasPrice, err := t.GetGasPrice(ctx, t.baselineClient)
	if err != nil {
		return failAll(fmt.Sprintf("get gas price: %v", err), false)
	}
	maxPriorityFee := big.NewInt(1000000000) // 1 gwei
	signed, err := t.builder.BuildAndSign(TxTypeEIP1559, &TxParams{
		From:                 t.builder.Address(),
		To:                   &recipient,
		Value:                amount,
		Gas:                  traceTransferGasLimit,
		MaxFeePerGas:         new(big.Int).Add(gasPrice, maxPriorityFee),
		MaxPriorityFeePerGas: maxPriorityFee,
		Nonce:                nonce,
		ChainID:              t.chainID,
	})
	if err != nil {
		return failAll(fmt.Sprintf("build transaction: %v", err), false)
	}
	txHash, err := t.SendRawTransaction(ctx, t.baselineClient, signed, testName+"_send")
	if err != nil {
		return failAll(fmt.Sprintf("send transaction: %v", err), false)
	}

	// Both endpoints must have the block before the traces are comparable.
	for _, client := range []*rpc.Client{t.baselineClient, t.targetClient} {
		if _, err := t.WaitForReceipt(ctx, client, txHash, 60*time.Second); err != nil {
			return failAll(fmt.Sprintf("wait for receipt on %s: %v", client.Name(), err), true)
		}
	}

	results := make([]*TxTestResult, 0, len(configs))
	for _, cfg := range configs {
		results = append(results, t.compareTrace(ctx, txHash, cfg, fmt.Sprintf("%s_%s", testName, cfg.Label)))
	}
	return results
}

func (t *Tester) compareTrace(ctx context.Context, txHash common.Hash, cfg TraceTracerConfig, name string) *TxTestResult {
	params := []interface{}{txHash.Hex(), cfg.Options}
	req := rpc.NewRequest("debug_traceTransaction", params)
	tc := report.TestCase{Name: name, Method: "debug_traceTransaction", Params: params}
	pair := &rpc.ClientPair{Baseline: t.baselineClient, Target: t.targetClient}
	compareResult := pair.Compare(ctx, req)

	result := &TxTestResult{
		TestName:       name,
		TxType:         "Trace",
		BaselineTxHash: txHash.Hex(),
		TargetTxHash:   txHash.Hex(),
	}

	baselineResp, targetResp := compareResult.BaselineResponse, compareResult.TargetResponse
	if !rpc.SuccessfulResponse(baselineResp) || !rpc.SuccessfulResponse(targetResp) {
		if t.reporter != nil {
			t.reporter.AddResult(tc, compareResult, nil, nil)
		}
		result.Error = fmt.Sprintf("trace request failed: baseline=%s target=%s",
			responseSummary(baselineResp), responseSummary(targetResp))
		return result
	}

	diffResult, compareErr := diff.Compare(
		normalizeRawResponse(baselineResp.RawBody, false),
		normalizeRawResponse(targetResp.RawBody, false),
		diff.DefaultOptions(),
	)
	if t.reporter != nil {
		t.reporter.AddResult(tc, compareResult, diffResult, compareErr)
	}
	switch {
	case compareErr != nil:
		result.Error = fmt.Sprintf("compare traces: %v", compareErr)
	case diffResult != nil && !diffResult.IsEqual:
		result.Error = fmt.Sprintf("traces differ in %d field(s)", diffResult.DiffFields)
	default:
		result.Passed = true
	}
	return result
}

func responseSummary(resp *rpc.ResponseWithMeta) string {
	switch {
	case resp == nil:
		return "no response"
	case resp.Error != nil:
		return resp.Error.Error()
	case resp.Response == nil:
		return "empty response"
	case resp.Response.Error != nil:
		return fmt.Sprintf("code=%d message=%s", resp.Response.Error.Code, resp.Response.Error.Message)
	default:
		return "ok"
	}
}
