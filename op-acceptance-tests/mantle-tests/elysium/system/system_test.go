package system

import "testing"

func TestL1Glamsterdam_System_RealCL(gt *testing.T) {
	// Mid-flight must run first: it is the only case that needs the L1 to
	// still be pre-Glamsterdam when the test body starts.
	gt.Run("TestL1UpgradeMidFlight", runL1UpgradeMidFlight)

	gt.Run("TestL1Glamsterdam_L2Arsia_Smoke", runL1GlamsterdamL2ArsiaSmoke)
	gt.Run("TestL1Glamsterdam_BatcherSubmissionE2E", runBatcherSubmission)
	gt.Run("TestL1Glamsterdam_ProposerE2E", runProposerOutputRootSubmission)
	gt.Run("TestL1Glamsterdam_VerifierConverge", runL1GlamsterdamVerifierConverge)
	gt.Run("TestL1Glamsterdam_AfterRestart", runL1GlamsterdamAfterRestart)
	gt.Run("TestL1Glamsterdam_LongRun_30min", runSystemLongRunAcrossUpgrade)
	gt.Run("TestL1Glamsterdam_HighTPS", runL1GlamsterdamHighTPS)
}

// TestL1Glamsterdam_Derivation_RealCLBeacon groups derivation cases that need
// the real-CL devnet gate from this package's TestMain, while keeping them
// selectable separately from the broader system suite.
func TestL1Glamsterdam_Derivation_RealCLBeacon(gt *testing.T) {
	gt.Run("TestL1Beacon_ConfigSpec_PostGlamsterdam", runL1BeaconConfigSpec)
	gt.Run("TestL1Beacon_BlobsFetch_PostGlamsterdam", runL1BeaconBlobsFetch)
}
