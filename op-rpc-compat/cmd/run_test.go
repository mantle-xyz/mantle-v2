package cmd

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ethereum-optimism/optimism/op-rpc-compat/pkg/report"
	"github.com/ethereum-optimism/optimism/op-rpc-compat/pkg/tx"
	"github.com/ethereum-optimism/optimism/op-rpc-compat/testcases"
)

func TestTxStandardOnlyRequiresTx(t *testing.T) {
	oldTxMode, oldStandardOnly := txTest, txStandardOnly
	t.Cleanup(func() {
		txTest, txStandardOnly = oldTxMode, oldStandardOnly
	})
	txTest, txStandardOnly = false, true
	if err := runTests(); err == nil || !strings.Contains(err.Error(), "--tx") {
		t.Fatalf("standalone --tx-standard-only error = %v", err)
	}
}

func TestDefaultCorpusDoesNotDependOnWorkingDirectory(t *testing.T) {
	t.Chdir(t.TempDir())
	files, err := findTestFiles("", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(files, "eth_basic.json") {
		t.Fatalf("default corpus missing eth_basic.json: %v", files)
	}
	if slices.Contains(files, knownDiffsFileName) {
		t.Fatal("known_diffs.json must not run as a testcase")
	}
}

func TestExplicitTestcaseDirectoryUsesOSFiles(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "custom.json"), []byte("[]"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, knownDiffsFileName), []byte(`{"known_diffs":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	files, err := findTestFiles(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(files, []string{filepath.Join(dir, "custom.json")}) {
		t.Fatalf("files = %v", files)
	}
}

func TestExplicitTestFileUsesOSPath(t *testing.T) {
	file := filepath.Join(t.TempDir(), "custom.json")
	if err := os.WriteFile(file, []byte(`[{"name":"custom","method":"eth_chainId","params":[]}]`), 0o600); err != nil {
		t.Fatal(err)
	}
	tests, err := loadTestFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if len(tests) != 1 || tests[0].Name != "custom" {
		t.Fatalf("tests = %+v", tests)
	}
}

func TestExternalCorpusCannotClaimEmbeddedIdentity(t *testing.T) {
	file := filepath.Join(t.TempDir(), "custom.json")
	if err := os.WriteFile(file, []byte(`[{"name":"reviewed-case","method":"eth_chainId","params":[],"corpus_id":"embedded"}]`), 0o600); err != nil {
		t.Fatal(err)
	}
	cases, err := loadTestFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if len(cases) != 1 || !strings.HasPrefix(cases[0].CorpusID, "sha256:") {
		t.Fatalf("external corpus identity = %+v", cases)
	}
}

func TestEmbeddedTestcaseLoadsOutsideRepository(t *testing.T) {
	t.Chdir(t.TempDir())
	tests, err := loadTestFileFS(testcases.FS, "eth_basic.json")
	if err != nil {
		t.Fatal(err)
	}
	if len(tests) == 0 {
		t.Fatal("embedded eth_basic.json contains no testcases")
	}
}

func TestTransactionFailuresAppearInReport(t *testing.T) {
	r := report.NewReporter(report.EndpointMetadata{}, report.EndpointMetadata{}, false)
	failed := recordTransactionResults(r, []*tx.TxTestResult{
		{TestName: "rejection", TxType: "rejection", Error: "both endpoints accepted an invalid transaction"},
		{TestName: "transfer", TxType: "Legacy", Passed: true},
	})
	generated := r.Generate()
	if failed != 1 || !r.HasFailures() || generated.FailedTests != 1 || generated.PassedTests != 1 {
		t.Fatalf("failed = %d, report = %+v", failed, generated)
	}
	if got := generated.Results[0]; got.TestCase.Name != "rejection" || got.CompareError == "" || got.Status != report.StatusFail {
		t.Fatalf("rejection result missing from JSON report: %+v", got)
	}
}

func TestInconclusiveTransactionDoesNotAddFailure(t *testing.T) {
	r := report.NewReporter(report.EndpointMetadata{}, report.EndpointMetadata{}, false)
	r.AddInconclusiveResult(report.TestCase{Name: "native_initial_balance", Method: "eth_getBalance"}, nil, "heads differ")
	failed := recordTransactionResults(r, []*tx.TxTestResult{{TestName: "native", TxType: "Legacy",
		Inconclusive: true, Error: "heads differ"}})
	generated := r.Generate()
	if failed != 0 || generated.InconclusiveTests != 1 || generated.FailedTests != 0 || !r.HasFailures() {
		t.Fatalf("inconclusive transaction summary = failed %d, report %+v", failed, generated)
	}
}

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

func TestTemplateSubstitutionPreservesProvenanceAndLargeNumbers(t *testing.T) {
	tests := []report.TestCase{{
		Name: "template", Method: "eth_call", CorpusID: "embedded",
		RequestTemplateSHA256: "original-template-digest",
		Params:                []any{json.Number("9007199254740993"), "{{latest_block_number}}"},
	}}
	resolved := replaceTemplateVars(tests, &TemplateVars{LatestBlockNumber: "0x190"})
	params := resolved[0].Params.([]any)
	if resolved[0].CorpusID != "embedded" || resolved[0].RequestTemplateSHA256 != "original-template-digest" ||
		params[0] != json.Number("9007199254740993") || params[1] != "0x190" || resolved[0].TemplateError != "" {
		t.Fatalf("template result = %+v", resolved[0])
	}
	if tags := caseDynamicTags(resolved[0]); len(tags) != 1 || tags[0] != "latest" {
		t.Fatalf("template snapshot provenance = %v", tags)
	}
	unresolved := replaceTemplateVars(tests, &TemplateVars{})
	if unresolved[0].TemplateError == "" {
		t.Fatal("missing template variable was not recorded")
	}
}

func TestUnresolvedTemplateDoesNotSendTestRPC(t *testing.T) {
	if os.Getenv("RPC_TEMPLATE_CHILD") == "1" {
		baselineURL = os.Getenv("RPC_TEMPLATE_URL")
		targetURL = baselineURL
		testFile = os.Getenv("RPC_TEMPLATE_FILE")
		outputFile = os.Getenv("RPC_TEMPLATE_REPORT")
		_ = runTests()
		return
	}
	var testCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			ID     int    `json:"id"`
			Method string `json:"method"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		response := map[string]any{"jsonrpc": "2.0", "id": request.ID}
		switch request.Method {
		case "eth_chainId":
			response["result"] = "0x539"
		case "eth_getBlockByNumber":
			response["result"] = map[string]any{"hash": "0x" + strings.Repeat("11", 32), "number": "0x1", "transactions": []any{}}
		case "web3_clientVersion":
			response["result"] = "client/v1"
		case "debug_getRawBlock":
			response["error"] = map[string]any{"code": -32601, "message": "method not found"}
		case "eth_getTransactionByHash":
			testCalls.Add(1)
			response["result"] = nil
		default:
			t.Errorf("unexpected method %q", request.Method)
			response["error"] = map[string]any{"code": -32601, "message": "method not found"}
		}
		_ = json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()
	file := filepath.Join(t.TempDir(), "case.json")
	if err := os.WriteFile(file, []byte(`[{"name":"templated","method":"eth_getTransactionByHash","params":["{{latest_tx_hash}}"]}]`), 0o600); err != nil {
		t.Fatal(err)
	}
	reportPath := filepath.Join(t.TempDir(), "report.json")
	command := exec.Command(os.Args[0], "-test.run=^TestUnresolvedTemplateDoesNotSendTestRPC$")
	command.Env = append(os.Environ(), "RPC_TEMPLATE_CHILD=1", "RPC_TEMPLATE_URL="+server.URL,
		"RPC_TEMPLATE_FILE="+file, "RPC_TEMPLATE_REPORT="+reportPath)
	if err := command.Run(); err == nil {
		t.Fatal("unresolved template exited successfully")
	}
	data, err := os.ReadFile(reportPath)
	if err != nil {
		t.Fatal(err)
	}
	var saved struct {
		Results []struct {
			Status     string `json:"status"`
			SkipReason string `json:"skip_reason"`
		} `json:"results"`
	}
	if err := json.Unmarshal(data, &saved); err != nil {
		t.Fatal(err)
	}
	if testCalls.Load() != 0 || saved.Results[0].Status != "INCONCLUSIVE" ||
		!strings.Contains(saved.Results[0].SkipReason, "latest_tx_hash") {
		t.Fatalf("unresolved template: calls=%d report=%s", testCalls.Load(), data)
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
