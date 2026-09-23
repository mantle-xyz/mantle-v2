package rpc

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestClientPairUsesLogicalNamesAndResponseDirection(t *testing.T) {
	serve := func(result string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":"` + result + `"}`))
		}))
	}
	baseline := serve("0x1")
	defer baseline.Close()
	target := serve("0x2")
	defer target.Close()

	pair := NewClientPair(baseline.URL, "old-reth", target.URL, "new-reth", time.Second)
	if pair.Baseline.Name() != "old-reth" || pair.Target.Name() != "new-reth" {
		t.Fatalf("client names = %q, %q", pair.Baseline.Name(), pair.Target.Name())
	}
	result := pair.Compare(context.Background(), NewRequest("eth_chainId", []any{}))
	if string(result.BaselineResponse.Response.Result) != `"0x1"` || string(result.TargetResponse.Response.Result) != `"0x2"` {
		t.Fatalf("response direction = %s, %s", result.BaselineResponse.Response.Result, result.TargetResponse.Response.Result)
	}
}
