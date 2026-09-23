package cmd

import (
	"testing"

	"github.com/ethereum-optimism/optimism/op-rpc-compat/pkg/report"
)

func TestReplaceTemplateVarsBlockOneRLP(t *testing.T) {
	tests := []report.TestCase{{
		Name:   "raw-block",
		Method: "debug_traceBlock",
		Params: []interface{}{"{{block_one_rlp}}", map[string]interface{}{}},
	}}

	got := replaceTemplateVars(tests, &TemplateVars{BlockOneRLP: "0xf90123"})
	params, ok := got[0].Params.([]interface{})
	if !ok {
		t.Fatalf("params type = %T, want []interface{}", got[0].Params)
	}
	if params[0] != "0xf90123" {
		t.Fatalf("raw block template = %v, want 0xf90123", params[0])
	}
}

func TestIncludePreconfTransactions(t *testing.T) {
	if !includePreconfTransactions(false) {
		t.Fatal("default transaction mode must include preconf scenarios")
	}
	if includePreconfTransactions(true) {
		t.Fatal("standard-only transaction mode must exclude preconf scenarios")
	}
}
