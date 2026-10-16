package cmd

import "github.com/ethereum-optimism/optimism/op-rpc-compat/pkg/report"

func caseSuite(tc report.TestCase) string {
	switch tc.Method {
	case "eth_blockNumber", "eth_gasPrice", "eth_maxPriorityFeePerGas", "eth_syncing",
		"net_peerCount", "txpool_status", "txpool_content", "txpool_inspect":
		if tc.Params == nil {
			return "diagnostic"
		}
		if params, ok := tc.Params.([]any); ok && len(params) == 0 {
			return "diagnostic"
		}
	case "txpool_contentFrom":
		return "diagnostic"
	}
	for _, tag := range caseDynamicTags(tc) {
		if tag == "pending" || tag == "safe" || tag == "finalized" {
			return "diagnostic"
		}
	}
	return "core"
}

func selectSuite(cases []report.TestCase, mode string) (selected, excluded []report.TestCase) {
	for _, tc := range cases {
		if mode == "all" || caseSuite(tc) == mode {
			selected = append(selected, tc)
		} else {
			excluded = append(excluded, tc)
		}
	}
	return selected, excluded
}
