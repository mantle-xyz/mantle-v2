package report

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/ethereum-optimism/optimism/op-rpc-compat/pkg/rpc"
)

func TestLoadKnownDiffsFromReader(t *testing.T) {
	r := NewReporter("baseline", "target", false)
	data := strings.NewReader(`{"known_diffs":[{"test_name":"sample","reason":"expected difference"}]}`)
	if err := r.LoadKnownDiffs(data); err != nil {
		t.Fatal(err)
	}
	if got := r.GetKnownDiff("sample"); got == nil || got.Reason != "expected difference" {
		t.Fatalf("known difference = %+v", got)
	}
}

func TestAddCompatibleResultPreservesRPCErrorWithoutFailing(t *testing.T) {
	r := NewReporter("geth", "reth", false)
	rpcBody := json.RawMessage(`{"jsonrpc":"2.0","id":1,"error":{"code":-32000,"message":"already known"}}`)
	response := &rpc.ResponseWithMeta{
		Response: &rpc.Response{
			JSONRPC: "2.0",
			ID:      1,
			Error:   &rpc.RPCError{Code: -32000, Message: "already known"},
		},
		RawBody: rpcBody,
	}
	compareResult := &rpc.CompareResult{SecondaryResponse: response}

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
	if string(result.RethResponse) != string(rpcBody) {
		t.Fatalf("reth response = %s, want %s", result.RethResponse, rpcBody)
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
			kd := &KnownDiff{GethExample: raw(c.gethEx), RethExample: raw(c.rethEx)}
			res := &TestResult{GethResponse: raw(c.gethAct), RethResponse: raw(c.rethAct)}
			if got := r.matchesKnownDiff(res, kd); got != c.want {
				t.Fatalf("matchesKnownDiff = %v, want %v", got, c.want)
			}
		})
	}
}
