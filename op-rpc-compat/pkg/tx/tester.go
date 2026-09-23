package tx

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/ethereum-optimism/optimism/op-rpc-compat/pkg/diff"
	"github.com/ethereum-optimism/optimism/op-rpc-compat/pkg/report"
	"github.com/ethereum-optimism/optimism/op-rpc-compat/pkg/rpc"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/holiman/uint256"
)

// Tester runs transaction scenarios against two RPC clients.
type Tester struct {
	baselineClient *rpc.Client
	targetClient   *rpc.Client
	builder        *Builder
	chainID        *big.Int
	reporter       *report.Reporter // records RPC outcomes
}

// NewTester creates a transaction tester.
func NewTester(pair *rpc.ClientPair, privateKeyHex string, reporter *report.Reporter) (*Tester, error) {
	if pair == nil || pair.Baseline == nil || pair.Target == nil {
		return nil, fmt.Errorf("transaction tester requires baseline and target clients")
	}
	baselineClient, targetClient := pair.Baseline, pair.Target

	ctx := context.Background()
	chainIDResp := baselineClient.Call(ctx, rpc.NewRequest("eth_chainId", nil))
	if chainIDResp.Error != nil {
		return nil, fmt.Errorf("%s eth_chainId: %w", baselineClient.Name(), chainIDResp.Error)
	}
	if chainIDResp.Response == nil {
		return nil, fmt.Errorf("%s eth_chainId returned no response", baselineClient.Name())
	}
	if chainIDResp.Response.Error != nil {
		return nil, fmt.Errorf("%s eth_chainId RPC error %d: %s", baselineClient.Name(), chainIDResp.Response.Error.Code, chainIDResp.Response.Error.Message)
	}

	var chainIDHex string
	if err := json.Unmarshal(chainIDResp.Response.Result, &chainIDHex); err != nil {
		return nil, fmt.Errorf("解析 chainID 失败: %w", err)
	}
	chainID, err := hexutil.DecodeBig(chainIDHex)
	if err != nil {
		return nil, fmt.Errorf("%s eth_chainId invalid result %q: %w", baselineClient.Name(), chainIDHex, err)
	}

	builder, err := NewBuilder(chainID, privateKeyHex)
	if err != nil {
		return nil, err
	}

	return &Tester{
		baselineClient: baselineClient,
		targetClient:   targetClient,
		builder:        builder,
		chainID:        chainID,
		reporter:       reporter,
	}, nil
}

// GetNonce returns an account's nonce from one client.
func (t *Tester) GetNonce(ctx context.Context, client *rpc.Client, address common.Address) (uint64, error) {
	resp := client.Call(ctx, rpc.NewRequest("eth_getTransactionCount", []interface{}{address.Hex(), "pending"}))
	if resp.Error != nil {
		return 0, resp.Error
	}
	if resp.Response.Error != nil {
		return 0, fmt.Errorf("RPC error: %s", resp.Response.Error.Message)
	}

	var nonceHex string
	if err := json.Unmarshal(resp.Response.Result, &nonceHex); err != nil {
		return 0, err
	}

	nonce, err := hexutil.DecodeUint64(nonceHex)
	if err != nil {
		return 0, err
	}
	return nonce, nil
}

// GetBalance returns an account's balance from one client without comparing responses.
func (t *Tester) GetBalance(ctx context.Context, client *rpc.Client, address common.Address, block string) (*big.Int, error) {
	resp := client.Call(ctx, rpc.NewRequest("eth_getBalance", []interface{}{address.Hex(), block}))
	if resp.Error != nil {
		return nil, resp.Error
	}
	if resp.Response.Error != nil {
		return nil, fmt.Errorf("RPC error: %s", resp.Response.Error.Message)
	}

	var balanceHex string
	if err := json.Unmarshal(resp.Response.Result, &balanceHex); err != nil {
		return nil, err
	}

	balance, err := hexutil.DecodeBig(balanceHex)
	if err != nil {
		return nil, err
	}
	return balance, nil
}

// GetBalanceAndCompare compares account balances and returns the reference value.
func (t *Tester) GetBalanceAndCompare(ctx context.Context, address common.Address, block string, testName string) (*big.Int, error) {
	req := rpc.NewRequest("eth_getBalance", []interface{}{address.Hex(), block})

	baselineResp := t.baselineClient.Call(ctx, req)
	targetResp := t.targetClient.Call(ctx, req)

	if t.reporter != nil {
		compareResult := &rpc.CompareResult{
			Request:          req,
			BaselineResponse: baselineResp,
			TargetResponse:   targetResp,
		}

		var diffResult *diff.CompareResult
		var compareErr error
		if baselineResp.Response.Error == nil && targetResp.Response.Error == nil {
			baselineRaw := normalizeRawResponse(baselineResp.RawBody, false)
			targetRaw := normalizeRawResponse(targetResp.RawBody, false)
			diffResult, compareErr = diff.Compare(
				baselineRaw,
				targetRaw,
				diff.DefaultOptions(),
			)
		}

		tc := report.TestCase{
			Name:   testName,
			Method: "eth_getBalance",
			Params: []interface{}{address.Hex(), block},
		}
		t.reporter.AddResult(tc, compareResult, diffResult, compareErr)
	}

	// Subsequent transaction construction uses the reference client's value.
	if baselineResp.Error != nil {
		return nil, baselineResp.Error
	}
	if baselineResp.Response.Error != nil {
		return nil, fmt.Errorf("RPC error: %s", baselineResp.Response.Error.Message)
	}

	var balanceHex string
	if err := json.Unmarshal(baselineResp.Response.Result, &balanceHex); err != nil {
		return nil, err
	}

	balance, err := hexutil.DecodeBig(balanceHex)
	if err != nil {
		return nil, err
	}
	return balance, nil
}

// GetGasPrice returns the gas price reported by one client.
func (t *Tester) GetGasPrice(ctx context.Context, client *rpc.Client) (*big.Int, error) {
	resp := client.Call(ctx, rpc.NewRequest("eth_gasPrice", nil))
	if resp.Error != nil {
		return nil, resp.Error
	}
	if resp.Response.Error != nil {
		return nil, fmt.Errorf("RPC error: %s", resp.Response.Error.Message)
	}

	var gasPriceHex string
	if err := json.Unmarshal(resp.Response.Result, &gasPriceHex); err != nil {
		return nil, err
	}

	gasPrice, err := hexutil.DecodeBig(gasPriceHex)
	if err != nil {
		return nil, err
	}
	return gasPrice, nil
}

// EstimateGas estimates gas for a transaction on one client.
func (t *Tester) EstimateGas(ctx context.Context, client *rpc.Client, params map[string]interface{}) (uint64, error) {
	resp := client.Call(ctx, rpc.NewRequest("eth_estimateGas", []interface{}{params}))
	if resp.Error != nil {
		return 0, resp.Error
	}
	if resp.Response.Error != nil {
		return 21000, nil // default intrinsic gas
	}

	var gasHex string
	if err := json.Unmarshal(resp.Response.Result, &gasHex); err != nil {
		return 21000, nil
	}

	gas, err := hexutil.DecodeUint64(gasHex)
	if err != nil {
		return 21000, nil
	}
	return gas, nil
}

// SendRawTransaction submits a signed transaction and records the RPC outcome.
// Each client receives a different transaction, so these calls are not compared directly.
func (t *Tester) SendRawTransaction(ctx context.Context, client *rpc.Client, signedTx *types.Transaction, testName string) (common.Hash, error) {
	return t.sendRawTransaction(ctx, client, signedTx, testName, false)
}

func (t *Tester) sendRawTransactionAllowAlreadyKnown(
	ctx context.Context,
	client *rpc.Client,
	signedTx *types.Transaction,
	testName string,
) (common.Hash, error) {
	return t.sendRawTransaction(ctx, client, signedTx, testName, true)
}

func (t *Tester) sendRawTransaction(
	ctx context.Context,
	client *rpc.Client,
	signedTx *types.Transaction,
	testName string,
	allowAlreadyKnown bool,
) (common.Hash, error) {
	rawTx, err := signedTx.MarshalBinary()
	if err != nil {
		return common.Hash{}, fmt.Errorf("序列化交易失败: %w", err)
	}

	req := rpc.NewRequest("eth_sendRawTransaction", []interface{}{hexutil.Encode(rawTx)})
	resp := client.Call(ctx, req)
	var responseErr error
	if resp.Error != nil {
		responseErr = resp.Error
	} else if resp.Response.Error != nil {
		responseErr = fmt.Errorf("RPC error: code=%d, message=%s", resp.Response.Error.Code, resp.Response.Error.Message)
	}
	acceptedAlreadyKnown := allowAlreadyKnown && isAlreadyKnownError(responseErr)

	// Record this response without comparing it to the other client's transaction.
	if t.reporter != nil {
		// A one-sided CompareResult retains the actual response for the report.
		var compareResult *rpc.CompareResult
		if client == t.baselineClient {
			compareResult = &rpc.CompareResult{
				Request:          req,
				BaselineResponse: resp,
				TargetResponse:   nil, // the target receives a different transaction
			}
		} else {
			compareResult = &rpc.CompareResult{
				Request:          req,
				BaselineResponse: nil, // the reference receives a different transaction
				TargetResponse:   resp,
			}
		}

		tc := report.TestCase{
			Name:   testName,
			Method: "eth_sendRawTransaction",
			Params: []interface{}{hexutil.Encode(rawTx)},
		}
		if acceptedAlreadyKnown {
			t.reporter.AddCompatibleResult(
				tc,
				compareResult,
				"同一交易已由另一转发节点提交到共享 sequencer；already known 视为幂等受理，后续继续校验本地 txpool 可见性",
			)
		} else {
			t.reporter.AddResult(tc, compareResult, nil, nil)
		}
	}

	if responseErr != nil {
		if acceptedAlreadyKnown {
			return signedTx.Hash(), nil
		}
		return common.Hash{}, responseErr
	}

	var txHashHex string
	if err := json.Unmarshal(resp.Response.Result, &txHashHex); err != nil {
		return common.Hash{}, err
	}

	return common.HexToHash(txHashHex), nil
}

// SendRawTransactionWithPreconf submits a preconfirmed transaction and records its RPC outcome.
// Each client receives a different transaction, so these calls are not compared directly.
func (t *Tester) SendRawTransactionWithPreconf(ctx context.Context, client *rpc.Client, signedTx *types.Transaction, testName string) (*PreconfResponse, error) {
	rawTx, err := signedTx.MarshalBinary()
	if err != nil {
		return nil, fmt.Errorf("序列化交易失败: %w", err)
	}

	req := rpc.NewRequest("eth_sendRawTransactionWithPreconf", []interface{}{hexutil.Encode(rawTx)})
	resp := client.Call(ctx, req)

	// Record this response without comparing it to the other client's transaction.
	if t.reporter != nil {
		// A one-sided CompareResult retains the actual response for the report.
		var compareResult *rpc.CompareResult
		if client == t.baselineClient {
			compareResult = &rpc.CompareResult{
				Request:          req,
				BaselineResponse: resp,
				TargetResponse:   nil, // the target receives a different transaction
			}
		} else {
			compareResult = &rpc.CompareResult{
				Request:          req,
				BaselineResponse: nil, // the reference receives a different transaction
				TargetResponse:   resp,
			}
		}

		tc := report.TestCase{
			Name:   testName,
			Method: "eth_sendRawTransactionWithPreconf",
			Params: []interface{}{hexutil.Encode(rawTx)},
		}
		t.reporter.AddResult(tc, compareResult, nil, nil)
	}

	if resp.Error != nil {
		return nil, resp.Error
	}
	if resp.Response.Error != nil {
		return nil, fmt.Errorf("RPC error: code=%d, message=%s", resp.Response.Error.Code, resp.Response.Error.Message)
	}

	var preconfResp PreconfResponse
	if err := json.Unmarshal(resp.Response.Result, &preconfResp); err != nil {
		return nil, err
	}

	return &preconfResp, nil
}

// WaitForReceipt polls one client for a transaction receipt without comparison.
func (t *Tester) WaitForReceipt(ctx context.Context, client *rpc.Client, txHash common.Hash, timeout time.Duration) (*TxReceipt, error) {
	deadline := time.Now().Add(timeout)

	for time.Now().Before(deadline) {
		resp := client.Call(ctx, rpc.NewRequest("eth_getTransactionReceipt", []interface{}{txHash.Hex()}))
		if resp.Error != nil {
			time.Sleep(500 * time.Millisecond)
			continue
		}
		if resp.Response.Error != nil {
			time.Sleep(500 * time.Millisecond)
			continue
		}

		if string(resp.Response.Result) == "null" {
			time.Sleep(500 * time.Millisecond)
			continue
		}

		var rawReceipt map[string]interface{}
		if err := json.Unmarshal(resp.Response.Result, &rawReceipt); err != nil {
			time.Sleep(500 * time.Millisecond)
			continue
		}

		receipt := &TxReceipt{
			TransactionHash: txHash.Hex(),
		}

		if status, ok := rawReceipt["status"].(string); ok {
			receipt.Status, _ = hexutil.DecodeUint64(status)
		}
		if gasUsed, ok := rawReceipt["gasUsed"].(string); ok {
			receipt.GasUsed, _ = hexutil.DecodeUint64(gasUsed)
		}
		if blockNumber, ok := rawReceipt["blockNumber"].(string); ok {
			receipt.BlockNumber, _ = hexutil.DecodeUint64(blockNumber)
		}
		if cumulativeGasUsed, ok := rawReceipt["cumulativeGasUsed"].(string); ok {
			receipt.CumulativeGasUsed, _ = hexutil.DecodeUint64(cumulativeGasUsed)
		}
		if contractAddress, ok := rawReceipt["contractAddress"].(string); ok {
			receipt.ContractAddress = contractAddress
		}

		return receipt, nil
	}

	return nil, fmt.Errorf("等待交易确认超时: %s", txHash.Hex())
}

// normalizeRawResponse removes the request ID and environment-dependent fields.
func normalizeRawResponse(data []byte, isReceipt bool) []byte {
	var m map[string]interface{}
	if err := json.Unmarshal(data, &m); err != nil {
		return data
	}
	delete(m, "id")

	if isReceipt {
		if res, ok := m["result"].(map[string]interface{}); ok {
			delete(res, "blockHash")
			delete(res, "blockNumber")
			delete(res, "transactionHash")
			delete(res, "transactionIndex")
			delete(res, "cumulativeGasUsed")
			delete(res, "from")
			delete(res, "to")
			delete(res, "contractAddress")
			delete(res, "logsBloom")
			delete(res, "root")
			// Cause tokenRatio is different
			delete(res, "l1Fee")
			delete(res, "tokenRatio")
			// Cause signature is different
			delete(res, "l1GasUsed")
			delete(res, "blobGasUsed")
			// Cause l1GasPrice is different
			delete(res, "l1GasPrice")
			delete(res, "effectiveGasPrice")
		}
	}

	normalized, _ := json.Marshal(m)
	return normalized
}

// CompareReceipts compares receipts for distinct transactions sent through each client.
func (t *Tester) CompareReceipts(ctx context.Context, testName string, baselineTxHash, targetTxHash common.Hash) (bool, error) {
	if t.reporter == nil {
		return true, nil
	}

	baselineReq := rpc.NewRequest("eth_getTransactionReceipt", []interface{}{baselineTxHash.Hex()})
	baselineResp := t.baselineClient.Call(ctx, baselineReq)

	targetReq := rpc.NewRequest("eth_getTransactionReceipt", []interface{}{targetTxHash.Hex()})
	targetResp := t.targetClient.Call(ctx, targetReq)

	// Pair the two receipt responses even though their transaction hashes differ.
	compareResult := &rpc.CompareResult{
		Request:          baselineReq, // retain the reference request in the report
		BaselineResponse: baselineResp,
		TargetResponse:   targetResp,
	}

	var diffResult *diff.CompareResult
	var compareErr error

	if baselineResp.Response.Error == nil && targetResp.Response.Error == nil {
		baselineRaw := normalizeRawResponse(baselineResp.RawBody, true)
		targetRaw := normalizeRawResponse(targetResp.RawBody, true)

		diffResult, compareErr = diff.Compare(
			baselineRaw,
			targetRaw,
			diff.DefaultOptions(),
		)

		// Check critical receipt fields explicitly so comparison options cannot mask a failure.
		if diffResult == nil {
			diffResult = &diff.CompareResult{}
		}

		var baselineMap, targetMap map[string]interface{}
		if err := json.Unmarshal(baselineResp.RawBody, &baselineMap); err == nil {
			if err := json.Unmarshal(targetResp.RawBody, &targetMap); err == nil {
				if baselineRes, ok := baselineMap["result"].(map[string]interface{}); ok && baselineRes != nil {
					if targetRes, ok := targetMap["result"].(map[string]interface{}); ok && targetRes != nil {
						if baselineRes["gasUsed"] != targetRes["gasUsed"] {
							exists := false
							for _, d := range diffResult.Differences {
								if strings.Contains(d.Path, "gasUsed") {
									exists = true
									break
								}
							}
							if !exists {
								diffResult.Differences = append(diffResult.Differences, diff.Difference{
									Type:     diff.DiffTypeValue,
									Path:     "result.gasUsed",
									Expected: baselineRes["gasUsed"],
									Actual:   targetRes["gasUsed"],
									Message:  "Gas 使用不一致 (强制检查)",
									Severity: diff.SeverityFail,
								})
								diffResult.FailCount++
							}
						}

						if baselineRes["status"] != targetRes["status"] {
							exists := false
							for _, d := range diffResult.Differences {
								if strings.Contains(d.Path, "status") {
									exists = true
									break
								}
							}
							if !exists {
								diffResult.Differences = append(diffResult.Differences, diff.Difference{
									Type:     diff.DiffTypeValue,
									Path:     "result.status",
									Expected: baselineRes["status"],
									Actual:   targetRes["status"],
									Message:  "交易状态不一致 (强制检查)",
									Severity: diff.SeverityFail,
								})
								diffResult.FailCount++
							}
						}
					}
				}
			}
		}
	}

	tc := report.TestCase{
		Name:   fmt.Sprintf("%s_receipt", testName),
		Method: "eth_getTransactionReceipt",
		Params: []interface{}{baselineTxHash.Hex(), targetTxHash.Hex()}, // retain both transaction hashes
	}
	t.reporter.AddResult(tc, compareResult, diffResult, compareErr)

	if compareErr != nil {
		return false, compareErr
	}
	if diffResult != nil && diffResult.FailCount > 0 {
		var diffMsgs []string
		for _, d := range diffResult.Differences {
			if d.Severity == diff.SeverityFail {
				diffMsgs = append(diffMsgs, fmt.Sprintf("%s: %s (Baseline: %v, Target: %v)", d.Path, d.Message, d.Expected, d.Actual))
			}
		}
		return false, fmt.Errorf("receipt comparison failed: %s", strings.Join(diffMsgs, "; "))
	}

	return true, nil
}

// WaitForReceiptAndCompare polls both clients for the same transaction hash and compares receipts.
func (t *Tester) WaitForReceiptAndCompare(ctx context.Context, txHash common.Hash, timeout time.Duration, testName string) (*TxReceipt, error) {
	deadline := time.Now().Add(timeout)
	req := rpc.NewRequest("eth_getTransactionReceipt", []interface{}{txHash.Hex()})
	for time.Now().Before(deadline) {
		baselineResp := t.baselineClient.Call(ctx, req)
		targetResp := t.targetClient.Call(ctx, req)

		if baselineResp.Error != nil {
			time.Sleep(500 * time.Millisecond)
			continue
		}
		if baselineResp.Response.Error != nil {
			time.Sleep(500 * time.Millisecond)
			continue
		}
		if string(baselineResp.Response.Result) == "null" {
			time.Sleep(500 * time.Millisecond)
			continue
		}

		var rawReceipt map[string]interface{}
		if err := json.Unmarshal(baselineResp.Response.Result, &rawReceipt); err != nil {
			time.Sleep(500 * time.Millisecond)
			continue
		}

		receipt := &TxReceipt{
			TransactionHash: txHash.Hex(),
		}

		if status, ok := rawReceipt["status"].(string); ok {
			receipt.Status, _ = hexutil.DecodeUint64(status)
		}
		if gasUsed, ok := rawReceipt["gasUsed"].(string); ok {
			receipt.GasUsed, _ = hexutil.DecodeUint64(gasUsed)
		}
		if blockNumber, ok := rawReceipt["blockNumber"].(string); ok {
			receipt.BlockNumber, _ = hexutil.DecodeUint64(blockNumber)
		}
		if cumulativeGasUsed, ok := rawReceipt["cumulativeGasUsed"].(string); ok {
			receipt.CumulativeGasUsed, _ = hexutil.DecodeUint64(cumulativeGasUsed)
		}
		if contractAddress, ok := rawReceipt["contractAddress"].(string); ok {
			receipt.ContractAddress = contractAddress
		}

		// Record the comparison once the reference receipt is available.
		if t.reporter != nil {
			// Allow the target a short period to catch up with the reference.
			if string(targetResp.Response.Result) == "null" {
				if time.Until(deadline) > 2*time.Second {
					time.Sleep(200 * time.Millisecond)
					continue
				}
			}

			compareResult := &rpc.CompareResult{
				Request:          req,
				BaselineResponse: baselineResp,
				TargetResponse:   targetResp,
			}

			var diffResult *diff.CompareResult
			var compareErr error
			if targetResp.Response != nil && targetResp.Response.Error == nil && string(targetResp.Response.Result) != "null" {
				diffResult, compareErr = diff.Compare(
					baselineResp.RawBody,
					targetResp.RawBody,
					diff.DefaultOptions(),
				)
			} else {
				// A missing target receipt is a visible difference.
				diffResult = &diff.CompareResult{
					Differences: []diff.Difference{
						{
							Type:    diff.DiffTypeMissing,
							Path:    "receipt",
							Message: fmt.Sprintf("%s is missing a receipt (%s confirmed)", t.targetClient.Name(), t.baselineClient.Name()),
						},
					},
				}
			}

			tc := report.TestCase{
				Name:   fmt.Sprintf("%s_receipt", testName),
				Method: "eth_getTransactionReceipt",
				Params: []interface{}{txHash.Hex()},
			}
			t.reporter.AddResult(tc, compareResult, diffResult, compareErr)
		}

		return receipt, nil
	}

	return nil, fmt.Errorf("等待交易确认超时: %s", txHash.Hex())
}

// GetProof requests an account proof from one client.
func (t *Tester) GetProof(ctx context.Context, client *rpc.Client, address common.Address, storageKeys []string, block string) (map[string]interface{}, error) {
	resp := client.Call(ctx, rpc.NewRequest("eth_getProof", []interface{}{address.Hex(), storageKeys, block}))
	if resp.Error != nil {
		return nil, resp.Error
	}
	if resp.Response.Error != nil {
		return nil, fmt.Errorf("RPC error: %s", resp.Response.Error.Message)
	}

	var proof map[string]interface{}
	if err := json.Unmarshal(resp.Response.Result, &proof); err != nil {
		return nil, err
	}
	return proof, nil
}

// TestNativeTransfer compares native-token transfers sent through both clients.
func (t *Tester) TestNativeTransfer(ctx context.Context, recipient common.Address, amount *big.Int, txType TxType, usePreconf bool) *TxTestResult {
	testName := fmt.Sprintf("原生代币转账_%s", txType.String())
	if usePreconf {
		testName += "_preconf"
	}

	result := &TxTestResult{
		TestName: testName,
		TxType:   txType.String(),
	}

	initialBalance, err := t.GetBalanceAndCompare(ctx, recipient, "latest", fmt.Sprintf("%s_initial_balance", testName))
	if err != nil {
		result.Error = fmt.Sprintf("获取初始余额失败: %v", err)
		return result
	}

	baselineNonce, err := t.GetNonce(ctx, t.baselineClient, t.builder.Address())
	if err != nil {
		result.Error = fmt.Sprintf("获取 Baseline nonce 失败: %v", err)
		return result
	}

	gasPrice, err := t.GetGasPrice(ctx, t.baselineClient)
	if err != nil {
		result.Error = fmt.Sprintf("获取 gas 价格失败: %v", err)
		return result
	}

	estimatedGas, _ := t.EstimateGas(ctx, t.baselineClient, map[string]interface{}{
		"from":  t.builder.Address().Hex(),
		"to":    recipient.Hex(),
		"value": hexutil.EncodeBig(amount),
	})
	gas := estimatedGas * 2 // allow for estimation variance
	// EIP-7702 with an authorization list needs at least 46,000 intrinsic gas.
	if txType == TxTypeEIP7702 && gas < 46000 {
		gas = 46000
	}

	maxPriorityFee := big.NewInt(1000000000)                   // 1 gwei
	maxFeePerGas := new(big.Int).Add(gasPrice, maxPriorityFee) // baseFee + priorityFee

	params := &TxParams{
		From:                 t.builder.Address(),
		To:                   &recipient,
		Value:                amount,
		Gas:                  gas,
		GasPrice:             gasPrice,
		MaxFeePerGas:         maxFeePerGas,
		MaxPriorityFeePerGas: maxPriorityFee,
		Nonce:                baselineNonce,
		ChainID:              t.chainID,
	}

	// EIP-7702 requires an authorization list.
	if txType == TxTypeEIP7702 {
		// The sender self-authorizes its own address as the code address.
		auth := types.SetCodeAuthorization{
			ChainID: *uint256.MustFromBig(t.chainID),
			Address: t.builder.Address(), // authorized code address
			Nonce:   baselineNonce,       // current nonce
		}
		signedAuth, err := t.builder.SignSetCodeAuth(auth)
		if err != nil {
			result.Error = fmt.Sprintf("签名授权失败: %v", err)
			return result
		}
		params.AuthList = []types.SetCodeAuthorization{signedAuth}
	}

	baselineTx, err := t.builder.BuildAndSign(txType, params)
	if err != nil {
		result.Error = fmt.Sprintf("构建 Baseline 交易失败: %v", err)
		return result
	}

	var baselineTxHash common.Hash
	if usePreconf {
		preconfResp, err := t.SendRawTransactionWithPreconf(ctx, t.baselineClient, baselineTx, fmt.Sprintf("%s_send_baseline_preconf", testName))
		if err != nil {
			result.Error = fmt.Sprintf("发送 Baseline preconf 交易失败: %v", err)
			return result
		}
		result.BaselinePreconf = preconfResp
		baselineTxHash = common.HexToHash(preconfResp.TxHash)
	} else {
		baselineTxHash, err = t.SendRawTransaction(ctx, t.baselineClient, baselineTx, fmt.Sprintf("%s_send_baseline", testName))
		if err != nil {
			result.Error = fmt.Sprintf("发送 Baseline 交易失败: %v", err)
			return result
		}
	}
	result.BaselineTxHash = baselineTxHash.Hex()

	baselineReceipt, err := t.WaitForReceipt(ctx, t.baselineClient, baselineTxHash, 60*time.Second)
	if err != nil {
		result.Error = fmt.Sprintf("等待 Baseline 交易确认失败: %v", err)
		return result
	}
	result.BaselineReceipt = baselineReceipt

	balanceAfterBaseline, err := t.GetBalanceAndCompare(ctx, recipient, "latest", fmt.Sprintf("%s_after_baseline_balance", testName))
	if err != nil {
		result.Error = fmt.Sprintf("获取 Baseline 交易后余额失败: %v", err)
		return result
	}
	baselineBalanceDelta := new(big.Int).Sub(balanceAfterBaseline, initialBalance)

	// Use the next nonce for the target transaction.
	params.Nonce = baselineNonce + 1

	// The target EIP-7702 authorization needs its own nonce.
	if txType == TxTypeEIP7702 {
		auth := types.SetCodeAuthorization{
			ChainID: *uint256.MustFromBig(t.chainID),
			Address: t.builder.Address(),
			Nonce:   baselineNonce + 1, // next nonce
		}
		signedAuth, err := t.builder.SignSetCodeAuth(auth)
		if err != nil {
			result.Error = fmt.Sprintf("签名 Target 授权失败: %v", err)
			return result
		}
		params.AuthList = []types.SetCodeAuthorization{signedAuth}
	}

	targetTx, err := t.builder.BuildAndSign(txType, params)
	if err != nil {
		result.Error = fmt.Sprintf("构建 Target 交易失败: %v", err)
		return result
	}

	var targetTxHash common.Hash
	if usePreconf {
		preconfResp, err := t.SendRawTransactionWithPreconf(ctx, t.targetClient, targetTx, fmt.Sprintf("%s_send_target_preconf", testName))
		if err != nil {
			result.Error = fmt.Sprintf("发送 Target preconf 交易失败: %v", err)
			return result
		}
		result.TargetPreconf = preconfResp
		targetTxHash = common.HexToHash(preconfResp.TxHash)
	} else {
		targetTxHash, err = t.SendRawTransaction(ctx, t.targetClient, targetTx, fmt.Sprintf("%s_send_target", testName))
		if err != nil {
			result.Error = fmt.Sprintf("发送 Target 交易失败: %v", err)
			return result
		}
	}
	result.TargetTxHash = targetTxHash.Hex()

	targetReceipt, err := t.WaitForReceipt(ctx, t.targetClient, targetTxHash, 60*time.Second)
	if err != nil {
		result.Error = fmt.Sprintf("等待 Target 交易确认失败: %v", err)
		return result
	}
	result.TargetReceipt = targetReceipt

	match, diffErr := t.CompareReceipts(ctx, testName, baselineTxHash, targetTxHash)

	balanceAfterTarget, err := t.GetBalanceAndCompare(ctx, recipient, "latest", fmt.Sprintf("%s_after_target_balance", testName))
	if err != nil {
		result.Error = fmt.Sprintf("获取 Target 交易后余额失败: %v", err)
		return result
	}
	targetBalanceDelta := new(big.Int).Sub(balanceAfterTarget, balanceAfterBaseline)

	comparison := &StateComparison{
		BaselineBalanceDelta: baselineBalanceDelta,
		TargetBalanceDelta:   targetBalanceDelta,
		BalanceMatch:         baselineBalanceDelta.Cmp(targetBalanceDelta) == 0,
		BaselineGasUsed:      baselineReceipt.GasUsed,
		TargetGasUsed:        targetReceipt.GasUsed,
		GasMatch:             baselineReceipt.GasUsed == targetReceipt.GasUsed,
		BaselineStatus:       baselineReceipt.Status,
		TargetStatus:         targetReceipt.Status,
		StatusMatch:          baselineReceipt.Status == targetReceipt.Status,
	}

	if !comparison.BalanceMatch {
		comparison.Differences = append(comparison.Differences,
			fmt.Sprintf("余额变化不一致: Baseline=%s, Target=%s", baselineBalanceDelta.String(), targetBalanceDelta.String()))
	}
	if !comparison.GasMatch {
		comparison.Differences = append(comparison.Differences,
			fmt.Sprintf("Gas 使用不一致: Baseline=%d, Target=%d", baselineReceipt.GasUsed, targetReceipt.GasUsed))
	}
	if !comparison.StatusMatch {
		comparison.Differences = append(comparison.Differences,
			fmt.Sprintf("交易状态不一致: Baseline=%d, Target=%d", baselineReceipt.Status, targetReceipt.Status))
	}

	result.StateComparison = comparison
	result.Passed = comparison.BalanceMatch && comparison.StatusMatch && comparison.GasMatch && match

	if !result.Passed && result.Error == "" {
		var errs []string
		if !match && diffErr != nil {
			errs = append(errs, diffErr.Error())
		}
		if !comparison.BalanceMatch {
			errs = append(errs, "余额不一致")
		}
		if !comparison.StatusMatch {
			errs = append(errs, "Status不一致")
		}
		if !comparison.GasMatch {
			errs = append(errs, "Gas不一致")
		}

		if len(comparison.Differences) > 0 {
			errs = append(errs, strings.Join(comparison.Differences, "; "))
		}
		result.Error = strings.Join(errs, " | ")
	}

	return result
}

// Builder returns the transaction builder.
func (t *Tester) Builder() *Builder {
	return t.builder
}

// BaselineClient returns the reference client.
func (t *Tester) BaselineClient() *rpc.Client {
	return t.baselineClient
}

// TargetClient returns the target client.
func (t *Tester) TargetClient() *rpc.Client {
	return t.targetClient
}

// ChainID returns the configured chain ID.
func (t *Tester) ChainID() *big.Int {
	return t.chainID
}

// TestTxpoolRejection checks that both clients reject the same signed transaction.
//
// Each error must contain expectedErrSubstring. buildTx receives the current
// nonce and chooses the signer and transaction contents.
func (t *Tester) TestTxpoolRejection(
	ctx context.Context,
	testName string,
	buildTx func(nonce uint64) (*types.Transaction, error),
	expectedErrSubstring string,
) *TxTestResult {
	result := &TxTestResult{
		TestName: testName,
		TxType:   "rejection",
	}

	// Use the reference nonce, as in the other transaction tests.
	nonce, err := t.GetNonce(ctx, t.baselineClient, t.builder.Address())
	if err != nil {
		result.Error = fmt.Sprintf("获取 nonce 失败: %v", err)
		return result
	}

	signedTx, err := buildTx(nonce)
	if err != nil {
		result.Error = fmt.Sprintf("构造交易失败: %v", err)
		return result
	}

	rawTx, err := signedTx.MarshalBinary()
	if err != nil {
		result.Error = fmt.Sprintf("序列化交易失败: %v", err)
		return result
	}

	// Send identical signed bytes to both clients.
	req := rpc.NewRequest("eth_sendRawTransaction", []interface{}{hexutil.Encode(rawTx)})
	baselineResp := t.baselineClient.Call(ctx, req)
	targetResp := t.targetClient.Call(ctx, req)

	// A forwarding replica may wrap the sequencer error while another client
	// returns it directly. Comparing full messages would create a false failure,
	// so this scenario checks the expected substring without recording a diff.

	baselineErrMsg := extractRPCError(baselineResp)
	targetErrMsg := extractRPCError(targetResp)

	var failures []string

	if baselineErrMsg == "" {
		failures = append(failures, fmt.Sprintf("%s accepted a transaction that should be rejected", t.baselineClient.Name()))
	} else if !strings.Contains(baselineErrMsg, expectedErrSubstring) {
		failures = append(failures, fmt.Sprintf(
			"%s rejection error: expected substring %q, got %q", t.baselineClient.Name(), expectedErrSubstring, baselineErrMsg))
	}

	if targetErrMsg == "" {
		failures = append(failures, fmt.Sprintf("%s accepted a transaction that should be rejected", t.targetClient.Name()))
	} else if !strings.Contains(targetErrMsg, expectedErrSubstring) {
		failures = append(failures, fmt.Sprintf(
			"%s rejection error: expected substring %q, got %q", t.targetClient.Name(), expectedErrSubstring, targetErrMsg))
	}

	if len(failures) == 0 {
		result.Passed = true
	} else {
		result.Error = strings.Join(failures, " | ")
	}

	return result
}

// extractRPCError returns an RPC or transport error message, or an empty string on acceptance.
func extractRPCError(resp *rpc.ResponseWithMeta) string {
	if resp.Error != nil {
		return resp.Error.Error()
	}
	if resp.Response != nil && resp.Response.Error != nil {
		return resp.Response.Error.Message
	}
	return ""
}

// TestTxpoolAcceptance checks that both clients accept valid transactions.
//
// It uses different nonces to avoid conflicts and expects a hash from each client.
func (t *Tester) TestTxpoolAcceptance(
	ctx context.Context,
	testName string,
	buildTx func(nonce uint64) (*types.Transaction, error),
) *TxTestResult {
	result := &TxTestResult{
		TestName: testName,
		TxType:   "acceptance",
	}

	nonce, err := t.GetNonce(ctx, t.baselineClient, t.builder.Address())
	if err != nil {
		result.Error = fmt.Sprintf("获取 nonce 失败: %v", err)
		return result
	}

	var failures []string

	baselineTx, err := buildTx(nonce)
	if err != nil {
		result.Error = fmt.Sprintf("build %s transaction: %v", t.baselineClient.Name(), err)
		return result
	}
	baselineHash, baselineErr := t.SendRawTransaction(ctx, t.baselineClient, baselineTx, testName+"_baseline")
	if baselineErr != nil {
		failures = append(failures, fmt.Sprintf("%s rejected a valid transaction: %v", t.baselineClient.Name(), baselineErr))
	} else {
		result.BaselineTxHash = baselineHash.Hex()
	}

	// Use the next nonce to avoid conflicting with the reference transaction.
	targetTx, err := buildTx(nonce + 1)
	if err != nil {
		result.Error = fmt.Sprintf("build %s transaction: %v", t.targetClient.Name(), err)
		return result
	}
	targetHash, targetErr := t.SendRawTransaction(ctx, t.targetClient, targetTx, testName+"_target")
	if targetErr != nil {
		failures = append(failures, fmt.Sprintf("%s rejected a valid transaction: %v", t.targetClient.Name(), targetErr))
	} else {
		result.TargetTxHash = targetHash.Hex()
	}

	if len(failures) == 0 {
		result.Passed = true
	} else {
		result.Error = strings.Join(failures, " | ")
	}

	return result
}

// TestTxpoolForwardedRetention checks local txpool admission parity after forwarding.
//
// Follower RPC nodes forward eth_sendRawTransaction to the sequencer. By default,
// op-geth does not admit forwarded transactions to its local pool. Older op-reth
// retained them; with equivalent admission settings, both local views should agree.
//
// Both endpoints must be forwarding followers. A sequencer admits transactions
// directly and cannot expose this difference.
//
// Each client receives a future-nonce transaction, which remains queued instead
// of being mined. After acceptance (including an already-known response from a
// shared sequencer), compare each follower's own txpool_content for the sender.
func (t *Tester) TestTxpoolForwardedRetention(
	ctx context.Context,
	testName string,
	buildTx func(nonce uint64) (*types.Transaction, error),
) *TxTestResult {
	result := &TxTestResult{
		TestName: testName,
		TxType:   "forwarded_retention",
	}

	sender := t.builder.Address()

	// Leave a nonce gap on each client so the transaction remains queued.
	baselineNonce, err := t.GetNonce(ctx, t.baselineClient, sender)
	if err != nil {
		result.Error = fmt.Sprintf("get %s nonce: %v", t.baselineClient.Name(), err)
		return result
	}
	targetNonce, err := t.GetNonce(ctx, t.targetClient, sender)
	if err != nil {
		result.Error = fmt.Sprintf("get %s nonce: %v", t.targetClient.Name(), err)
		return result
	}

	var failures []string

	baselineTx, err := buildTx(baselineNonce + 10)
	if err != nil {
		result.Error = fmt.Sprintf("build %s transaction: %v", t.baselineClient.Name(), err)
		return result
	}
	if baselineHash, baselineErr := t.sendRawTransactionAllowAlreadyKnown(ctx, t.baselineClient, baselineTx, testName+"_baseline"); baselineErr != nil {
		failures = append(failures, fmt.Sprintf("%s future-nonce submission failed: %v", t.baselineClient.Name(), baselineErr))
	} else {
		result.BaselineTxHash = baselineHash.Hex()
	}

	targetTx, err := buildTx(targetNonce + 10)
	if err != nil {
		result.Error = fmt.Sprintf("build %s transaction: %v", t.targetClient.Name(), err)
		return result
	}
	if targetHash, targetErr := t.sendRawTransactionAllowAlreadyKnown(ctx, t.targetClient, targetTx, testName+"_target"); targetErr != nil {
		failures = append(failures, fmt.Sprintf("%s future-nonce submission failed: %v", t.targetClient.Name(), targetErr))
	} else {
		result.TargetTxHash = targetHash.Hex()
	}

	baselinePresent, baselineErr := t.senderInTxpoolContent(ctx, t.baselineClient, sender)
	if baselineErr != nil {
		failures = append(failures, fmt.Sprintf("query %s txpool_content: %v", t.baselineClient.Name(), baselineErr))
	}
	targetPresent, targetErr := t.senderInTxpoolContent(ctx, t.targetClient, sender)
	if targetErr != nil {
		failures = append(failures, fmt.Sprintf("query %s txpool_content: %v", t.targetClient.Name(), targetErr))
	}

	// Both followers must present the same local pool state for this sender.
	if baselineErr == nil && targetErr == nil && baselinePresent != targetPresent {
		failures = append(failures, fmt.Sprintf(
			"forwarding followers disagree on local txpool retention: %s present=%v, %s present=%v",
			t.baselineClient.Name(), baselinePresent, t.targetClient.Name(), targetPresent,
		))
	}

	if len(failures) == 0 {
		result.Passed = true
	} else {
		result.Error = strings.Join(failures, " | ")
	}

	return result
}

func isAlreadyKnownError(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "already known")
}

// senderInTxpoolContent reports whether a sender appears in the pending or queued pool.
func (t *Tester) senderInTxpoolContent(ctx context.Context, client *rpc.Client, sender common.Address) (bool, error) {
	resp := client.Call(ctx, rpc.NewRequest("txpool_content", []interface{}{}))
	if resp.Error != nil {
		return false, resp.Error
	}
	if resp.Response.Error != nil {
		return false, fmt.Errorf("RPC error: %s", resp.Response.Error.Message)
	}

	// txpool_content contains pending and queued maps keyed by sender address.
	var content struct {
		Pending map[string]json.RawMessage `json:"pending"`
		Queued  map[string]json.RawMessage `json:"queued"`
	}
	if err := json.Unmarshal(resp.Response.Result, &content); err != nil {
		return false, fmt.Errorf("解析 txpool_content 失败: %w", err)
	}

	// Address keys are compared case-insensitively.
	want := strings.ToLower(sender.Hex())
	for addr := range content.Pending {
		if strings.ToLower(addr) == want {
			return true, nil
		}
	}
	for addr := range content.Queued {
		if strings.ToLower(addr) == want {
			return true, nil
		}
	}
	return false, nil
}
