package report

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/ethereum-optimism/optimism/op-rpc-compat/pkg/diff"
	"github.com/ethereum-optimism/optimism/op-rpc-compat/pkg/rpc"
)

func TestReporterPrintsLogicalEndpointNames(t *testing.T) {
	r := NewReporter(EndpointMetadata{Name: "old-reth", URL: "http://old.example"}, EndpointMetadata{Name: "new-reth", URL: "http://new.example"}, true)
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	previous := os.Stdout
	os.Stdout = writer
	t.Cleanup(func() {
		os.Stdout = previous
		reader.Close()
		writer.Close()
	})
	r.AddResult(TestCase{Name: "transport", Method: "eth_chainId"}, &rpc.CompareResult{
		BaselineResponse: &rpc.ResponseWithMeta{Error: errors.New("baseline down")},
		TargetResponse:   &rpc.ResponseWithMeta{Error: errors.New("target down")},
	}, nil, nil)
	r.PrintSummary()
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	os.Stdout = previous
	output, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(output), "old-reth") || !strings.Contains(string(output), "new-reth") {
		t.Fatalf("missing logical names in output: %s", output)
	}
	if !strings.Contains(string(output), "\n    old-reth:") || !strings.Contains(string(output), "\n    new-reth:") {
		t.Fatalf("error details must use logical names: %s", output)
	}
	if strings.Contains(string(output), "geth 错误") || strings.Contains(string(output), "reth 错误") ||
		strings.Contains(string(output), "\n    geth:") || strings.Contains(string(output), "\n    reth:") {
		t.Fatalf("fixed client labels remain: %s", output)
	}
}

func TestRPCErrorDifferenceUsesLogicalEndpointNames(t *testing.T) {
	r := NewReporter(EndpointMetadata{Name: "old-reth"}, EndpointMetadata{Name: "new-reth"}, false)
	r.AddResult(TestCase{Name: "rpc-error", Method: "eth_call"}, &rpc.CompareResult{
		BaselineResponse: &rpc.ResponseWithMeta{Response: &rpc.Response{Error: &rpc.RPCError{Code: -32000, Message: "failed"}}},
		TargetResponse:   &rpc.ResponseWithMeta{Response: &rpc.Response{Result: json.RawMessage(`"0x1"`)}},
	}, nil, nil)
	result := r.Generate().Results[0]
	if len(result.Differences) == 0 || !strings.Contains(result.Differences[0].Message, "old-reth") || !strings.Contains(result.Differences[0].Message, "new-reth") {
		t.Fatalf("RPC error difference = %+v", result.Differences)
	}
}

func TestLoadKnownDiffsFromReader(t *testing.T) {
	r := NewReporter(EndpointMetadata{Name: "baseline", URL: "baseline"}, EndpointMetadata{Name: "target", URL: "target"}, false)
	data := strings.NewReader(`{"known_diffs":[{"test_name":"sample","reason":"expected difference"}]}`)
	if err := r.LoadKnownDiffs(data); err != nil {
		t.Fatal(err)
	}
	if got := r.GetKnownDiff("sample"); got == nil || got.Reason != "expected difference" {
		t.Fatalf("known difference = %+v", got)
	}
}

func TestReportUsesClientIndependentSchema(t *testing.T) {
	r := NewReporter(
		EndpointMetadata{Name: "old-reth", URL: "http://baseline.example", ClientVersion: "reth/2.4"},
		EndpointMetadata{Name: "new-reth", URL: "http://target.example", ClientVersion: "reth/2.5"},
		false,
	)
	r.AddCompatibleResult(TestCase{Name: "sample", Method: "eth_chainId"}, &rpc.CompareResult{
		BaselineResponse: &rpc.ResponseWithMeta{RawBody: []byte(`{"result":"0x1"}`)},
		TargetResponse:   &rpc.ResponseWithMeta{RawBody: []byte(`{"result":"0x2"}`)},
	}, "expected difference")

	data, err := json.Marshal(r.Generate())
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["schema_version"] != float64(2) {
		t.Fatalf("schema_version = %v", decoded["schema_version"])
	}
	for key, want := range map[string]string{"baseline": "old-reth", "target": "new-reth"} {
		endpoint, ok := decoded[key].(map[string]any)
		if !ok || endpoint["name"] != want || endpoint["client_version"] == "" {
			t.Fatalf("%s metadata = %v", key, decoded[key])
		}
	}
	if decoded["geth_url"] != nil || decoded["reth_url"] != nil {
		t.Fatalf("legacy endpoint fields remain: %s", data)
	}
	results := decoded["results"].([]any)
	result := results[0].(map[string]any)
	if result["baseline_response"] == nil || result["target_response"] == nil {
		t.Fatalf("missing client-independent response fields: %v", result)
	}
	if result["geth_response"] != nil || result["reth_response"] != nil {
		t.Fatalf("legacy response fields remain: %v", result)
	}
}

func TestKnownDiffApplicability(t *testing.T) {
	config := `{"known_diffs":[
		{"test_name":"pair-specific","reason":"client difference",
		 "applies_to":{"baseline":{"name_pattern":"^op-geth$","version_pattern":"^Geth/v1[.]6[.]"},
		               "target":{"name_pattern":"^op-reth$","version_pattern":"^op-reth/v2[.]4[.]"}},
		 "baseline_example":{"error":{"code":-32601,"message":"missing"}},
		 "target_example":{"result":"0x0"}},
		{"test_name":"generic","reason":"implementation-independent"}
	]}`
	cases := []struct {
		name             string
		baseline, target EndpointMetadata
		wantSpecific     bool
	}{
		{"op-geth/op-reth", EndpointMetadata{Name: "op-geth", ClientVersion: "Geth/v1.6.1"}, EndpointMetadata{Name: "op-reth", ClientVersion: "op-reth/v2.4.2"}, true},
		{"old/new reth", EndpointMetadata{Name: "old-reth", ClientVersion: "op-reth/v2.3.0"}, EndpointMetadata{Name: "new-reth", ClientVersion: "op-reth/v2.4.2"}, false},
		{"wrong target version", EndpointMetadata{Name: "op-geth", ClientVersion: "Geth/v1.6.1"}, EndpointMetadata{Name: "op-reth", ClientVersion: "op-reth/v2.5.0"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := NewReporter(tc.baseline, tc.target, false)
			if err := r.LoadKnownDiffs(strings.NewReader(config)); err != nil {
				t.Fatal(err)
			}
			got := r.GetKnownDiff("pair-specific")
			if (got != nil) != tc.wantSpecific {
				t.Fatalf("pair-specific rule = %+v, want applicable = %v", got, tc.wantSpecific)
			}
			if r.GetKnownDiff("generic") == nil {
				t.Fatal("unconditional rule must remain applicable")
			}
		})
	}
}

func TestNonApplicableKnownDiffReportsFailure(t *testing.T) {
	r := NewReporter(EndpointMetadata{Name: "old-reth"}, EndpointMetadata{Name: "new-reth"}, false)
	config := `{"known_diffs":[{"test_name":"different","reason":"geth/reth only",
		"applies_to":{"baseline":{"name_pattern":"^op-geth$"},"target":{"name_pattern":"^op-reth$"}},
		"baseline_example":{"result":"0x1"},"target_example":{"result":"0x2"}}]}`
	if err := r.LoadKnownDiffs(strings.NewReader(config)); err != nil {
		t.Fatal(err)
	}
	r.AddResult(TestCase{Name: "different", Method: "eth_chainId"}, &rpc.CompareResult{
		BaselineResponse: &rpc.ResponseWithMeta{RawBody: []byte(`{"result":"0x1"}`)},
		TargetResponse:   &rpc.ResponseWithMeta{RawBody: []byte(`{"result":"0x2"}`)},
	}, &diff.CompareResult{
		Differences: []diff.Difference{{Type: diff.DiffTypeValue, Severity: diff.SeverityFail}},
		FailCount:   1,
	}, nil)
	result := r.Generate().Results[0]
	if result.Status != StatusFail || result.Passed || result.SkipReason != "" {
		t.Fatalf("non-applicable known difference was waived: %+v", result)
	}
}

func TestAddCompatibleResultPreservesRPCErrorWithoutFailing(t *testing.T) {
	r := NewReporter(EndpointMetadata{Name: "geth", URL: "geth"}, EndpointMetadata{Name: "reth", URL: "reth"}, false)
	rpcBody := json.RawMessage(`{"jsonrpc":"2.0","id":1,"error":{"code":-32000,"message":"already known"}}`)
	response := &rpc.ResponseWithMeta{
		Response: &rpc.Response{
			JSONRPC: "2.0",
			ID:      1,
			Error:   &rpc.RPCError{Code: -32000, Message: "already known"},
		},
		RawBody: rpcBody,
	}
	compareResult := &rpc.CompareResult{TargetResponse: response}

	r.AddCompatibleResult(
		TestCase{Name: "forwarded_reth", Method: "eth_sendRawTransaction"},
		compareResult,
		"the shared sequencer already knows the forwarded transaction",
	)

	if len(r.results) != 1 {
		t.Fatalf("results length = %d, want 1", len(r.results))
	}
	result := r.results[0]
	if result.Status != StatusCompatible || !result.Passed {
		t.Fatalf("status = %s, passed = %v; want COMPATIBLE and true", result.Status, result.Passed)
	}
	if string(result.TargetResponse) != string(rpcBody) {
		t.Fatalf("reth response = %s, want %s", result.TargetResponse, rpcBody)
	}
	if result.SkipReason == "" {
		t.Fatal("compatible result must retain its reason")
	}
}

// Test_matchesKnownDiff checks the stricter matching rules:
//   - Compare only type and code on the reference side, where wording may vary.
//   - Compare type, code, and normalized message on the target side to detect drift while tolerating truncation.
func Test_matchesKnownDiff(t *testing.T) {
	r := &Reporter{}
	raw := func(s string) json.RawMessage { return json.RawMessage(s) }
	errObj := func(code int, msg string) string {
		b, _ := json.Marshal(map[string]any{"error": map[string]any{"code": code, "message": msg}})
		return string(b)
	}
	okObj := func(result string) string {
		b, _ := json.Marshal(map[string]any{"result": result})
		return string(b)
	}

	cases := []struct {
		name            string
		gethEx, gethAct string
		rethEx, rethAct string
		want            bool
	}{
		{
			name:   "reth message drift surfaces (insufficient funds -> OutOfFunds)",
			gethEx: errObj(-32000, "insufficient funds for transfer"), gethAct: errObj(-32000, "failed with 60000000 gas: insufficient funds for transfer: address 0x1"),
			rethEx: errObj(-32003, "insufficient funds for transfer"), rethAct: errObj(-32003, "EVM error: OutOfFunds"),
			want: false, // changed target wording must not be waived
		},
		{
			name:   "geth wording varies but reth stable -> still exempt",
			gethEx: errObj(-32602, "invalid argument 1: unknown block number tag"), gethAct: errObj(-32602, "invalid argument 1: hex string without 0x prefix"),
			rethEx: errObj(-32602, "Invalid params"), rethAct: errObj(-32602, "Invalid params"),
			want: true, // reference code and target message still match
		},
		{
			name:   "truncated reth record tolerated (contains)",
			gethEx: errObj(-32000, "insufficient funds"), gethAct: errObj(-32000, "insufficient funds for transfer: address 0xabc"),
			rethEx: errObj(-32003, "insufficient funds"), rethAct: errObj(-32003, "insufficient funds for transfer: address 0xabc"),
			want: true,
		},
		{
			name:   "reth code change surfaces",
			gethEx: errObj(-32000, "x"), gethAct: errObj(-32000, "x"),
			rethEx: errObj(-32003, "intrinsic gas too low"), rethAct: errObj(-32000, "intrinsic gas too low"),
			want: false, // reth code -32003 -> -32000
		},
		{
			name:   "geth code change surfaces",
			gethEx: errObj(-32000, "out of gas"), gethAct: errObj(-32003, "out of gas"),
			rethEx: errObj(-32003, "out of gas"), rethAct: errObj(-32003, "out of gas"),
			want: false, // a changed reference code must be visible
		},
		{
			name:   "success responses exempt on type only (volatile results)",
			gethEx: okObj("Geth/v1.16"), gethAct: okObj("Geth/v1.17"),
			rethEx: okObj("op-reth-rc4"), rethAct: okObj("op-reth-rc5"),
			want: true,
		},
		{
			name:   "type mismatch surfaces (reth error->success)",
			gethEx: errObj(-32000, "x"), gethAct: errObj(-32000, "x"),
			rethEx: errObj(-32602, "unknown account"), rethAct: okObj("0xraw"),
			want: false, // eth_fillTransaction target changed from error to success
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			kd := &KnownDiff{BaselineExample: raw(c.gethEx), TargetExample: raw(c.rethEx)}
			res := &TestResult{BaselineResponse: raw(c.gethAct), TargetResponse: raw(c.rethAct)}
			if got := r.matchesKnownDiff(res, kd); got != c.want {
				t.Fatalf("matchesKnownDiff = %v, want %v", got, c.want)
			}
		})
	}
}
