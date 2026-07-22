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

// TestL1Glamsterdam_Derivation_RealCLBeacon is the L1 -> L2 derivation coverage that can only run
// against a REAL consensus layer.
//
// It lives in this package, apart from the rest of the derivation cases, because of what it needs
// rather than what it tests. The sysgo derivation cases each stand up their own system with a
// case-specific orchestrator config (blob DA for derivblob, time travel for channeltimeout, no
// discovery for l1reorg), and Go allows exactly one TestMain per package, so they structurally
// cannot share one. The cases below need the opposite: a real-CL sysext devnet, which is precisely
// what this package's TestMain already gates on. Sharing it also means they inherit its
// DEVNET_EXPECT_PRECONDITIONS_MET guarantee, so a run with no devnet fails instead of skipping.
//
// Keeping them under their own umbrella rather than folding them into
// TestL1Glamsterdam_System_RealCL keeps derivation and system coverage separable with -run.
func TestL1Glamsterdam_Derivation_RealCLBeacon(gt *testing.T) {
	gt.Run("TestL1Beacon_ConfigSpec_PostGlamsterdam", runL1BeaconConfigSpec)
	gt.Run("TestL1Beacon_BlobsFetch_PostGlamsterdam", runL1BeaconBlobsFetch)
}
