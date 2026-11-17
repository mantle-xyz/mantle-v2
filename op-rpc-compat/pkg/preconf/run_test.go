package preconf

import (
	"strings"
	"testing"
)

func TestInconclusiveScenarioIsNotCountedAsPass(t *testing.T) {
	runner := &Runner{}
	runner.record("successful", true, "landed on chain")
	runner.recordInconclusive("deposit-ordering", "deposit had no co-located user transaction")
	runner.record("failed", false, "receipt mismatch")
	passed, failed, inconclusive := runner.resultCounts()
	if passed != 1 || failed != 1 || inconclusive != 1 {
		t.Fatalf("result counts = %d passed, %d failed, %d inconclusive", passed, failed, inconclusive)
	}
}

func TestInvalidOnlyScenarioFailsBeforeSetup(t *testing.T) {
	for _, tc := range []struct {
		name  string
		only  string
		heavy bool
	}{
		{name: "unknown", only: "not_a_scenario"},
		{name: "heavy_without_flag", only: "deposit_ordered_before_user"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runner := &Runner{cfg: Config{Only: tc.only, Heavy: tc.heavy}}
			err := runner.Run(t.Context())
			if err == nil || !strings.Contains(err.Error(), tc.only) {
				t.Fatalf("Run error = %v, want scenario validation error", err)
			}
		})
	}
}
