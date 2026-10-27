package cmd

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ethereum-optimism/optimism/op-rpc-compat/pkg/report"
	"github.com/ethereum-optimism/optimism/op-rpc-compat/pkg/rpc"
)

func snapshotServer(t *testing.T, hash string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request rpc.Request
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		var result any = "0x1"
		if request.Method == "eth_getBlockByNumber" {
			result = map[string]any{"hash": hash}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": result})
	}))
}

func TestDynamicTagsFoundInsideParams(t *testing.T) {
	tags := dynamicTags([]any{map[string]any{"fromBlock": "latest", "toBlock": "safe"}})
	if len(tags) != 2 || tags[0] != "latest" || tags[1] != "safe" {
		t.Fatalf("tags = %v", tags)
	}
}

func TestCompareAtStableSnapshot(t *testing.T) {
	hash := "0x" + strings.Repeat("11", 32)
	baseline := snapshotServer(t, hash)
	defer baseline.Close()
	target := snapshotServer(t, hash)
	defer target.Close()
	pair := rpc.NewClientPair(baseline.URL, "baseline", target.URL, "target", time.Second)
	compared, differences, err, reason := compareAtStableSnapshot(t.Context(), pair,
		rpc.NewRequest("eth_getBalance", []any{"0x0", "latest"}), []string{"latest"}, 1, 0)
	if err != nil || reason != "" || compared == nil || differences == nil || len(differences.Differences) != 0 {
		t.Fatalf("comparison = %+v, %+v, %v, %q", compared, differences, err, reason)
	}
}

func TestDifferentHeadsAreInconclusive(t *testing.T) {
	baseline := snapshotServer(t, "0x"+strings.Repeat("11", 32))
	defer baseline.Close()
	target := snapshotServer(t, "0x"+strings.Repeat("22", 32))
	defer target.Close()
	pair := rpc.NewClientPair(baseline.URL, "baseline", target.URL, "target", time.Second)
	_, _, _, reason := compareAtStableSnapshot(t.Context(), pair,
		rpc.NewRequest("eth_getBalance", []any{"0x0", "latest"}), []string{"latest"}, 2, 0)
	if reason == "" {
		t.Fatal("different chain heads were compared")
	}
}

func TestInvalidBlockHashIsInconclusive(t *testing.T) {
	invalidHash := "0x" + strings.Repeat("zz", 32)
	baseline := snapshotServer(t, invalidHash)
	defer baseline.Close()
	target := snapshotServer(t, invalidHash)
	defer target.Close()
	pair := rpc.NewClientPair(baseline.URL, "baseline", target.URL, "target", time.Second)
	_, _, _, reason := compareAtStableSnapshot(t.Context(), pair,
		rpc.NewRequest("eth_getBalance", []any{"0x0", "latest"}), []string{"latest"}, 1, 0)
	if reason == "" {
		t.Fatal("invalid block hash established a snapshot")
	}
}

func TestTransactionFeeHistoryRequiresSharedLatestBlock(t *testing.T) {
	baseline := snapshotServer(t, "0x"+strings.Repeat("11", 32))
	defer baseline.Close()
	target := snapshotServer(t, "0x"+strings.Repeat("22", 32))
	defer target.Close()
	pair := rpc.NewClientPair(baseline.URL, "baseline", target.URL, "target", time.Second)
	reporter := report.NewReporter(report.EndpointMetadata{}, report.EndpointMetadata{}, false)
	compareFeeHistory(t.Context(), pair, reporter)
	if got := reporter.Generate().Results[0]; got.Status != report.StatusInconclusive || !reporter.HasFailures() {
		t.Fatalf("fee history across different heads = %+v", got)
	}
}

func TestChangedSnapshotRetainsComparedResponses(t *testing.T) {
	var blockReads atomic.Int32
	serve := func() *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var request rpc.Request
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Errorf("decode request: %v", err)
				return
			}
			var result any = "0x1"
			if request.Method == "eth_getBlockByNumber" {
				hash := "0x" + strings.Repeat("11", 32)
				if blockReads.Add(1) > 2 {
					hash = "0x" + strings.Repeat("22", 32)
				}
				result = map[string]any{"hash": hash}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": result})
		}))
	}
	baseline := serve()
	defer baseline.Close()
	target := serve()
	defer target.Close()
	pair := rpc.NewClientPair(baseline.URL, "baseline", target.URL, "target", time.Second)
	compared, _, _, reason := compareAtStableSnapshot(t.Context(), pair,
		rpc.NewRequest("eth_getBalance", []any{"0x0", "latest"}), []string{"latest"}, 1, 0)
	if reason == "" || compared == nil || compared.BaselineResponse == nil || compared.TargetResponse == nil {
		t.Fatalf("snapshot change lost responses: comparison=%+v reason=%q", compared, reason)
	}
}
