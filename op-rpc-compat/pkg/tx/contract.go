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

// SimpleStorageBytecode contains the compiled bytecode of this Solidity source:
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

// DeployContract deploys the same contract through both clients and compares receipts.
func (t *Tester) DeployContract(ctx context.Context, bytecode string, testName string) (*ContractDeployResult, error) {
	result := &ContractDeployResult{
		TestName: testName,
	}

	baselineNonce, err := t.GetNonce(ctx, t.baselineClient, t.builder.Address())
	if err != nil {
		return nil, fmt.Errorf("获取 Baseline nonce 失败: %w", err)
	}

	gasPrice, err := t.GetGasPrice(ctx, t.baselineClient)
	if err != nil {
		return nil, fmt.Errorf("获取 gas 价格失败: %w", err)
	}

	bytecodeBytes := common.FromHex(bytecode)
	estimatedGas, err := t.EstimateGas(ctx, t.baselineClient, map[string]interface{}{
		"from": t.builder.Address().Hex(),
		"data": hexutil.Encode(bytecodeBytes),
	})
	if err != nil {
		return nil, fmt.Errorf("预估 gas 失败: %w", err)
	}
	gasLimit := estimatedGas + 100000 // allow for estimation variance

	maxPriorityFee := big.NewInt(1000000000) // 1 gwei
	maxFeePerGas := new(big.Int).Add(gasPrice, maxPriorityFee)

	baselineTx := types.NewTx(&types.DynamicFeeTx{
		ChainID:   t.chainID,
		Nonce:     baselineNonce,
		To:        nil, // nil creates a contract
		Value:     big.NewInt(0),
		Gas:       gasLimit,
		GasFeeCap: maxFeePerGas,
		GasTipCap: maxPriorityFee,
		Data:      bytecodeBytes,
	})

	signer := types.NewCancunSigner(t.chainID)
	baselineSignedTx, err := types.SignTx(baselineTx, signer, t.builder.privateKey)
	if err != nil {
		return nil, fmt.Errorf("签名 Baseline 交易失败: %w", err)
	}

	baselineTxHash, err := t.SendRawTransaction(ctx, t.baselineClient, baselineSignedTx, fmt.Sprintf("%s_deploy_baseline", testName))
	if err != nil {
		return nil, fmt.Errorf("发送 Baseline 部署交易失败: %w", err)
	}
	result.BaselineTxHash = baselineTxHash.Hex()

	baselineReceipt, err := t.WaitForReceipt(ctx, t.baselineClient, baselineTxHash, 60*time.Second)
	if err != nil {
		return nil, fmt.Errorf("等待 Baseline 部署确认失败: %w", err)
	}
	result.BaselineReceipt = baselineReceipt
	result.BaselineContractAddress = baselineReceipt.ContractAddress

	if baselineReceipt.Status != 1 {
		return nil, fmt.Errorf("Baseline 合约部署失败: status=%d", baselineReceipt.Status)
	}

	targetNonce := baselineNonce + 1

	targetTx := types.NewTx(&types.DynamicFeeTx{
		ChainID:   t.chainID,
		Nonce:     targetNonce,
		To:        nil, // nil creates a contract
		Value:     big.NewInt(0),
		Gas:       gasLimit,
		GasFeeCap: maxFeePerGas,
		GasTipCap: maxPriorityFee,
		Data:      bytecodeBytes,
	})

	targetSignedTx, err := types.SignTx(targetTx, signer, t.builder.privateKey)
	if err != nil {
		return nil, fmt.Errorf("签名 Target 交易失败: %w", err)
	}

	targetTxHash, err := t.SendRawTransaction(ctx, t.targetClient, targetSignedTx, fmt.Sprintf("%s_deploy_target", testName))
	if err != nil {
		return nil, fmt.Errorf("发送 Target 部署交易失败: %w", err)
	}
	result.TargetTxHash = targetTxHash.Hex()

	targetReceipt, err := t.WaitForReceipt(ctx, t.targetClient, targetTxHash, 60*time.Second)
	if err != nil {
		return nil, fmt.Errorf("等待 Target 部署确认失败: %w", err)
	}
	result.TargetReceipt = targetReceipt
	result.TargetContractAddress = targetReceipt.ContractAddress

	if targetReceipt.Status != 1 {
		return nil, fmt.Errorf("❌ Target 合约部署失败: status=%d", targetReceipt.Status)
	}

	match, diffErr := t.CompareReceipts(ctx, testName, baselineTxHash, targetTxHash)

	result.Success = baselineReceipt.Status == 1 && targetReceipt.Status == 1 && match

	if !result.Success && result.Error == "" {
		var errs []string
		if !match && diffErr != nil {
			errs = append(errs, diffErr.Error())
		}
		if baselineReceipt.Status != 1 {
			errs = append(errs, "Baseline Status != 1")
		}
		if targetReceipt.Status != 1 {
			errs = append(errs, "Target Status != 1")
		}
		result.Error = strings.Join(errs, " | ")
	}

	return result, nil
}

// CallContract invokes the same contract method on both clients and compares responses.
// callData contains the encoded method invocation.
func (t *Tester) CallContract(ctx context.Context, baselineContractAddr, targetContractAddr common.Address, callData []byte, testName string) (*ContractCallResult, error) {
	result := &ContractCallResult{
		TestName:        testName,
		ContractAddress: baselineContractAddr.Hex(),
	}

	baselineReq := rpc.NewRequest("eth_call", []interface{}{
		map[string]interface{}{
			"to":   baselineContractAddr.Hex(),
			"data": hexutil.Encode(callData),
		},
		"latest",
	})
	targetReq := rpc.NewRequest("eth_call", []interface{}{
		map[string]interface{}{
			"to":   targetContractAddr.Hex(),
			"data": hexutil.Encode(callData),
		},
		"latest",
	})

	baselineResp := t.baselineClient.Call(ctx, baselineReq)
	targetResp := t.targetClient.Call(ctx, targetReq)

	if t.reporter != nil {
		compareResult := &rpc.CompareResult{
			Request:          baselineReq, // retain the reference request in the report
			BaselineResponse: baselineResp,
			TargetResponse:   targetResp,
		}

		var diffResult *diff.CompareResult
		var compareErr error
		if baselineResp.Response.Error == nil && targetResp.Response.Error == nil {
			// Normalize eth_call responses by removing the request ID.
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
			Method: "eth_call",
			Params: []interface{}{
				map[string]interface{}{
					"to":   baselineContractAddr.Hex(), // retain the reference contract address
					"data": hexutil.Encode(callData),
				},
				"latest",
			},
		}
		t.reporter.AddResult(tc, compareResult, diffResult, compareErr)
	}

	if baselineResp.Error != nil {
		return nil, fmt.Errorf("❌ Baseline eth_call 失败: %w", baselineResp.Error)
	}
	if baselineResp.Response.Error != nil {
		return nil, fmt.Errorf("❌ Baseline RPC error: %s", baselineResp.Response.Error.Message)
	}

	var baselineResultHex string
	if err := json.Unmarshal(baselineResp.Response.Result, &baselineResultHex); err != nil {
		return nil, fmt.Errorf("解析 Baseline 响应失败: %w", err)
	}
	result.BaselineResult = baselineResultHex

	if targetResp.Error == nil && targetResp.Response.Error == nil {
		var targetResultHex string
		if err := json.Unmarshal(targetResp.Response.Result, &targetResultHex); err == nil {
			result.TargetResult = targetResultHex
			result.ResultMatch = baselineResultHex == targetResultHex
		}
	}

	result.Success = result.ResultMatch

	return result, nil
}

// SendContractTransaction sends a state-changing contract call to both clients.
func (t *Tester) SendContractTransaction(ctx context.Context, baselineContractAddr, targetContractAddr common.Address, callData []byte, testName string) (*ContractTxResult, error) {
	result := &ContractTxResult{
		TestName:        testName,
		ContractAddress: baselineContractAddr.Hex(),
	}

	baselineNonce, err := t.GetNonce(ctx, t.baselineClient, t.builder.Address())
	if err != nil {
		return nil, fmt.Errorf("获取 Baseline nonce 失败: %w", err)
	}

	gasPrice, err := t.GetGasPrice(ctx, t.baselineClient)
	if err != nil {
		return nil, fmt.Errorf("获取 gas 价格失败: %w", err)
	}

	estimatedGas, err := t.EstimateGas(ctx, t.baselineClient, map[string]interface{}{
		"from": t.builder.Address().Hex(),
		"to":   baselineContractAddr.Hex(),
		"data": hexutil.Encode(callData),
	})
	if err != nil {
		return nil, fmt.Errorf("预估 gas 失败: %w", err)
	}
	gasLimit := estimatedGas + 50000

	maxPriorityFee := big.NewInt(1000000000)
	maxFeePerGas := new(big.Int).Add(gasPrice, maxPriorityFee)

	baselineTx := types.NewTx(&types.DynamicFeeTx{
		ChainID:   t.chainID,
		Nonce:     baselineNonce,
		To:        &baselineContractAddr,
		Value:     big.NewInt(0),
		Gas:       gasLimit,
		GasFeeCap: maxFeePerGas,
		GasTipCap: maxPriorityFee,
		Data:      callData,
	})

	signer := types.NewCancunSigner(t.chainID)
	baselineSignedTx, err := types.SignTx(baselineTx, signer, t.builder.privateKey)
	if err != nil {
		return nil, fmt.Errorf("签名 Baseline 交易失败: %w", err)
	}

	baselineTxHash, err := t.SendRawTransaction(ctx, t.baselineClient, baselineSignedTx, fmt.Sprintf("%s_baseline", testName))
	if err != nil {
		return nil, fmt.Errorf("发送 Baseline 交易失败: %w", err)
	}
	result.BaselineTxHash = baselineTxHash.Hex()

	baselineReceipt, err := t.WaitForReceipt(ctx, t.baselineClient, baselineTxHash, 60*time.Second)
	if err != nil {
		return nil, fmt.Errorf("等待 Baseline 交易确认失败: %w", err)
	}
	result.BaselineReceipt = baselineReceipt

	targetNonce := baselineNonce + 1

	targetTx := types.NewTx(&types.DynamicFeeTx{
		ChainID:   t.chainID,
		Nonce:     targetNonce,
		To:        &targetContractAddr,
		Value:     big.NewInt(0),
		Gas:       gasLimit,
		GasFeeCap: maxFeePerGas,
		GasTipCap: maxPriorityFee,
		Data:      callData,
	})

	targetSignedTx, err := types.SignTx(targetTx, signer, t.builder.privateKey)
	if err != nil {
		return nil, fmt.Errorf("签名 Target 交易失败: %w", err)
	}

	targetTxHash, err := t.SendRawTransaction(ctx, t.targetClient, targetSignedTx, fmt.Sprintf("%s_target", testName))
	if err != nil {
		return nil, fmt.Errorf("发送 Target 交易失败: %w", err)
	}
	result.TargetTxHash = targetTxHash.Hex()

	targetReceipt, err := t.WaitForReceipt(ctx, t.targetClient, targetTxHash, 60*time.Second)
	if err != nil {
		return nil, fmt.Errorf("等待 Target 交易确认失败: %w", err)
	}
	result.TargetReceipt = targetReceipt

	match, diffErr := t.CompareReceipts(ctx, testName, baselineTxHash, targetTxHash)

	result.Success = baselineReceipt.Status == 1 && match

	if !result.Success && result.Error == "" {
		var errs []string
		if !match && diffErr != nil {
			errs = append(errs, diffErr.Error())
		}
		if baselineReceipt.Status != 1 {
			errs = append(errs, "Baseline Status != 1")
		}

		result.Error = strings.Join(errs, " | ")
	}

	return result, nil
}
