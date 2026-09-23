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
)

// SimpleStorage 合约字节码
// Solidity 源码:
// pragma solidity ^0.8.0;
//
//	contract SimpleStorage {
//	    uint256 private value;
//	    function set(uint256 _value) public {
//	        value = _value;
//	    }
//	    function get() public view returns (uint256) {
//	        return value;
//	    }
//	}
const SimpleStorageBytecode = "0x608060405234801561000f575f80fd5b506101438061001d5f395ff3fe608060405234801561000f575f80fd5b5060043610610034575f3560e01c806360fe47b1146100385780636d4ce63c14610054575b5f80fd5b610052600480360381019061004d91906100ba565b610072565b005b61005c61007b565b60405161006991906100f4565b60405180910390f35b805f8190555050565b5f8054905090565b5f80fd5b5f819050919050565b61009981610087565b81146100a3575f80fd5b50565b5f813590506100b481610090565b92915050565b5f602082840312156100cf576100ce610083565b5b5f6100dc848285016100a6565b91505092915050565b6100ee81610087565b82525050565b5f6020820190506101075f8301846100e5565b9291505056fea2646970667358221220c4e5b5c2d88f5d5c8e9f1a2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0c1d2e64736f6c63430008150033"

// DeployContract 部署合约到两个客户端并比较收据
func (t *Tester) DeployContract(ctx context.Context, bytecode string, testName string) (*ContractDeployResult, error) {
	result := &ContractDeployResult{
		TestName: testName,
	}

	// === 部署到 Geth ===
	gethNonce, err := t.GetNonce(ctx, t.gethClient, t.builder.Address())
	if err != nil {
		return nil, fmt.Errorf("获取 Geth nonce 失败: %w", err)
	}

	gasPrice, err := t.GetGasPrice(ctx, t.gethClient)
	if err != nil {
		return nil, fmt.Errorf("获取 gas 价格失败: %w", err)
	}

	// 预估 gas
	bytecodeBytes := common.FromHex(bytecode)
	estimatedGas, err := t.EstimateGas(ctx, t.gethClient, map[string]interface{}{
		"from": t.builder.Address().Hex(),
		"data": hexutil.Encode(bytecodeBytes),
	})
	if err != nil {
		return nil, fmt.Errorf("预估 gas 失败: %w", err)
	}
	gasLimit := estimatedGas + 100000 // 添加安全边界

	// 构建合约部署交易（使用 EIP-1559）
	maxPriorityFee := big.NewInt(1000000000) // 1 gwei
	maxFeePerGas := new(big.Int).Add(gasPrice, maxPriorityFee)

	gethTx := types.NewTx(&types.DynamicFeeTx{
		ChainID:   t.chainID,
		Nonce:     gethNonce,
		To:        nil, // nil 表示合约创建
		Value:     big.NewInt(0),
		Gas:       gasLimit,
		GasFeeCap: maxFeePerGas,
		GasTipCap: maxPriorityFee,
		Data:      bytecodeBytes,
	})

	// 签名交易
	signer := types.NewCancunSigner(t.chainID)
	gethSignedTx, err := types.SignTx(gethTx, signer, t.builder.privateKey)
	if err != nil {
		return nil, fmt.Errorf("签名 Geth 交易失败: %w", err)
	}

	// 发送交易
	gethTxHash, err := t.SendRawTransaction(ctx, t.gethClient, gethSignedTx, fmt.Sprintf("%s_deploy_geth", testName))
	if err != nil {
		return nil, fmt.Errorf("发送 Geth 部署交易失败: %w", err)
	}
	result.GethTxHash = gethTxHash.Hex()

	// 等待确认
	gethReceipt, err := t.WaitForReceipt(ctx, t.gethClient, gethTxHash, 60*time.Second)
	if err != nil {
		return nil, fmt.Errorf("等待 Geth 部署确认失败: %w", err)
	}
	result.GethReceipt = gethReceipt
	result.GethContractAddress = gethReceipt.ContractAddress

	if gethReceipt.Status != 1 {
		return nil, fmt.Errorf("Geth 合约部署失败: status=%d", gethReceipt.Status)
	}

	// === 部署到 Reth ===
	rethNonce := gethNonce + 1

	rethTx := types.NewTx(&types.DynamicFeeTx{
		ChainID:   t.chainID,
		Nonce:     rethNonce,
		To:        nil, // nil 表示合约创建
		Value:     big.NewInt(0),
		Gas:       gasLimit,
		GasFeeCap: maxFeePerGas,
		GasTipCap: maxPriorityFee,
		Data:      bytecodeBytes,
	})

	rethSignedTx, err := types.SignTx(rethTx, signer, t.builder.privateKey)
	if err != nil {
		return nil, fmt.Errorf("签名 Reth 交易失败: %w", err)
	}

	rethTxHash, err := t.SendRawTransaction(ctx, t.rethClient, rethSignedTx, fmt.Sprintf("%s_deploy_reth", testName))
	if err != nil {
		return nil, fmt.Errorf("发送 Reth 部署交易失败: %w", err)
	}
	result.RethTxHash = rethTxHash.Hex()

	// 等待确认
	rethReceipt, err := t.WaitForReceipt(ctx, t.rethClient, rethTxHash, 60*time.Second)
	if err != nil {
		return nil, fmt.Errorf("等待 Reth 部署确认失败: %w", err)
	}
	result.RethReceipt = rethReceipt
	result.RethContractAddress = rethReceipt.ContractAddress

	if rethReceipt.Status != 1 {
		return nil, fmt.Errorf("❌ Reth 合约部署失败: status=%d", rethReceipt.Status)
	}

	// 比较两个收据
	match, diffErr := t.CompareReceipts(ctx, testName, gethTxHash, rethTxHash)

	// 对比结果
	result.Success = gethReceipt.Status == 1 && rethReceipt.Status == 1 && match

	if !result.Success && result.Error == "" {
		var errs []string
		if !match && diffErr != nil {
			errs = append(errs, diffErr.Error())
		}
		if gethReceipt.Status != 1 {
			errs = append(errs, "Geth Status != 1")
		}
		if rethReceipt.Status != 1 {
			errs = append(errs, "Reth Status != 1")
		}
		result.Error = strings.Join(errs, " | ")
	}

	return result, nil
}

// CallContract 调用合约方法并比较响应
// callData: 编码后的方法调用数据
func (t *Tester) CallContract(ctx context.Context, gethContractAddr, rethContractAddr common.Address, callData []byte, testName string) (*ContractCallResult, error) {
	result := &ContractCallResult{
		TestName:        testName,
		ContractAddress: gethContractAddr.Hex(),
	}

	// 使用 eth_call 查询合约状态（view 方法）
	gethReq := rpc.NewRequest("eth_call", []interface{}{
		map[string]interface{}{
			"to":   gethContractAddr.Hex(),
			"data": hexutil.Encode(callData),
		},
		"latest",
	})
	rethReq := rpc.NewRequest("eth_call", []interface{}{
		map[string]interface{}{
			"to":   rethContractAddr.Hex(),
			"data": hexutil.Encode(callData),
		},
		"latest",
	})

	// 同时调用 geth 和 reth
	gethResp := t.gethClient.Call(ctx, gethReq)
	rethResp := t.rethClient.Call(ctx, rethReq)

	// 记录到 Reporter
	if t.reporter != nil {
		compareResult := &rpc.CompareResult{
			Request:           gethReq, // 使用 Geth 请求作为主要记录
			PrimaryResponse:   gethResp,
			SecondaryResponse: rethResp,
		}

		var diffResult *diff.CompareResult
		var compareErr error
		if gethResp.Response.Error == nil && rethResp.Response.Error == nil {
			// eth_call 响应标准化（移除 id）
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
			Method: "eth_call",
			Params: []interface{}{
				map[string]interface{}{
					"to":   gethContractAddr.Hex(), // 记录 Geth 地址
					"data": hexutil.Encode(callData),
				},
				"latest",
			},
		}
		t.reporter.AddResult(tc, compareResult, diffResult, compareErr)
	}

	// 解析响应
	if gethResp.Error != nil {
		return nil, fmt.Errorf("❌ Geth eth_call 失败: %w", gethResp.Error)
	}
	if gethResp.Response.Error != nil {
		return nil, fmt.Errorf("❌ Geth RPC error: %s", gethResp.Response.Error.Message)
	}

	var gethResultHex string
	if err := json.Unmarshal(gethResp.Response.Result, &gethResultHex); err != nil {
		return nil, fmt.Errorf("解析 Geth 响应失败: %w", err)
	}
	result.GethResult = gethResultHex

	if rethResp.Error == nil && rethResp.Response.Error == nil {
		var rethResultHex string
		if err := json.Unmarshal(rethResp.Response.Result, &rethResultHex); err == nil {
			result.RethResult = rethResultHex
			result.ResultMatch = gethResultHex == rethResultHex
		}
	}

	result.Success = result.ResultMatch

	return result, nil
}

// SendContractTransaction 发送修改合约状态的交易
func (t *Tester) SendContractTransaction(ctx context.Context, gethContractAddr, rethContractAddr common.Address, callData []byte, testName string) (*ContractTxResult, error) {
	result := &ContractTxResult{
		TestName:        testName,
		ContractAddress: gethContractAddr.Hex(),
	}

	// === 发送到 Geth ===
	gethNonce, err := t.GetNonce(ctx, t.gethClient, t.builder.Address())
	if err != nil {
		return nil, fmt.Errorf("获取 Geth nonce 失败: %w", err)
	}

	gasPrice, err := t.GetGasPrice(ctx, t.gethClient)
	if err != nil {
		return nil, fmt.Errorf("获取 gas 价格失败: %w", err)
	}

	// 预估 gas
	estimatedGas, err := t.EstimateGas(ctx, t.gethClient, map[string]interface{}{
		"from": t.builder.Address().Hex(),
		"to":   gethContractAddr.Hex(),
		"data": hexutil.Encode(callData),
	})
	if err != nil {
		return nil, fmt.Errorf("预估 gas 失败: %w", err)
	}
	gasLimit := estimatedGas + 50000

	// 构建交易
	maxPriorityFee := big.NewInt(1000000000)
	maxFeePerGas := new(big.Int).Add(gasPrice, maxPriorityFee)

	gethTx := types.NewTx(&types.DynamicFeeTx{
		ChainID:   t.chainID,
		Nonce:     gethNonce,
		To:        &gethContractAddr,
		Value:     big.NewInt(0),
		Gas:       gasLimit,
		GasFeeCap: maxFeePerGas,
		GasTipCap: maxPriorityFee,
		Data:      callData,
	})

	signer := types.NewCancunSigner(t.chainID)
	gethSignedTx, err := types.SignTx(gethTx, signer, t.builder.privateKey)
	if err != nil {
		return nil, fmt.Errorf("签名 Geth 交易失败: %w", err)
	}

	gethTxHash, err := t.SendRawTransaction(ctx, t.gethClient, gethSignedTx, fmt.Sprintf("%s_geth", testName))
	if err != nil {
		return nil, fmt.Errorf("发送 Geth 交易失败: %w", err)
	}
	result.GethTxHash = gethTxHash.Hex()

	gethReceipt, err := t.WaitForReceipt(ctx, t.gethClient, gethTxHash, 60*time.Second)
	if err != nil {
		return nil, fmt.Errorf("等待 Geth 交易确认失败: %w", err)
	}
	result.GethReceipt = gethReceipt

	// === 发送到 Reth ===
	rethNonce := gethNonce + 1

	rethTx := types.NewTx(&types.DynamicFeeTx{
		ChainID:   t.chainID,
		Nonce:     rethNonce,
		To:        &rethContractAddr,
		Value:     big.NewInt(0),
		Gas:       gasLimit,
		GasFeeCap: maxFeePerGas,
		GasTipCap: maxPriorityFee,
		Data:      callData,
	})

	rethSignedTx, err := types.SignTx(rethTx, signer, t.builder.privateKey)
	if err != nil {
		return nil, fmt.Errorf("签名 Reth 交易失败: %w", err)
	}

	rethTxHash, err := t.SendRawTransaction(ctx, t.rethClient, rethSignedTx, fmt.Sprintf("%s_reth", testName))
	if err != nil {
		return nil, fmt.Errorf("发送 Reth 交易失败: %w", err)
	}
	result.RethTxHash = rethTxHash.Hex()

	rethReceipt, err := t.WaitForReceipt(ctx, t.rethClient, rethTxHash, 60*time.Second)
	if err != nil {
		return nil, fmt.Errorf("等待 Reth 交易确认失败: %w", err)
	}
	result.RethReceipt = rethReceipt

	// 比较两个收据
	match, diffErr := t.CompareReceipts(ctx, testName, gethTxHash, rethTxHash)

	// 对比结果
	result.Success = gethReceipt.Status == 1 && match

	if !result.Success && result.Error == "" {
		var errs []string
		if !match && diffErr != nil {
			errs = append(errs, diffErr.Error())
		}
		if gethReceipt.Status != 1 {
			errs = append(errs, "Geth Status != 1")
		}

		result.Error = strings.Join(errs, " | ")
	}

	return result, nil
}
