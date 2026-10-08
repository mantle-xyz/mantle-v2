package rpc

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestClientPairUsesLogicalNamesAndResponseDirection(t *testing.T) {
	serve := func(result string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var request Request
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Errorf("decode request: %v", err)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": result})
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

func TestCallRejectsInvalidRPCEnvelope(t *testing.T) {
	tests := []struct {
		name   string
		body   func(int) string
		status int
	}{
		{"missing result and error", func(id int) string { return fmt.Sprintf(`{"jsonrpc":"2.0","id":%d}`, id) }, http.StatusOK},
		{"result and error", func(id int) string {
			return fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"result":null,"error":{"code":-1,"message":"bad"}}`, id)
		}, http.StatusOK},
		{"wrong id", func(id int) string { return fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"result":null}`, id+1) }, http.StatusOK},
		{"wrong version", func(id int) string { return fmt.Sprintf(`{"jsonrpc":"1.0","id":%d,"result":null}`, id) }, http.StatusOK},
		{"non-object", func(int) string { return `[]` }, http.StatusOK},
		{"invalid error", func(id int) string { return fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"error":{"message":"bad"}}`, id) }, http.StatusOK},
		{"null error code", func(id int) string {
			return fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"error":{"code":null,"message":"bad"}}`, id)
		}, http.StatusOK},
		{"null error message", func(id int) string {
			return fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"error":{"code":-1,"message":null}}`, id)
		}, http.StatusOK},
		{"HTTP failure", func(id int) string { return fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"result":null}`, id) }, http.StatusServiceUnavailable},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request Request
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Errorf("decode request: %v", err)
					return
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body(request.ID)))
			}))
			defer server.Close()
			response := NewClient(server.URL, "baseline", time.Second).Call(t.Context(), NewRequest("eth_chainId", []any{}))
			if response.Error == nil || SuccessfulResponse(response) {
				t.Fatalf("invalid envelope was accepted: %+v", response)
			}
		})
	}
}

func TestCallAcceptsExplicitNullResult(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request Request
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		_, _ = fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%d,"result":null}`, request.ID)
	}))
	defer server.Close()
	response := NewClient(server.URL, "baseline", time.Second).Call(t.Context(), NewRequest("eth_getTransactionReceipt", []any{}))
	if !SuccessfulResponse(response) || string(response.Response.Result) != "null" {
		t.Fatalf("explicit null result was rejected: %+v", response)
	}
}
