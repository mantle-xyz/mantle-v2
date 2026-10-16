package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/ethereum-optimism/optimism/op-rpc-compat/pkg/diff"
	"github.com/ethereum-optimism/optimism/op-rpc-compat/pkg/report"
	"github.com/ethereum-optimism/optimism/op-rpc-compat/pkg/triage"
)

func TestRunTriageWritesChangedRawDifferenceAcrossBuilds(t *testing.T) {
	makeReport := func(buildID string, actual json.Number) report.Report {
		return report.Report{SchemaVersion: 4,
			Baseline: report.EndpointMetadata{ClientFamily: "Geth", ChainID: "0x539", GenesisHash: "0xgenesis"},
			Target: report.EndpointMetadata{ClientFamily: "mantle-reth", BuildID: buildID,
				ChainID: "0x539", GenesisHash: "0xgenesis"},
			Results: []report.TestResult{{
				TestCase: report.TestCase{Name: "fee", Method: "eth_gasPrice", Params: []any{}},
				CorpusID: "embedded", ObservedStatus: report.StatusFail, Status: report.StatusWarning,
				Differences: []diff.Difference{{Pointer: "/result", Type: diff.DiffTypeValue,
					Severity: diff.SeverityFail, ExpectedPresent: true, ActualPresent: true,
					Expected: json.Number("9007199254740993"), Actual: actual}},
			}},
		}
	}
	writeReport := func(name string, value report.Report) string {
		t.Helper()
		path := filepath.Join(t.TempDir(), name)
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	previous := writeReport("previous.json", makeReport("git:old", "9007199254740994"))
	current := writeReport("current.json", makeReport("git:new", "9007199254740995"))
	output := filepath.Join(t.TempDir(), "triage.json")
	if err := runTriage(previous, current, output); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(output)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	decoder := json.NewDecoder(file)
	decoder.UseNumber()
	var summary triage.Summary
	if err := decoder.Decode(&summary); err != nil {
		t.Fatal(err)
	}
	if len(summary.Changed) != 1 || len(summary.New) != 0 || len(summary.Resolved) != 0 ||
		summary.Changed[0].Current.Actual.(json.Number).String() != "9007199254740995" {
		t.Fatalf("triage summary = %+v", summary)
	}
}
