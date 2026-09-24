package cmd

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ethereum-optimism/optimism/op-rpc-compat/pkg/rpc"
)

func preflightServer(t *testing.T, chainID, genesisHash, version string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			ID     int    `json:"id"`
			Method string `json:"method"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode request: %v", err)
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		var result any
		switch request.Method {
		case "eth_chainId":
			result = chainID
		case "eth_getBlockByNumber":
			if genesisHash == "" {
				result = map[string]any{}
			} else {
				result = map[string]any{"hash": genesisHash}
			}
		case "web3_clientVersion":
			result = version
		default:
			t.Errorf("unexpected method %q", request.Method)
			http.Error(w, "unexpected method", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": result}); err != nil {
			t.Errorf("encode response: %v", err)
		}
	}))
}

func TestPreflightAcceptsSameChainAndRecordsVersions(t *testing.T) {
	genesis := "0x" + strings.Repeat("11", 32)
	baseline := preflightServer(t, "0x1388", genesis, "op-reth/v2.3.0")
	defer baseline.Close()
	target := preflightServer(t, "0x1388", genesis, "op-reth/v2.4.2")
	defer target.Close()
	pair := rpc.NewClientPair(baseline.URL, "old-reth", target.URL, "new-reth", time.Second)

	baseMeta, targetMeta, err := preflightEndpoints(t.Context(), pair)
	if err != nil {
		t.Fatal(err)
	}
	if baseMeta.Name != "old-reth" || baseMeta.URL != baseline.URL || baseMeta.ClientVersion != "op-reth/v2.3.0" {
		t.Fatalf("baseline metadata = %+v", baseMeta)
	}
	if targetMeta.Name != "new-reth" || targetMeta.URL != target.URL || targetMeta.ClientVersion != "op-reth/v2.4.2" {
		t.Fatalf("target metadata = %+v", targetMeta)
	}
}

func TestPreflightRejectsChainIDMismatch(t *testing.T) {
	genesis := "0x" + strings.Repeat("11", 32)
	baseline := preflightServer(t, "0x1388", genesis, "baseline/v1")
	defer baseline.Close()
	target := preflightServer(t, "0x1389", genesis, "target/v2")
	defer target.Close()
	pair := rpc.NewClientPair(baseline.URL, "baseline-node", target.URL, "target-node", time.Second)

	_, _, err := preflightEndpoints(t.Context(), pair)
	if err == nil || !strings.Contains(err.Error(), "chain ID") || !strings.Contains(err.Error(), "baseline-node") || !strings.Contains(err.Error(), "target-node") {
		t.Fatalf("chain mismatch error = %v", err)
	}
}

func TestPreflightRejectsGenesisMismatch(t *testing.T) {
	baseline := preflightServer(t, "0x1388", "0x"+strings.Repeat("11", 32), "baseline/v1")
	defer baseline.Close()
	target := preflightServer(t, "0x1388", "0x"+strings.Repeat("22", 32), "target/v2")
	defer target.Close()
	pair := rpc.NewClientPair(baseline.URL, "baseline-node", target.URL, "target-node", time.Second)

	_, _, err := preflightEndpoints(t.Context(), pair)
	if err == nil || !strings.Contains(err.Error(), "genesis hash") || !strings.Contains(err.Error(), "baseline-node") || !strings.Contains(err.Error(), "target-node") {
		t.Fatalf("genesis mismatch error = %v", err)
	}
}

func TestPreflightNamesUnreachableEndpoint(t *testing.T) {
	genesis := "0x" + strings.Repeat("11", 32)
	baseline := preflightServer(t, "0x1388", genesis, "baseline/v1")
	defer baseline.Close()
	target := preflightServer(t, "0x1388", genesis, "target/v2")
	url := target.URL
	target.Close()
	pair := rpc.NewClientPair(baseline.URL, "baseline-node", url, "target-node", time.Second)

	_, _, err := preflightEndpoints(t.Context(), pair)
	if err == nil || !strings.Contains(err.Error(), "target-node") || strings.Contains(err.Error(), "task up") {
		t.Fatalf("unreachable endpoint error = %v", err)
	}
}

func TestPreflightRejectsMissingGenesisHash(t *testing.T) {
	baseline := preflightServer(t, "0x1388", "", "baseline/v1")
	defer baseline.Close()
	target := preflightServer(t, "0x1388", "0x"+strings.Repeat("11", 32), "target/v2")
	defer target.Close()
	pair := rpc.NewClientPair(baseline.URL, "baseline-node", target.URL, "target-node", time.Second)
	_, _, err := preflightEndpoints(t.Context(), pair)
	if err == nil || !strings.Contains(err.Error(), "missing genesis hash") || !strings.Contains(err.Error(), "baseline-node") {
		t.Fatalf("missing genesis error = %v", err)
	}
}

func TestPreflightRejectsEmptyClientVersion(t *testing.T) {
	genesis := "0x" + strings.Repeat("11", 32)
	baseline := preflightServer(t, "0x1388", genesis, "")
	defer baseline.Close()
	target := preflightServer(t, "0x1388", genesis, "target/v2")
	defer target.Close()
	pair := rpc.NewClientPair(baseline.URL, "baseline-node", target.URL, "target-node", time.Second)
	_, _, err := preflightEndpoints(t.Context(), pair)
	if err == nil || !strings.Contains(err.Error(), "empty client version") || !strings.Contains(err.Error(), "baseline-node") {
		t.Fatalf("empty version error = %v", err)
	}
}

func TestRunTestsPreflightsBeforeReadingCases(t *testing.T) {
	genesis := "0x" + strings.Repeat("11", 32)
	baseline := preflightServer(t, "0x1388", genesis, "baseline/v1")
	defer baseline.Close()
	target := preflightServer(t, "0x1389", genesis, "target/v2")
	defer target.Close()
	oldBaselineURL, oldTargetURL := baselineURL, targetURL
	oldBaselineName, oldTargetName := baselineName, targetName
	oldFile, oldTxMode, oldOutput := testFile, txTest, outputFile
	t.Cleanup(func() {
		baselineURL, targetURL = oldBaselineURL, oldTargetURL
		baselineName, targetName = oldBaselineName, oldTargetName
		testFile, txTest, outputFile = oldFile, oldTxMode, oldOutput
	})
	baselineURL, targetURL = baseline.URL, target.URL
	baselineName, targetName = "baseline-node", "target-node"
	testFile, txTest, outputFile = filepath.Join(t.TempDir(), "missing.json"), false, ""

	err := runTests()
	if err == nil || !strings.Contains(err.Error(), "chain ID mismatch") {
		t.Fatalf("run error = %v; want preflight mismatch before file loading", err)
	}
}

func TestRunTestsPrintsLogicalEndpointNames(t *testing.T) {
	genesis := "0x" + strings.Repeat("11", 32)
	baseline := preflightServer(t, "0x1388", genesis, "old-reth/v1")
	defer baseline.Close()
	target := preflightServer(t, "0x1388", genesis, "new-reth/v2")
	defer target.Close()
	file := filepath.Join(t.TempDir(), "chain-id.json")
	if err := os.WriteFile(file, []byte(`[{"name":"chain-id","method":"eth_chainId","params":[]}]`), 0o600); err != nil {
		t.Fatal(err)
	}
	oldBaselineURL, oldTargetURL := baselineURL, targetURL
	oldBaselineName, oldTargetName := baselineName, targetName
	oldFile, oldTxMode, oldOutput := testFile, txTest, outputFile
	t.Cleanup(func() {
		baselineURL, targetURL = oldBaselineURL, oldTargetURL
		baselineName, targetName = oldBaselineName, oldTargetName
		testFile, txTest, outputFile = oldFile, oldTxMode, oldOutput
	})
	baselineURL, targetURL = baseline.URL, target.URL
	baselineName, targetName = "old-reth", "new-reth"
	reportFile := filepath.Join(t.TempDir(), "report.json")
	testFile, txTest, outputFile = file, false, reportFile
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
	err = runTests()
	if closeErr := writer.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	os.Stdout = previous
	if err != nil {
		t.Fatal(err)
	}
	output, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(output), "old-reth: ") || !strings.Contains(string(output), "new-reth: ") {
		t.Fatalf("missing logical endpoint labels: %s", output)
	}
	if strings.Contains(string(output), "\ngeth: ") || strings.Contains(string(output), "\nreth: ") {
		t.Fatalf("fixed client labels remain: %s", output)
	}
	data, err := os.ReadFile(reportFile)
	if err != nil {
		t.Fatal(err)
	}
	var saved struct {
		SchemaVersion int `json:"schema_version"`
		Baseline      struct {
			Name          string `json:"name"`
			ClientVersion string `json:"client_version"`
		} `json:"baseline"`
		Target struct {
			Name          string `json:"name"`
			ClientVersion string `json:"client_version"`
		} `json:"target"`
	}
	if err := json.Unmarshal(data, &saved); err != nil {
		t.Fatal(err)
	}
	if saved.SchemaVersion != 2 || saved.Baseline.Name != "old-reth" || saved.Baseline.ClientVersion != "old-reth/v1" ||
		saved.Target.Name != "new-reth" || saved.Target.ClientVersion != "new-reth/v2" {
		t.Fatalf("saved endpoint metadata = %+v", saved)
	}
}
