package cmd

import (
	"testing"

	"github.com/ethereum-optimism/optimism/op-rpc-compat/pkg/report"
)

func TestCaseSuiteRequiresDiagnosticOnlyForUnstableTags(t *testing.T) {
	for _, tc := range []struct {
		name   string
		method string
		params any
		want   string
	}{
		{"fixed block", "eth_getBalance", []any{"0x1"}, "core"},
		{"latest with snapshot check", "eth_getBalance", []any{"latest"}, "core"},
		{"pending", "eth_getBalance", []any{"pending"}, "diagnostic"},
		{"safe", "eth_getBalance", []any{"safe"}, "diagnostic"},
		{"finalized in filter", "eth_getLogs", []any{map[string]any{"toBlock": "finalized"}}, "diagnostic"},
		{"moving block height", "eth_blockNumber", []any{}, "diagnostic"},
		{"invalid block number parameters", "eth_blockNumber", []any{"unexpected"}, "core"},
		{"local gas price", "eth_gasPrice", []any{}, "diagnostic"},
		{"invalid gas price parameters", "eth_gasPrice", []any{"unexpected"}, "core"},
		{"local priority fee", "eth_maxPriorityFeePerGas", []any{}, "diagnostic"},
		{"local sync status", "eth_syncing", []any{}, "diagnostic"},
		{"local peer count", "net_peerCount", []any{}, "diagnostic"},
		{"local txpool status", "txpool_status", []any{}, "diagnostic"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := caseSuite(report.TestCase{Method: tc.method, Params: tc.params}); got != tc.want {
				t.Fatalf("suite = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestSelectSuitePreservesCasesAndMakesExclusionsExplicit(t *testing.T) {
	cases := []report.TestCase{
		{Name: "fixed", Params: []any{"0x1"}, CorpusID: "embedded"},
		{Name: "pending", Params: []any{"pending"}, CorpusID: "embedded"},
		{Name: "latest", Params: []any{"latest"}, CorpusID: "embedded"},
	}
	for _, tc := range []struct {
		mode     string
		selected []string
		excluded []string
	}{
		{"core", []string{"fixed", "latest"}, []string{"pending"}},
		{"diagnostic", []string{"pending"}, []string{"fixed", "latest"}},
		{"all", []string{"fixed", "pending", "latest"}, nil},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			selected, excluded := selectSuite(cases, tc.mode)
			if len(selected) != len(tc.selected) || len(excluded) != len(tc.excluded) {
				t.Fatalf("selected=%v excluded=%v", selected, excluded)
			}
			for i, name := range tc.selected {
				if selected[i].Name != name || selected[i].CorpusID != "embedded" {
					t.Fatalf("selected[%d] = %+v", i, selected[i])
				}
			}
			for i, name := range tc.excluded {
				if excluded[i].Name != name || excluded[i].CorpusID != "embedded" {
					t.Fatalf("excluded[%d] = %+v", i, excluded[i])
				}
			}
		})
	}
}
