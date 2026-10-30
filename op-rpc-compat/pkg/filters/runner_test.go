package filters

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ethereum-optimism/optimism/op-rpc-compat/pkg/rpc"
)

func TestVerifyUsesEachEndpointFilterIDAndUninstalls(t *testing.T) {
	serve := func(filterID string) (*httptest.Server, *int, *int) {
		queries, uninstalls := new(int), new(int)
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var request struct {
				ID     int      `json:"id"`
				Method string   `json:"method"`
				Params []string `json:"params"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Errorf("decode request: %v", err)
				return
			}
			var result any
			switch request.Method {
			case "eth_newBlockFilter":
				result = filterID
			case "eth_getFilterChanges":
				if len(request.Params) != 1 || request.Params[0] != filterID {
					t.Errorf("queried wrong filter: %v", request.Params)
				}
				*queries++
				result = []any{}
			case "eth_uninstallFilter":
				if len(request.Params) != 1 || request.Params[0] != filterID {
					t.Errorf("uninstalled wrong filter: %v", request.Params)
				}
				*uninstalls++
				result = true
			default:
				t.Errorf("unexpected method: %s", request.Method)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": result})
		}))
		return server, queries, uninstalls
	}
	baseline, baselineQueries, baselineUninstalls := serve("0x1")
	defer baseline.Close()
	target, targetQueries, targetUninstalls := serve("0x2")
	defer target.Close()
	pair := rpc.NewClientPair(baseline.URL, "baseline", target.URL, "target", time.Second)
	responses, err := Verify(t.Context(), pair, rpc.NewRequest("eth_newBlockFilter", []any{}))
	if err != nil || responses == nil || *baselineQueries != 1 || *targetQueries != 1 ||
		*baselineUninstalls != 1 || *targetUninstalls != 1 {
		t.Fatalf("filter verification: responses=%+v err=%v counts=%d/%d %d/%d", responses, err,
			*baselineQueries, *targetQueries, *baselineUninstalls, *targetUninstalls)
	}
	if string(responses.BaselineResponse.Response.Result) == string(responses.TargetResponse.Response.Result) {
		t.Fatal("test endpoints unexpectedly returned the same filter ID")
	}
}

func TestVerifyRejectsInvalidFilterQuery(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request rpc.Request
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		var result any = "0x1"
		if request.Method == "eth_getFilterChanges" {
			result = "not an array"
		} else if request.Method == "eth_uninstallFilter" {
			result = true
		}
		_, _ = fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%d,"result":%s}`, request.ID, mustJSON(t, result))
	}))
	defer server.Close()
	pair := rpc.NewClientPair(server.URL, "baseline", server.URL, "target", time.Second)
	if _, err := Verify(t.Context(), pair, rpc.NewRequest("eth_newBlockFilter", []any{})); err == nil {
		t.Fatal("invalid filter query was accepted")
	}
}

func mustJSON(t *testing.T, value any) string {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
