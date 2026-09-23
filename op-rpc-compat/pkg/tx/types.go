// Package tx 提供交易构建、签名和测试功能
package tx

import (
	"encoding/json"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
)

// TxType 交易类型
type TxType int

const (
	TxTypeLegacy TxType = iota
	TxTypeEIP1559
	TxTypeEIP7702
)

func (t TxType) String() string {
	switch t {
	case TxTypeLegacy:
		return "Legacy"
	case TxTypeEIP1559:
		return "EIP-1559"
	case TxTypeEIP7702:
		return "EIP-7702"
	default:
		return "Unknown"
	}
}

// TxParams 交易参数
type TxParams struct {
	From     common.Address
	To       *common.Address // nil 表示合约创建
	Value    *big.Int
	Data     []byte
	Gas      uint64
	GasPrice *big.Int // Legacy TX

	// EIP-1559 参数
	MaxFeePerGas         *big.Int
	MaxPriorityFeePerGas *big.Int

	// EIP-7702 参数
	AuthList []types.SetCodeAuthorization

	Nonce   uint64
	ChainID *big.Int
}

// PreconfResponse eth_sendRawTransactionWithPreconf 的响应
type PreconfResponse struct {
	TxHash      string          `json:"txHash"`
	Status      string          `json:"status"`
	Reason      *string         `json:"reason"`
	BlockHeight string          `json:"blockHeight"`
	Receipt     json.RawMessage `json:"receipt,omitempty"`
}

// StateSnapshot 链上状态快照
type StateSnapshot struct {
	BlockNumber uint64                 `json:"block_number"`
	Balance     *big.Int               `json:"balance"`
	Nonce       uint64                 `json:"nonce"`
	Code        []byte                 `json:"code,omitempty"`
	Storage     map[string]string      `json:"storage,omitempty"`
	Proof       map[string]interface{} `json:"proof,omitempty"`
}

// TxTestResult 交易测试结果
type TxTestResult struct {
	TestName    string `json:"test_name"`
	TxType      string `json:"tx_type"`
	Passed      bool   `json:"passed"`
	GethTxHash  string `json:"geth_tx_hash,omitempty"`
	RethTxHash  string `json:"reth_tx_hash,omitempty"`
	Error       string `json:"error,omitempty"`
	GethReceipt *TxReceipt `json:"geth_receipt,omitempty"`
	RethReceipt *TxReceipt `json:"reth_receipt,omitempty"`

	// 状态对比
	StateComparison *StateComparison `json:"state_comparison,omitempty"`

	// Preconf 响应（如果使用 preconf 方式）
	GethPreconf *PreconfResponse `json:"geth_preconf,omitempty"`
	RethPreconf *PreconfResponse `json:"reth_preconf,omitempty"`
}

// TxReceipt 简化的交易收据
type TxReceipt struct {
	Status            uint64 `json:"status"`
	GasUsed           uint64 `json:"gasUsed"`
	BlockNumber       uint64 `json:"blockNumber"`
	TransactionHash   string `json:"transactionHash"`
	ContractAddress   string `json:"contractAddress,omitempty"`
	CumulativeGasUsed uint64 `json:"cumulativeGasUsed"`
}

// StateComparison 状态对比结果
type StateComparison struct {
	// 余额变化
	GethBalanceDelta *big.Int `json:"geth_balance_delta"`
	RethBalanceDelta *big.Int `json:"reth_balance_delta"`
	BalanceMatch     bool     `json:"balance_match"`

	// Gas 使用
	GethGasUsed uint64 `json:"geth_gas_used"`
	RethGasUsed uint64 `json:"reth_gas_used"`
	GasMatch    bool   `json:"gas_match"`

	// Receipt 状态
	GethStatus uint64 `json:"geth_status"`
	RethStatus uint64 `json:"reth_status"`
	StatusMatch bool  `json:"status_match"`

	// 详细差异
	Differences []string `json:"differences,omitempty"`
}

// ContractDeployResult 合约部署测试结果
type ContractDeployResult struct {
	TestName             string                    `json:"test_name"`
	Success              bool                      `json:"success"`
	GethTxHash           string                    `json:"geth_tx_hash,omitempty"`
	RethTxHash           string                    `json:"reth_tx_hash,omitempty"`
	GethContractAddress  string                    `json:"geth_contract_address,omitempty"`
	RethContractAddress  string                    `json:"reth_contract_address,omitempty"`
	GethReceipt          *TxReceipt                `json:"geth_receipt,omitempty"`
	RethReceipt          *TxReceipt                `json:"reth_receipt,omitempty"`
	Error                string                    `json:"error,omitempty"`
}

// ContractCallResult 合约调用（eth_call）结果
type ContractCallResult struct {
	TestName        string `json:"test_name"`
	Success         bool   `json:"success"`
	ContractAddress string `json:"contract_address"`
	GethResult      string `json:"geth_result,omitempty"`
	RethResult      string `json:"reth_result,omitempty"`
	ResultMatch     bool   `json:"result_match"`
	Error           string `json:"error,omitempty"`
}

// ContractTxResult 合约交易（状态修改）结果
type ContractTxResult struct {
	TestName        string               `json:"test_name"`
	Success         bool                 `json:"success"`
	ContractAddress string               `json:"contract_address"`
	GethTxHash      string               `json:"geth_tx_hash,omitempty"`
	RethTxHash      string               `json:"reth_tx_hash,omitempty"`
	GethReceipt     *TxReceipt           `json:"geth_receipt,omitempty"`
	RethReceipt     *TxReceipt           `json:"reth_receipt,omitempty"`
	Error           string               `json:"error,omitempty"`
}
