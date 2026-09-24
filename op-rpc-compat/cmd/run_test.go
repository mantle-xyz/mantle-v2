package cmd

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
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

func TestDefaultKnownDiffsLoadOutsideRepository(t *testing.T) {
	t.Chdir(t.TempDir())
	r := report.NewReporter(report.EndpointMetadata{Name: "op-geth", URL: "baseline"}, report.EndpointMetadata{Name: "op-reth", URL: "target"}, false)
	if err := loadKnownDiffsFS(r, testcaseFS("")); err != nil {
		t.Fatal(err)
	}
	if r.GetKnownDiff("eth_hashrate") == nil {
		t.Fatal("embedded known differences missing eth_hashrate")
	}
}

func TestCustomKnownDiffsUseSelectedDirectory(t *testing.T) {
	dir := t.TempDir()
	data := []byte(`{"known_diffs":[{"test_name":"custom","reason":"custom corpus"}]}`)
	if err := os.WriteFile(filepath.Join(dir, knownDiffsFileName), data, 0o600); err != nil {
		t.Fatal(err)
	}
	r := report.NewReporter(report.EndpointMetadata{Name: "baseline", URL: "baseline"}, report.EndpointMetadata{Name: "target", URL: "target"}, false)
	if err := loadKnownDiffsFS(r, testcaseFS(dir)); err != nil {
		t.Fatal(err)
	}
	if got := r.GetKnownDiff("custom"); got == nil || got.Reason != "custom corpus" {
		t.Fatalf("known difference = %+v", got)
	}
}

func TestMalformedKnownDiffsAreRejected(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, knownDiffsFileName), []byte(`{"known_diffs":`), 0o600); err != nil {
		t.Fatal(err)
	}
	r := report.NewReporter(report.EndpointMetadata{}, report.EndpointMetadata{}, false)
	if _, err := loadSelectedKnownDiffs(r, dir); err == nil {
		t.Fatal("malformed known differences must stop the run")
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

func TestIncludePreconfTransactions(t *testing.T) {
	if !includePreconfTransactions(false) {
		t.Fatal("default transaction mode must include preconf scenarios")
	}
	if includePreconfTransactions(true) {
		t.Fatal("standard-only transaction mode must exclude preconf scenarios")
	}
}
