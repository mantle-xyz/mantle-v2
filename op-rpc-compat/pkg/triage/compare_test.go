package triage

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/ethereum-optimism/optimism/op-rpc-compat/pkg/diff"
	"github.com/ethereum-optimism/optimism/op-rpc-compat/pkg/report"
)

func triageReport(buildID string, results ...report.TestResult) *report.Report {
	return &report.Report{
		SchemaVersion: 4,
		Baseline:      report.EndpointMetadata{ClientFamily: "Geth", ChainID: "0x539", GenesisHash: "0xgenesis"},
		Target: report.EndpointMetadata{ClientFamily: "mantle-reth", BuildID: buildID,
			ChainID: "0x539", GenesisHash: "0xgenesis"},
		Results: results,
	}
}

func triageResult(name, pointer string, actual any) report.TestResult {
	return report.TestResult{
		TestCase: report.TestCase{Name: name, Method: "eth_call", Params: []any{"0x1"}},
		CorpusID: "embedded", ObservedStatus: report.StatusFail, Status: report.StatusFail,
		Differences: []diff.Difference{{Pointer: pointer, Type: diff.DiffTypeValue,
			Severity: diff.SeverityFail, ExpectedPresent: true, ActualPresent: true,
			Expected: "0x1", Actual: actual}},
	}
}

func TestCompareReportsFindsOnlyNewChangedAndResolvedRawDifferences(t *testing.T) {
	previous := triageReport("git:old", triageResult("unchanged", "/result/a", "0x2"),
		triageResult("changed", "/result/b", "0x2"), triageResult("resolved", "/result/c", "0x2"))
	current := triageReport("git:new", triageResult("unchanged", "/result/a", "0x2"),
		triageResult("changed", "/result/b", "0x3"), triageResult("new", "/result/d", "0x4"),
		report.TestResult{TestCase: report.TestCase{Name: "resolved", Method: "eth_call", Params: []any{"0x1"}},
			CorpusID: "embedded", Status: report.StatusPass})
	summary, err := Compare(previous, current)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Unchanged != 1 || len(summary.New) != 1 || len(summary.Changed) != 1 || len(summary.Resolved) != 1 ||
		summary.New[0].CaseID != "new" || summary.Changed[0].Current.CaseID != "changed" ||
		summary.Resolved[0].CaseID != "resolved" {
		t.Fatalf("triage summary = %+v", summary)
	}
	if summary.New[0].Fingerprint == "" || summary.New[0].Fingerprint == summary.Resolved[0].Fingerprint {
		t.Fatalf("invalid fingerprints: %+v", summary)
	}
}

func TestCompareReportsPreservesDuplicateCasesAndLargeNumbers(t *testing.T) {
	large := json.Number("9007199254740993")
	previous := triageReport("git:old", triageResult("duplicate", "/result", large),
		triageResult("duplicate", "/result", large))
	current := triageReport("git:new", triageResult("duplicate", "/result", large))
	summary, err := Compare(previous, current)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Unchanged != 1 || len(summary.Resolved) != 1 || len(summary.New) != 0 {
		t.Fatalf("duplicate case summary = %+v", summary)
	}
	data, err := json.Marshal(summary.Resolved[0])
	if err != nil || !strings.Contains(string(data), large.String()) {
		t.Fatalf("large number lost: %s, %v", data, err)
	}
}

func TestCompareReportsRejectsDifferentChain(t *testing.T) {
	previous := triageReport("git:old")
	current := triageReport("git:new")
	current.Target.GenesisHash = "0xother"
	if _, err := Compare(previous, current); err == nil {
		t.Fatal("different genesis was compared as one run")
	}
}

func TestCompareReportsRejectsUnknownFamilies(t *testing.T) {
	previous := triageReport("git:old")
	current := triageReport("git:new")
	previous.Target.ClientFamily, current.Target.ClientFamily = "", ""
	if _, err := Compare(previous, current); err == nil {
		t.Fatal("two unknown client families were compared")
	}
}

func TestCompareReportsRejectsSwappedSameFamilyBuilds(t *testing.T) {
	previous := triageReport("git:new", triageResult("same", "/result", "0x2"))
	current := triageReport("git:old", triageResult("same", "/result", "0x2"))
	previous.Baseline.ClientFamily, current.Baseline.ClientFamily = "mantle-reth", "mantle-reth"
	previous.Baseline.BuildID, current.Baseline.BuildID = "git:old", "git:new"
	previous.Baseline.URL, current.Baseline.URL = "http://old", "http://old"
	previous.Target.URL, current.Target.URL = "http://new", "http://new"
	if _, err := Compare(previous, current); err == nil {
		t.Fatal("swapped same-family builds were compared as the same direction")
	}
	current.Target.BuildID = "git:third"
	if _, err := Compare(previous, current); err == nil {
		t.Fatal("prior target build moved to baseline was compared as the same direction")
	}
	current.Baseline.BuildID, current.Target.BuildID = "git:third", "git:fourth"
	if _, err := Compare(previous, current); err == nil {
		t.Fatal("both builds changed without an anchor but URLs hid the ambiguity")
	}
	previous.Baseline.BuildID, current.Baseline.BuildID = "", ""
	previous.Target.BuildID, current.Target.BuildID = "", ""
	current.Baseline.URL, current.Target.URL = previous.Target.URL, previous.Baseline.URL
	if _, err := Compare(previous, current); err == nil {
		t.Fatal("swapped same-family endpoints with unknown builds were compared")
	}
}

func TestCompareReportsAllowsLegacySameFamilyURLDirection(t *testing.T) {
	previous := triageReport("", triageResult("same", "/result", "0x2"))
	current := triageReport("git:new", triageResult("same", "/result", "0x2"))
	previous.Baseline.ClientFamily, current.Baseline.ClientFamily = "mantle-reth", "mantle-reth"
	current.Baseline.BuildID = "git:old"
	previous.Baseline.URL, current.Baseline.URL = "http://old", "http://old"
	previous.Target.URL, current.Target.URL = "http://new", "http://new"
	if _, err := Compare(previous, current); err != nil {
		t.Fatalf("stable legacy endpoint direction was rejected: %v", err)
	}
}

func TestCompareReportsDoesNotCallUnexecutedCaseResolved(t *testing.T) {
	previous := triageReport("git:old", triageResult("pending", "/result", "0x2"))
	current := triageReport("git:new", triageResult("other", "/result", "0x3"))
	previous.SelectedSuite, current.SelectedSuite = "core", "core"
	current.ExcludedCases = []report.ExcludedCase{{Name: "pending", Method: "eth_call", CorpusID: "embedded", Reason: "diagnostic"}}
	summary, err := Compare(previous, current)
	if err != nil {
		t.Fatal(err)
	}
	if len(summary.Resolved) != 0 || len(summary.NotRun) != 1 || summary.NotRun[0].CaseID != "pending" {
		t.Fatalf("unexecuted case summary = %+v", summary)
	}
	current.SelectedSuite = "diagnostic"
	if _, err := Compare(previous, current); err == nil {
		t.Fatal("different suite selections were compared")
	}
}

func TestCompareReportsDoesNotCallTransportFailureResolved(t *testing.T) {
	previous := triageReport("git:old", triageResult("receipt", "/result", "0x2"))
	current := triageReport("git:new", report.TestResult{
		TestCase: report.TestCase{Name: "receipt", Method: "eth_call", Params: []any{"0x1"}},
		CorpusID: "embedded", Status: report.StatusFail, BaselineError: "timeout",
	})
	summary, err := Compare(previous, current)
	if err != nil {
		t.Fatal(err)
	}
	if len(summary.Resolved) != 0 || len(summary.NotRun) != 0 ||
		len(summary.Uncompared) != 1 || summary.Uncompared[0].CaseID != "receipt" {
		t.Fatalf("transport failure summary = %+v", summary)
	}
}

func TestFingerprintIncludesDirectedClientFamilies(t *testing.T) {
	first := triageReport("git:old", triageResult("same", "/result", "0x2"))
	second := triageReport("git:new", triageResult("same", "/result", "0x2"))
	second.Baseline.ClientFamily = "other-client"
	left, err := collect(first, families(first))
	if err != nil {
		t.Fatal(err)
	}
	right, err := collect(second, families(second))
	if err != nil {
		t.Fatal(err)
	}
	for _, findings := range left {
		for _, other := range right {
			if findings[0].Fingerprint == other[0].Fingerprint {
				t.Fatal("different client families produced the same fingerprint")
			}
		}
	}
}

func TestFingerprintKeepsExactBuildAnchor(t *testing.T) {
	first := triageReport("git:new", triageResult("same", "/result", "0x2"))
	second := triageReport("git:new", triageResult("same", "/result", "0x2"))
	first.Results[0].Differences[0].ScopeBaseline = "exact_build:git:11111111"
	second.Results[0].Differences[0].ScopeBaseline = "exact_build:git:22222222"
	first.Results[0].Differences[0].ScopeTarget = "family:mantle-reth"
	second.Results[0].Differences[0].ScopeTarget = "family:mantle-reth"
	left, err := collect(first, families(first))
	if err != nil {
		t.Fatal(err)
	}
	right, err := collect(second, families(second))
	if err != nil {
		t.Fatal(err)
	}
	for _, findings := range left {
		for _, other := range right {
			if findings[0].Fingerprint == other[0].Fingerprint {
				t.Fatal("different exact-build anchors produced the same fingerprint")
			}
		}
	}
}
