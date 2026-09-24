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
	TestName        string     `json:"test_name"`
	TxType          string     `json:"tx_type"`
	Passed          bool       `json:"passed"`
	BaselineTxHash  string     `json:"baseline_tx_hash,omitempty"`
	TargetTxHash    string     `json:"target_tx_hash,omitempty"`
	Error           string     `json:"error,omitempty"`
	BaselineReceipt *TxReceipt `json:"baseline_receipt,omitempty"`
	TargetReceipt   *TxReceipt `json:"target_receipt,omitempty"`

	// State comparison.
	StateComparison *StateComparison `json:"state_comparison,omitempty"`

	// Preconfirmation responses, when applicable.
	BaselinePreconf *PreconfResponse `json:"baseline_preconf,omitempty"`
	TargetPreconf   *PreconfResponse `json:"target_preconf,omitempty"`
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
	BaselineBalanceDelta *big.Int `json:"baseline_balance_delta"`
	TargetBalanceDelta   *big.Int `json:"target_balance_delta"`
	BalanceMatch         bool     `json:"balance_match"`

	// Gas usage.
	BaselineGasUsed uint64 `json:"baseline_gas_used"`
	TargetGasUsed   uint64 `json:"target_gas_used"`
	GasMatch        bool   `json:"gas_match"`

	// Receipt status.
	BaselineStatus uint64 `json:"baseline_status"`
	TargetStatus   uint64 `json:"target_status"`
	StatusMatch    bool   `json:"status_match"`

	// Detailed differences.
	Differences []string `json:"differences,omitempty"`
}

// ContractDeployResult records a contract deployment comparison.
type ContractDeployResult struct {
	TestName                string     `json:"test_name"`
	Success                 bool       `json:"success"`
	BaselineTxHash          string     `json:"baseline_tx_hash,omitempty"`
	TargetTxHash            string     `json:"target_tx_hash,omitempty"`
	BaselineContractAddress string     `json:"baseline_contract_address,omitempty"`
	TargetContractAddress   string     `json:"target_contract_address,omitempty"`
	BaselineReceipt         *TxReceipt `json:"baseline_receipt,omitempty"`
	TargetReceipt           *TxReceipt `json:"target_receipt,omitempty"`
	Error                   string     `json:"error,omitempty"`
}

// ContractCallResult records an eth_call comparison.
type ContractCallResult struct {
	TestName        string `json:"test_name"`
	Success         bool   `json:"success"`
	ContractAddress string `json:"contract_address"`
	BaselineResult  string `json:"baseline_result,omitempty"`
	TargetResult    string `json:"target_result,omitempty"`
	ResultMatch     bool   `json:"result_match"`
	Error           string `json:"error,omitempty"`
}

// ContractTxResult records a state-changing contract transaction comparison.
type ContractTxResult struct {
	TestName        string     `json:"test_name"`
	Success         bool       `json:"success"`
	ContractAddress string     `json:"contract_address"`
	BaselineTxHash  string     `json:"baseline_tx_hash,omitempty"`
	TargetTxHash    string     `json:"target_tx_hash,omitempty"`
	BaselineReceipt *TxReceipt `json:"baseline_receipt,omitempty"`
	TargetReceipt   *TxReceipt `json:"target_receipt,omitempty"`
	Error           string     `json:"error,omitempty"`
}
