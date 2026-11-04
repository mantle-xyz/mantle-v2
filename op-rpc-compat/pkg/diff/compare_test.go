package diff

import (
	"encoding/json"
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

func TestDifferencePointerAndPresence(t *testing.T) {
	tests := []struct {
		name, baseline, target, pointer string
		baselinePresent, targetPresent  bool
	}{
		{"explicit null", `{"result":{"a/b~c":null}}`, `{"result":{"a/b~c":"x"}}`, "/result/a~1b~0c", true, true},
		{"missing field", `{"result":{"a/b~c":"x"}}`, `{"result":{}}`, "/result/a~1b~0c", true, false},
		{"extra field", `{"result":{}}`, `{"result":{"a/b~c":"x"}}`, "/result/a~1b~0c", false, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result, err := Compare([]byte(tc.baseline), []byte(tc.target), DefaultOptions())
			if err != nil {
				t.Fatal(err)
			}
			data, err := json.Marshal(result.Differences[0])
			if err != nil {
				t.Fatal(err)
			}
			var difference struct {
				Pointer         string `json:"pointer"`
				ExpectedPresent bool   `json:"expected_present"`
				ActualPresent   bool   `json:"actual_present"`
			}
			if err := json.Unmarshal(data, &difference); err != nil {
				t.Fatal(err)
			}
			if difference.Pointer != tc.pointer || difference.ExpectedPresent != tc.baselinePresent || difference.ActualPresent != tc.targetPresent {
				t.Fatalf("difference = %s", data)
			}
		})
	}
}

func TestStrictValueDifferences(t *testing.T) {
	tests := []struct {
		name, baseline, target string
		wantType               DiffType
	}{
		{"null to object", `{"result":{"field":null}}`, `{"result":{"field":{}}}`, DiffType("null")},
		{"object to null", `{"result":{"field":{}}}`, `{"result":{"field":null}}`, DiffType("null")},
		{"missing field", `{"result":{"field":1}}`, `{"result":{}}`, DiffTypeMissing},
		{"extra field", `{"result":{}}`, `{"result":{"field":1}}`, DiffTypeExtra},
		{"array order", `{"result":[1,2]}`, `{"result":[2,1]}`, DiffTypeValue},
		{"hex width", `{"result":"0x0001"}`, `{"result":"0x01"}`, DiffTypeValue},
		{"large JSON integer", `{"result":9007199254740992}`, `{"result":9007199254740993}`, DiffTypeValue},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Compare([]byte(tc.baseline), []byte(tc.target), DefaultOptions())
			if err != nil {
				t.Fatal(err)
			}
			if got.FailCount == 0 || got.IsEqual || len(got.Differences) == 0 || got.Differences[0].Type != tc.wantType {
				t.Fatalf("comparison = %+v; want failing %s difference", got, tc.wantType)
			}
		})
	}
}

func TestEquivalentJSONNumbersMatchWithoutRounding(t *testing.T) {
	for _, tc := range []struct{ baseline, target string }{
		{`{"result":0}`, `{"result":0.0}`},
		{`{"result":1e0}`, `{"result":1.0}`},
		{`{"result":9007199254740993}`, `{"result":9007199254740993.0}`},
	} {
		result, err := Compare([]byte(tc.baseline), []byte(tc.target), DefaultOptions())
		if err != nil {
			t.Fatal(err)
		}
		if !result.IsEqual || len(result.Differences) != 0 {
			t.Fatalf("equivalent numeric values differ: %s vs %s: %+v", tc.baseline, tc.target, result)
		}
	}
}
