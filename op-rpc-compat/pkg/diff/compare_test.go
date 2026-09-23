package diff

import (
	"strings"
	"testing"
)

func TestDifferenceTextUsesBaselineAndTarget(t *testing.T) {
	result, err := Compare([]byte(`{"value":null}`), []byte(`{"value":"new"}`), DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Differences) == 0 {
		t.Fatal("expected a difference")
	}
	for _, difference := range result.Differences {
		if strings.Contains(difference.Message, "geth") || strings.Contains(difference.Message, "reth") {
			t.Errorf("fixed client name in difference: %q", difference.Message)
		}
	}
	formatted := FormatDifferences([]Difference{{Type: DiffTypeValue, Path: ".value", Expected: "old", Actual: "new"}})
	if !strings.Contains(formatted, "baseline: old") || !strings.Contains(formatted, "target: new") {
		t.Fatalf("difference output = %q", formatted)
	}
}
