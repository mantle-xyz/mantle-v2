package preconf

import "testing"

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
