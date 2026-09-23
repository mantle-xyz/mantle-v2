// Package tx provides transaction construction, signing, and comparison tests.
package tx

import (
	"encoding/json"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
)

// TxType identifies a transaction envelope type.
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

// TxParams contains inputs for constructing a transaction.
type TxParams struct {
	From     common.Address
	To       *common.Address // nil creates a contract
	Value    *big.Int
	Data     []byte
	Gas      uint64
	GasPrice *big.Int // Legacy TX

	// EIP-1559 fee fields.
	MaxFeePerGas         *big.Int
	MaxPriorityFeePerGas *big.Int

	// EIP-7702 authorization list.
	AuthList []types.SetCodeAuthorization

	Nonce   uint64
	ChainID *big.Int
}

// PreconfResponse is the response from eth_sendRawTransactionWithPreconf.
type PreconfResponse struct {
	TxHash      string          `json:"txHash"`
	Status      string          `json:"status"`
	Reason      *string         `json:"reason"`
	BlockHeight string          `json:"blockHeight"`
	Receipt     json.RawMessage `json:"receipt,omitempty"`
}

// StateSnapshot captures on-chain state at a block.
type StateSnapshot struct {
	BlockNumber uint64                 `json:"block_number"`
	Balance     *big.Int               `json:"balance"`
	Nonce       uint64                 `json:"nonce"`
	Code        []byte                 `json:"code,omitempty"`
	Storage     map[string]string      `json:"storage,omitempty"`
	Proof       map[string]interface{} `json:"proof,omitempty"`
}

// TxTestResult records a transaction comparison result.
type TxTestResult struct {
	TestName    string `json:"test_name"`
	TxType      string `json:"tx_type"`
	Passed      bool   `json:"passed"`
	GethTxHash  string `json:"geth_tx_hash,omitempty"`
	RethTxHash  string `json:"reth_tx_hash,omitempty"`
	Error       string `json:"error,omitempty"`
	GethReceipt *TxReceipt `json:"geth_receipt,omitempty"`
	RethReceipt *TxReceipt `json:"reth_receipt,omitempty"`

	// State comparison.
	StateComparison *StateComparison `json:"state_comparison,omitempty"`

	// Preconfirmation responses, when applicable.
	GethPreconf *PreconfResponse `json:"geth_preconf,omitempty"`
	RethPreconf *PreconfResponse `json:"reth_preconf,omitempty"`
}

// TxReceipt contains the receipt fields needed for comparison.
type TxReceipt struct {
	Status            uint64 `json:"status"`
	GasUsed           uint64 `json:"gasUsed"`
	BlockNumber       uint64 `json:"blockNumber"`
	TransactionHash   string `json:"transactionHash"`
	ContractAddress   string `json:"contractAddress,omitempty"`
	CumulativeGasUsed uint64 `json:"cumulativeGasUsed"`
}

// StateComparison records differences between client state snapshots.
type StateComparison struct {
	// Balance changes.
	GethBalanceDelta *big.Int `json:"geth_balance_delta"`
	RethBalanceDelta *big.Int `json:"reth_balance_delta"`
	BalanceMatch     bool     `json:"balance_match"`

	// Gas usage.
	GethGasUsed uint64 `json:"geth_gas_used"`
	RethGasUsed uint64 `json:"reth_gas_used"`
	GasMatch    bool   `json:"gas_match"`

	// Receipt status.
	GethStatus uint64 `json:"geth_status"`
	RethStatus uint64 `json:"reth_status"`
	StatusMatch bool  `json:"status_match"`

	// Detailed differences.
	Differences []string `json:"differences,omitempty"`
}

// ContractDeployResult records a contract deployment comparison.
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

// ContractCallResult records an eth_call comparison.
type ContractCallResult struct {
	TestName        string `json:"test_name"`
	Success         bool   `json:"success"`
	ContractAddress string `json:"contract_address"`
	GethResult      string `json:"geth_result,omitempty"`
	RethResult      string `json:"reth_result,omitempty"`
	ResultMatch     bool   `json:"result_match"`
	Error           string `json:"error,omitempty"`
}

// ContractTxResult records a state-changing contract transaction comparison.
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
