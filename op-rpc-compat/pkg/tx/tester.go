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

// Tester 交易测试器
type Tester struct {
	gethClient *rpc.Client
	rethClient *rpc.Client
	builder    *Builder
	chainID    *big.Int
	reporter   *report.Reporter // 用于记录所有 RPC 调用
}

// NewTester 创建交易测试器
func NewTester(gethURL, rethURL, privateKeyHex string, reporter *report.Reporter) (*Tester, error) {
	gethClient := rpc.NewClient(gethURL, "geth", 30*time.Second)
	rethClient := rpc.NewClient(rethURL, "reth", 30*time.Second)

	// 获取 chainID
	ctx := context.Background()
	chainIDResp := gethClient.Call(ctx, rpc.NewRequest("eth_chainId", nil))
	if chainIDResp.Error != nil {
		return nil, fmt.Errorf("获取 chainID 失败: %w", chainIDResp.Error)
	}

	var chainIDHex string
	if err := json.Unmarshal(chainIDResp.Response.Result, &chainIDHex); err != nil {
		return nil, fmt.Errorf("解析 chainID 失败: %w", err)
	}
	chainID, _ := hexutil.DecodeBig(chainIDHex)

	builder, err := NewBuilder(chainID, privateKeyHex)
	if err != nil {
		return nil, err
	}

	return &Tester{
		gethClient: gethClient,
		rethClient: rethClient,
		builder:    builder,
		chainID:    chainID,
		reporter:   reporter,
	}, nil
}

// GetNonce 获取账户 nonce
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

// GetBalance 获取账户余额（仅查询单个客户端，不比较）
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

// GetBalanceAndCompare 获取账户余额并比较 geth 和 reth 的响应
func (t *Tester) GetBalanceAndCompare(ctx context.Context, address common.Address, block string, testName string) (*big.Int, error) {
	req := rpc.NewRequest("eth_getBalance", []interface{}{address.Hex(), block})

	// 同时查询 geth 和 reth
	gethResp := t.gethClient.Call(ctx, req)
	rethResp := t.rethClient.Call(ctx, req)

	// 记录到 Reporter
	if t.reporter != nil {
		compareResult := &rpc.CompareResult{
			Request:           req,
			PrimaryResponse:   gethResp,
			SecondaryResponse: rethResp,
		}

		var diffResult *diff.CompareResult
		var compareErr error
		if gethResp.Response.Error == nil && rethResp.Response.Error == nil {
			gethRaw := normalizeRawResponse(gethResp.RawBody, false)
			rethRaw := normalizeRawResponse(rethResp.RawBody, false)
			diffResult, compareErr = diff.Compare(
				gethRaw,
				rethRaw,
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

	// 返回 geth 的结果（用于后续逻辑）
	if gethResp.Error != nil {
		return nil, gethResp.Error
	}
	if gethResp.Response.Error != nil {
		return nil, fmt.Errorf("RPC error: %s", gethResp.Response.Error.Message)
	}

	var balanceHex string
	if err := json.Unmarshal(gethResp.Response.Result, &balanceHex); err != nil {
		return nil, err
	}

	balance, err := hexutil.DecodeBig(balanceHex)
	if err != nil {
		return nil, err
	}
	return balance, nil
}

// GetGasPrice 获取 gas 价格
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

// EstimateGas 估算 gas
func (t *Tester) EstimateGas(ctx context.Context, client *rpc.Client, params map[string]interface{}) (uint64, error) {
	resp := client.Call(ctx, rpc.NewRequest("eth_estimateGas", []interface{}{params}))
	if resp.Error != nil {
		return 0, resp.Error
	}
	if resp.Response.Error != nil {
		return 21000, nil // 默认 gas
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

// SendRawTransaction 发送原始交易（记录 RPC 调用但不比较，因为 geth 和 reth 发送的是不同的交易）
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

	// 记录到 Reporter（仅记录，不比较，因为 geth 和 reth 发送的是不同的交易）
	if t.reporter != nil {
		// 创建一个虚拟的 CompareResult，只包含当前客户端的响应
		var compareResult *rpc.CompareResult
		if client == t.gethClient {
			compareResult = &rpc.CompareResult{
				Request:           req,
				PrimaryResponse:   resp,
				SecondaryResponse: nil, // reth 不发送相同交易
			}
		} else {
			compareResult = &rpc.CompareResult{
				Request:           req,
				PrimaryResponse:   nil, // geth 不发送相同交易
				SecondaryResponse: resp,
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

// SendRawTransactionWithPreconf 发送预确认交易（记录 RPC 调用但不比较，因为 geth 和 reth 发送的是不同的交易）
func (t *Tester) SendRawTransactionWithPreconf(ctx context.Context, client *rpc.Client, signedTx *types.Transaction, testName string) (*PreconfResponse, error) {
	rawTx, err := signedTx.MarshalBinary()
	if err != nil {
		return nil, fmt.Errorf("序列化交易失败: %w", err)
	}

	req := rpc.NewRequest("eth_sendRawTransactionWithPreconf", []interface{}{hexutil.Encode(rawTx)})
	resp := client.Call(ctx, req)

	// 记录到 Reporter（仅记录，不比较，因为 geth 和 reth 发送的是不同的交易）
	if t.reporter != nil {
		// 创建一个虚拟的 CompareResult，只包含当前客户端的响应
		var compareResult *rpc.CompareResult
		if client == t.gethClient {
			compareResult = &rpc.CompareResult{
				Request:           req,
				PrimaryResponse:   resp,
				SecondaryResponse: nil, // reth 不发送相同交易
			}
		} else {
			compareResult = &rpc.CompareResult{
				Request:           req,
				PrimaryResponse:   nil, // geth 不发送相同交易
				SecondaryResponse: resp,
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

// WaitForReceipt 等待交易确认（仅查询单个客户端，不比较）
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

		// 检查是否为 null
		if string(resp.Response.Result) == "null" {
			time.Sleep(500 * time.Millisecond)
			continue
		}

		// 解析为 map 再转换
		var rawReceipt map[string]interface{}
		if err := json.Unmarshal(resp.Response.Result, &rawReceipt); err != nil {
			time.Sleep(500 * time.Millisecond)
			continue
		}

		receipt := &TxReceipt{
			TransactionHash: txHash.Hex(),
		}

		// 解析字段
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

// normalizeRawResponse 标准化响应 JSON，移除 id 和不确定的字段
func normalizeRawResponse(data []byte, isReceipt bool) []byte {
	var m map[string]interface{}
	if err := json.Unmarshal(data, &m); err != nil {
		return data
	}
	delete(m, "id")

	if isReceipt {
		if res, ok := m["result"].(map[string]interface{}); ok {
			// 移除环境相关的字段
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

// CompareReceipts 比较两个不同交易哈希的收据（用于跨客户端比较）
// 分别从 Geth 和 Reth 获取收据并进行比较
func (t *Tester) CompareReceipts(ctx context.Context, testName string, gethTxHash, rethTxHash common.Hash) (bool, error) {
	if t.reporter == nil {
		return true, nil
	}

	// 1. 从 Geth 获取 Geth 交易的收据
	gethReq := rpc.NewRequest("eth_getTransactionReceipt", []interface{}{gethTxHash.Hex()})
	gethResp := t.gethClient.Call(ctx, gethReq)

	// 2. 从 Reth 获取 Reth 交易的收据
	rethReq := rpc.NewRequest("eth_getTransactionReceipt", []interface{}{rethTxHash.Hex()})
	rethResp := t.rethClient.Call(ctx, rethReq)

	// 3. 构造对比结果
	// 注意：这里我们将两个不同的请求响应组合在一起进行对比
	compareResult := &rpc.CompareResult{
		Request:           gethReq, // 使用 Geth 的请求作为主要的记录
		PrimaryResponse:   gethResp,
		SecondaryResponse: rethResp,
	}

	var diffResult *diff.CompareResult
	var compareErr error

	if gethResp.Response.Error == nil && rethResp.Response.Error == nil {
		// 标准化响应
		gethRaw := normalizeRawResponse(gethResp.RawBody, true)
		rethRaw := normalizeRawResponse(rethResp.RawBody, true)

		diffResult, compareErr = diff.Compare(
			gethRaw,
			rethRaw,
			diff.DefaultOptions(),
		)

		// 强制检查关键字段，确保差异被记录为失败
		// diff.Compare 可能会因为某些配置忽略某些字段，或者我们希望确保这些差异导致失败
		if diffResult == nil {
			diffResult = &diff.CompareResult{}
		}

		var gethMap, rethMap map[string]interface{}
		// 解析外层 JSON ({"jsonrpc":..., "result":...})
		if err := json.Unmarshal(gethResp.RawBody, &gethMap); err == nil {
			if err := json.Unmarshal(rethResp.RawBody, &rethMap); err == nil {
				// 获取 result 部分
				if gethRes, ok := gethMap["result"].(map[string]interface{}); ok && gethRes != nil {
					if rethRes, ok := rethMap["result"].(map[string]interface{}); ok && rethRes != nil {
						// 检查 gasUsed
						if gethRes["gasUsed"] != rethRes["gasUsed"] {
							// 检查是否已存在该差异
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
									Expected: gethRes["gasUsed"],
									Actual:   rethRes["gasUsed"],
									Message:  "Gas 使用不一致 (强制检查)",
									Severity: diff.SeverityFail,
								})
								diffResult.FailCount++
							}
						}

						// 检查 status
						if gethRes["status"] != rethRes["status"] {
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
									Expected: gethRes["status"],
									Actual:   rethRes["status"],
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
		Params: []interface{}{gethTxHash.Hex(), rethTxHash.Hex()}, // 记录两个 hash
	}
	t.reporter.AddResult(tc, compareResult, diffResult, compareErr)

	// 返回比较结果
	if compareErr != nil {
		return false, compareErr
	}
	if diffResult != nil && diffResult.FailCount > 0 {
		// 构建差异错误消息
		var diffMsgs []string
		for _, d := range diffResult.Differences {
			if d.Severity == diff.SeverityFail {
				diffMsgs = append(diffMsgs, fmt.Sprintf("%s: %s (Geth: %v, Reth: %v)", d.Path, d.Message, d.Expected, d.Actual))
			}
		}
		return false, fmt.Errorf("receipt comparison failed: %s", strings.Join(diffMsgs, "; "))
	}

	return true, nil
}

// WaitForReceiptAndCompare 等待交易确认并比较 geth 和 reth 的响应
// 对于同一个交易哈希，在 geth 和 reth 上都查询收据并比较结果
func (t *Tester) WaitForReceiptAndCompare(ctx context.Context, txHash common.Hash, timeout time.Duration, testName string) (*TxReceipt, error) {
	deadline := time.Now().Add(timeout)
	req := rpc.NewRequest("eth_getTransactionReceipt", []interface{}{txHash.Hex()})
	recorded := false // 只记录一次成功的结果

	for time.Now().Before(deadline) {
		// 同时查询 geth 和 reth
		gethResp := t.gethClient.Call(ctx, req)
		rethResp := t.rethClient.Call(ctx, req)

		// 检查 geth 响应
		if gethResp.Error != nil {
			time.Sleep(500 * time.Millisecond)
			continue
		}
		if gethResp.Response.Error != nil {
			time.Sleep(500 * time.Millisecond)
			continue
		}
		if string(gethResp.Response.Result) == "null" {
			time.Sleep(500 * time.Millisecond)
			continue
		}

		// 解析 geth 收据
		var rawReceipt map[string]interface{}
		if err := json.Unmarshal(gethResp.Response.Result, &rawReceipt); err != nil {
			time.Sleep(500 * time.Millisecond)
			continue
		}

		receipt := &TxReceipt{
			TransactionHash: txHash.Hex(),
		}

		// 解析字段
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

		// 记录到 Reporter（只在成功获取收据后记录一次）
		if t.reporter != nil && !recorded {
			// 如果 reth 还没有响应，给一点时间让它同步
			if string(rethResp.Response.Result) == "null" {
				// 简单的重试机制：如果距离 deadline 还远，就继续等待 reth
				if time.Until(deadline) > 2*time.Second {
					time.Sleep(200 * time.Millisecond)
					continue
				}
			}

			compareResult := &rpc.CompareResult{
				Request:           req,
				PrimaryResponse:   gethResp,
				SecondaryResponse: rethResp,
			}

			var diffResult *diff.CompareResult
			var compareErr error
			if rethResp.Response != nil && rethResp.Response.Error == nil && string(rethResp.Response.Result) != "null" {
				// 如果 reth 也有收据，比较两者
				diffResult, compareErr = diff.Compare(
					gethResp.RawBody,
					rethResp.RawBody,
					diff.DefaultOptions(),
				)
			} else {
				// reth 缺失收据，记录差异
				diffResult = &diff.CompareResult{
					Differences: []diff.Difference{
						{
							Type:    diff.DiffTypeMissing,
							Path:    "receipt",
							Message: "reth 缺失收据 (geth 已确认)",
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
			recorded = true
		}

		return receipt, nil
	}

	return nil, fmt.Errorf("等待交易确认超时: %s", txHash.Hex())
}

// GetProof 获取账户证明
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

// TestNativeTransfer 测试原生代币转账
func (t *Tester) TestNativeTransfer(ctx context.Context, recipient common.Address, amount *big.Int, txType TxType, usePreconf bool) *TxTestResult {
	testName := fmt.Sprintf("原生代币转账_%s", txType.String())
	if usePreconf {
		testName += "_preconf"
	}

	result := &TxTestResult{
		TestName: testName,
		TxType:   txType.String(),
	}

	// 获取初始状态（使用比较方法）
	initialBalance, err := t.GetBalanceAndCompare(ctx, recipient, "latest", fmt.Sprintf("%s_initial_balance", testName))
	if err != nil {
		result.Error = fmt.Sprintf("获取初始余额失败: %v", err)
		return result
	}

	// === Geth 交易 ===
	gethNonce, err := t.GetNonce(ctx, t.gethClient, t.builder.Address())
	if err != nil {
		result.Error = fmt.Sprintf("获取 Geth nonce 失败: %v", err)
		return result
	}

	gasPrice, err := t.GetGasPrice(ctx, t.gethClient)
	if err != nil {
		result.Error = fmt.Sprintf("获取 gas 价格失败: %v", err)
		return result
	}

	// 估算 gas
	estimatedGas, _ := t.EstimateGas(ctx, t.gethClient, map[string]interface{}{
		"from":  t.builder.Address().Hex(),
		"to":    recipient.Hex(),
		"value": hexutil.EncodeBig(amount),
	})
	gas := estimatedGas * 2 // 安全边界
	// EIP-7702 带授权列表，链上最小 intrinsic gas 为 46000
	if txType == TxTypeEIP7702 && gas < 46000 {
		gas = 46000
	}

	// 计算 EIP-1559 参数
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
		Nonce:                gethNonce,
		ChainID:              t.chainID,
	}

	// EIP-7702 需要授权列表
	if txType == TxTypeEIP7702 {
		// 创建一个自签名授权：发送者授权将自己的地址设置为代码地址
		// 这是一个最简单的 EIP-7702 测试场景
		auth := types.SetCodeAuthorization{
			ChainID: *uint256.MustFromBig(t.chainID),
			Address: t.builder.Address(), // 授权设置代码的地址
			Nonce:   gethNonce,           // 使用当前 nonce
		}
		signedAuth, err := t.builder.SignSetCodeAuth(auth)
		if err != nil {
			result.Error = fmt.Sprintf("签名授权失败: %v", err)
			return result
		}
		params.AuthList = []types.SetCodeAuthorization{signedAuth}
	}

	gethTx, err := t.builder.BuildAndSign(txType, params)
	if err != nil {
		result.Error = fmt.Sprintf("构建 Geth 交易失败: %v", err)
		return result
	}

	var gethTxHash common.Hash
	if usePreconf {
		preconfResp, err := t.SendRawTransactionWithPreconf(ctx, t.gethClient, gethTx, fmt.Sprintf("%s_send_geth_preconf", testName))
		if err != nil {
			result.Error = fmt.Sprintf("发送 Geth preconf 交易失败: %v", err)
			return result
		}
		result.GethPreconf = preconfResp
		gethTxHash = common.HexToHash(preconfResp.TxHash)
	} else {
		gethTxHash, err = t.SendRawTransaction(ctx, t.gethClient, gethTx, fmt.Sprintf("%s_send_geth", testName))
		if err != nil {
			result.Error = fmt.Sprintf("发送 Geth 交易失败: %v", err)
			return result
		}
	}
	result.GethTxHash = gethTxHash.Hex()

	// 等待 Geth 交易确认
	gethReceipt, err := t.WaitForReceipt(ctx, t.gethClient, gethTxHash, 60*time.Second)
	if err != nil {
		result.Error = fmt.Sprintf("等待 Geth 交易确认失败: %v", err)
		return result
	}
	result.GethReceipt = gethReceipt

	// 获取 Geth 交易后余额（使用比较方法）
	balanceAfterGeth, err := t.GetBalanceAndCompare(ctx, recipient, "latest", fmt.Sprintf("%s_after_geth_balance", testName))
	if err != nil {
		result.Error = fmt.Sprintf("获取 Geth 交易后余额失败: %v", err)
		return result
	}
	gethBalanceDelta := new(big.Int).Sub(balanceAfterGeth, initialBalance)

	// === Reth 交易 ===
	// 使用递增的 nonce
	params.Nonce = gethNonce + 1

	// EIP-7702 需要更新授权列表中的 nonce
	if txType == TxTypeEIP7702 {
		auth := types.SetCodeAuthorization{
			ChainID: *uint256.MustFromBig(t.chainID),
			Address: t.builder.Address(),
			Nonce:   gethNonce + 1, // 使用递增的 nonce
		}
		signedAuth, err := t.builder.SignSetCodeAuth(auth)
		if err != nil {
			result.Error = fmt.Sprintf("签名 Reth 授权失败: %v", err)
			return result
		}
		params.AuthList = []types.SetCodeAuthorization{signedAuth}
	}

	rethTx, err := t.builder.BuildAndSign(txType, params)
	if err != nil {
		result.Error = fmt.Sprintf("构建 Reth 交易失败: %v", err)
		return result
	}

	var rethTxHash common.Hash
	if usePreconf {
		preconfResp, err := t.SendRawTransactionWithPreconf(ctx, t.rethClient, rethTx, fmt.Sprintf("%s_send_reth_preconf", testName))
		if err != nil {
			result.Error = fmt.Sprintf("发送 Reth preconf 交易失败: %v", err)
			return result
		}
		result.RethPreconf = preconfResp
		rethTxHash = common.HexToHash(preconfResp.TxHash)
	} else {
		rethTxHash, err = t.SendRawTransaction(ctx, t.rethClient, rethTx, fmt.Sprintf("%s_send_reth", testName))
		if err != nil {
			result.Error = fmt.Sprintf("发送 Reth 交易失败: %v", err)
			return result
		}
	}
	result.RethTxHash = rethTxHash.Hex()

	// 等待 Reth 交易确认
	rethReceipt, err := t.WaitForReceipt(ctx, t.rethClient, rethTxHash, 60*time.Second)
	if err != nil {
		result.Error = fmt.Sprintf("等待 Reth 交易确认失败: %v", err)
		return result
	}
	result.RethReceipt = rethReceipt

	// 比较两个收据
	match, diffErr := t.CompareReceipts(ctx, testName, gethTxHash, rethTxHash)

	// 获取 Reth 交易后余额（使用比较方法）
	balanceAfterReth, err := t.GetBalanceAndCompare(ctx, recipient, "latest", fmt.Sprintf("%s_after_reth_balance", testName))
	if err != nil {
		result.Error = fmt.Sprintf("获取 Reth 交易后余额失败: %v", err)
		return result
	}
	rethBalanceDelta := new(big.Int).Sub(balanceAfterReth, balanceAfterGeth)

	// 状态对比
	comparison := &StateComparison{
		GethBalanceDelta: gethBalanceDelta,
		RethBalanceDelta: rethBalanceDelta,
		BalanceMatch:     gethBalanceDelta.Cmp(rethBalanceDelta) == 0,
		GethGasUsed:      gethReceipt.GasUsed,
		RethGasUsed:      rethReceipt.GasUsed,
		GasMatch:         gethReceipt.GasUsed == rethReceipt.GasUsed,
		GethStatus:       gethReceipt.Status,
		RethStatus:       rethReceipt.Status,
		StatusMatch:      gethReceipt.Status == rethReceipt.Status,
	}

	// 记录差异
	if !comparison.BalanceMatch {
		comparison.Differences = append(comparison.Differences,
			fmt.Sprintf("余额变化不一致: Geth=%s, Reth=%s", gethBalanceDelta.String(), rethBalanceDelta.String()))
	}
	if !comparison.GasMatch {
		comparison.Differences = append(comparison.Differences,
			fmt.Sprintf("Gas 使用不一致: Geth=%d, Reth=%d", gethReceipt.GasUsed, rethReceipt.GasUsed))
	}
	if !comparison.StatusMatch {
		comparison.Differences = append(comparison.Differences,
			fmt.Sprintf("交易状态不一致: Geth=%d, Reth=%d", gethReceipt.Status, rethReceipt.Status))
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

// Builder 返回交易构建器
func (t *Tester) Builder() *Builder {
	return t.builder
}

// GethClient 返回 Geth 客户端
func (t *Tester) GethClient() *rpc.Client {
	return t.gethClient
}

// RethClient 返回 Reth 客户端
func (t *Tester) RethClient() *rpc.Client {
	return t.rethClient
}

// ChainID 返回链 ID
func (t *Tester) ChainID() *big.Int {
	return t.chainID
}

// TestTxpoolRejection 测试 txpool 拒绝特定交易。
//
// 与 TestNativeTransfer 不同，此函数预期交易被两个节点 **都拒绝**。
// 同一笔已签名交易分别发给 geth 和 reth，验证两边都返回包含
// expectedErrSubstring 的错误信息。
//
// 将同一笔交易的 geth/reth 响应记录为一条双边 CompareResult，
// 让 reporter 能正确 diff 两个 error response（而不是记录两条单边的）。
//
// buildTx 接收当前 nonce 并返回一笔已签名交易。调用者负责选择签名方式
// （EIP-155 / HomesteadSigner）和交易内容。
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

	// 获取 nonce（从 geth，保持与其他测试一致）
	nonce, err := t.GetNonce(ctx, t.gethClient, t.builder.Address())
	if err != nil {
		result.Error = fmt.Sprintf("获取 nonce 失败: %v", err)
		return result
	}

	// 构造已签名交易
	signedTx, err := buildTx(nonce)
	if err != nil {
		result.Error = fmt.Sprintf("构造交易失败: %v", err)
		return result
	}

	// 序列化交易
	rawTx, err := signedTx.MarshalBinary()
	if err != nil {
		result.Error = fmt.Sprintf("序列化交易失败: %v", err)
		return result
	}

	// 同一笔交易发给两个节点
	req := rpc.NewRequest("eth_sendRawTransaction", []interface{}{hexutil.Encode(rawTx)})
	gethResp := t.gethClient.Call(ctx, req)
	rethResp := t.rethClient.Call(ctx, req)

	// Rejection 测试不记录到 reporter。
	//
	// 原因：geth (replica) 会把 sequencer 的错误用
	// "failed to forward tx to sequencer, err: '...'" 包装，
	// 导致 error.message 与 reth (sequencer) 的原始消息不一致。
	// 这是 replica forward 机制的正常行为，但 reporter 会把
	// message 文本差异标记为 FAIL——产生误报。
	//
	// Rejection 测试的 pass/fail 由下面的 expectedErrSubstring
	// 检查决定，不依赖 reporter 的 diff 机制。

	// 提取 error 信息
	gethErrMsg := extractRPCError(gethResp)
	rethErrMsg := extractRPCError(rethResp)

	var failures []string

	if gethErrMsg == "" {
		failures = append(failures, "geth 应该拒绝但接受了交易")
	} else if !strings.Contains(gethErrMsg, expectedErrSubstring) {
		failures = append(failures, fmt.Sprintf(
			"geth 错误信息不匹配: 期望包含 %q, 实际: %q", expectedErrSubstring, gethErrMsg))
	}

	if rethErrMsg == "" {
		failures = append(failures, "reth 应该拒绝但接受了交易")
	} else if !strings.Contains(rethErrMsg, expectedErrSubstring) {
		failures = append(failures, fmt.Sprintf(
			"reth 错误信息不匹配: 期望包含 %q, 实际: %q", expectedErrSubstring, rethErrMsg))
	}

	if len(failures) == 0 {
		result.Passed = true
	} else {
		result.Error = strings.Join(failures, " | ")
	}

	return result
}

// extractRPCError 从 RPC 响应中提取错误信息字符串。
// 返回空字符串表示无错误（交易被接受）。
func extractRPCError(resp *rpc.ResponseWithMeta) string {
	if resp.Error != nil {
		return resp.Error.Error()
	}
	if resp.Response != nil && resp.Response.Error != nil {
		return resp.Response.Error.Message
	}
	return ""
}

// TestTxpoolAcceptance 测试 txpool 接受特定交易（正向测试，验证不被误拒）。
//
// 与 TestTxpoolRejection 互补：预期交易被两个节点都接受。
// 用不同 nonce 分别构造交易发给 geth 和 reth，验证两边都返回 tx hash。
func (t *Tester) TestTxpoolAcceptance(
	ctx context.Context,
	testName string,
	buildTx func(nonce uint64) (*types.Transaction, error),
) *TxTestResult {
	result := &TxTestResult{
		TestName: testName,
		TxType:   "acceptance",
	}

	nonce, err := t.GetNonce(ctx, t.gethClient, t.builder.Address())
	if err != nil {
		result.Error = fmt.Sprintf("获取 nonce 失败: %v", err)
		return result
	}

	var failures []string

	// 发给 geth（nonce N）
	gethTx, err := buildTx(nonce)
	if err != nil {
		result.Error = fmt.Sprintf("构造 geth 交易失败: %v", err)
		return result
	}
	gethHash, gethErr := t.SendRawTransaction(ctx, t.gethClient, gethTx, testName+"_geth")
	if gethErr != nil {
		failures = append(failures, fmt.Sprintf("geth 应该接受但拒绝了: %v", gethErr))
	} else {
		result.GethTxHash = gethHash.Hex()
	}

	// 发给 reth（nonce N+1，避免跟 geth 的交易冲突）
	rethTx, err := buildTx(nonce + 1)
	if err != nil {
		result.Error = fmt.Sprintf("构造 reth 交易失败: %v", err)
		return result
	}
	rethHash, rethErr := t.SendRawTransaction(ctx, t.rethClient, rethTx, testName+"_reth")
	if rethErr != nil {
		failures = append(failures, fmt.Sprintf("reth 应该接受但拒绝了: %v", rethErr))
	} else {
		result.RethTxHash = rethHash.Hex()
	}

	if len(failures) == 0 {
		result.Passed = true
	} else {
		result.Error = strings.Join(failures, " | ")
	}

	return result
}

// TestTxpoolForwardedRetention 验证转发型节点对"已转发交易"的本地 txpool 准入行为在
// geth 与 reth 之间一致。
//
// 背景：验证/RPC 节点配了 --rollup.sequencer(-http) 后，eth_sendRawTransaction 会被转发给
// sequencer。op-geth 默认不把转发的 tx 留在本地池（--rollup.enabletxpooladmission 默认关）；
// 老版 op-reth 无此开关、无条件保留，导致同为转发节点时 reth 的 txpool_* 暴露该 tx 而 geth 不暴露
// （另见 docs/reth_geth_txpool_forwarded_tx_admission.md）。reth 补上等价开关后（默认关），两端
// 转发节点的本地池视图应当一致。
//
// 前提：本测试打的 geth/reth 端点必须是**转发型节点**（配了 sequencer），而非 sequencer 本身；
// 否则 tx 直接进池，两端本来就都非空，测不到该差异。
//
// 手法：用 future-nonce（在链上 nonce 上留出间隙 → 语义为 queued）分别向 geth、reth 提交一笔 tx，
// 各自受理（返回 hash；共享 sequencer 已接收时允许 already known）后立即查**同一节点**的
// txpool_content，断言两端对该发送者地址的呈现一致
// （默认关准入时：两端都不含该 tx）。future-nonce 不会被 sequencer 立即打包，避免竞态。
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

	// 各自基于本节点的链上 nonce 制造 gap（+10），确保落入 queued 而非 pending。
	gethNonce, err := t.GetNonce(ctx, t.gethClient, sender)
	if err != nil {
		result.Error = fmt.Sprintf("获取 geth nonce 失败: %v", err)
		return result
	}
	rethNonce, err := t.GetNonce(ctx, t.rethClient, sender)
	if err != nil {
		result.Error = fmt.Sprintf("获取 reth nonce 失败: %v", err)
		return result
	}

	var failures []string

	// 向 geth 提交 future-nonce tx
	gethTx, err := buildTx(gethNonce + 10)
	if err != nil {
		result.Error = fmt.Sprintf("构造 geth 交易失败: %v", err)
		return result
	}
	if gethHash, gethErr := t.sendRawTransactionAllowAlreadyKnown(ctx, t.gethClient, gethTx, testName+"_geth"); gethErr != nil {
		failures = append(failures, fmt.Sprintf("geth 提交 future-nonce tx 失败: %v", gethErr))
	} else {
		result.GethTxHash = gethHash.Hex()
	}

	// 向 reth 提交 future-nonce tx
	rethTx, err := buildTx(rethNonce + 10)
	if err != nil {
		result.Error = fmt.Sprintf("构造 reth 交易失败: %v", err)
		return result
	}
	if rethHash, rethErr := t.sendRawTransactionAllowAlreadyKnown(ctx, t.rethClient, rethTx, testName+"_reth"); rethErr != nil {
		failures = append(failures, fmt.Sprintf("reth 提交 future-nonce tx 失败: %v", rethErr))
	} else {
		result.RethTxHash = rethHash.Hex()
	}

	// 查询各自节点的 txpool_content，判断该 sender 是否出现在本地池。
	gethPresent, gethErr := t.senderInTxpoolContent(ctx, t.gethClient, sender)
	if gethErr != nil {
		failures = append(failures, fmt.Sprintf("查询 geth txpool_content 失败: %v", gethErr))
	}
	rethPresent, rethErr := t.senderInTxpoolContent(ctx, t.rethClient, sender)
	if rethErr != nil {
		failures = append(failures, fmt.Sprintf("查询 reth txpool_content 失败: %v", rethErr))
	}

	// 核心断言：两端对该 sender 的本地池呈现必须一致。
	if gethErr == nil && rethErr == nil && gethPresent != rethPresent {
		failures = append(failures, fmt.Sprintf(
			"转发节点本地 txpool 视图不一致: geth present=%v, reth present=%v "+
				"(期望一致；reth 需启用 --rollup.enabletxpooladmission 等价开关并默认关，以对齐 geth)",
			gethPresent, rethPresent,
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

// senderInTxpoolContent 查询指定节点的 txpool_content，返回该 sender 地址是否出现在
// pending 或 queued 段中。
func (t *Tester) senderInTxpoolContent(ctx context.Context, client *rpc.Client, sender common.Address) (bool, error) {
	resp := client.Call(ctx, rpc.NewRequest("txpool_content", []interface{}{}))
	if resp.Error != nil {
		return false, resp.Error
	}
	if resp.Response.Error != nil {
		return false, fmt.Errorf("RPC error: %s", resp.Response.Error.Message)
	}

	// txpool_content 结构: {"pending": {addr: {...}}, "queued": {addr: {...}}}
	var content struct {
		Pending map[string]json.RawMessage `json:"pending"`
		Queued  map[string]json.RawMessage `json:"queued"`
	}
	if err := json.Unmarshal(resp.Response.Result, &content); err != nil {
		return false, fmt.Errorf("解析 txpool_content 失败: %w", err)
	}

	// 地址键大小写不敏感比较。
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
