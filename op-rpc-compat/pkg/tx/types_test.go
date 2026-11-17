package tx

import (
	"encoding/json"
	"math/big"
	"strings"
	"testing"
)

func TestTransactionResultsUseClientIndependentJSON(t *testing.T) {
	cases := []struct {
		name  string
		value any
		keys  []string
	}{
		{"transfer", TxTestResult{BaselineTxHash: "0x1", TargetTxHash: "0x2"}, []string{"baseline_tx_hash", "target_tx_hash"}},
		{"state", StateComparison{BaselineBalanceDelta: big.NewInt(1), TargetBalanceDelta: big.NewInt(2)}, []string{"baseline_balance_delta", "target_balance_delta"}},
		{"deployment", ContractDeployResult{BaselineContractAddress: "0x1", TargetContractAddress: "0x2"}, []string{"baseline_contract_address", "target_contract_address"}},
		{"call", ContractCallResult{BaselineResult: "0x1", TargetResult: "0x2"}, []string{"baseline_result", "target_result"}},
		{"contract transaction", ContractTxResult{BaselineTxHash: "0x1", TargetTxHash: "0x2"}, []string{"baseline_tx_hash", "target_tx_hash"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			data, err := json.Marshal(tc.value)
			if err != nil {
				t.Fatal(err)
			}
			for _, key := range tc.keys {
				if !strings.Contains(string(data), `"`+key+`"`) {
					t.Errorf("missing %s in %s", key, data)
				}
			}
			if strings.Contains(string(data), `"geth_`) || strings.Contains(string(data), `"reth_`) {
				t.Errorf("legacy client key remains: %s", data)
			}
		})
	}
}
