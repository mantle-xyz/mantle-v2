package system

import "testing"

func TestL1Glamsterdam_System_RealCL(gt *testing.T) {
	// Mid-flight must run first: it is the only case that needs the L1 to
	// still be pre-Glamsterdam when the test body starts.
	gt.Run("TestL1UpgradeMidFlight", runL1UpgradeMidFlight)

	gt.Run("TestL1Glamsterdam_L2Arsia_Smoke", runL1GlamsterdamL2ArsiaSmoke)
	gt.Run("TestL1Glamsterdam_BatcherE2E", runBatcherBlobSubmission)
	gt.Run("TestL1Glamsterdam_ProposerE2E", runProposerOutputRootSubmission)
	gt.Run("TestL1Glamsterdam_VerifierConverge", runL1GlamsterdamVerifierConverge)
	gt.Run("TestL1Glamsterdam_AfterRestart", runL1GlamsterdamAfterRestart)
	gt.Run("TestL1Glamsterdam_LongRun_30min", runSystemLongRunAcrossUpgrade)
	gt.Run("TestL1Glamsterdam_HighTPS", runL1GlamsterdamHighTPS)
}

// TestL1Glamsterdam_Derivation_RealCLBeacon is the §1 (L1 -> L2 derivation) coverage that can only
// run against a REAL consensus layer: cases 1.9, 1.10 and 1.11.
//
// They live in this package, not with the rest of §1, because of what they need rather than what
// they test. §1.1-1.8 each stand up their own sysgo system with a case-specific orchestrator
// config (blob DA for derivblob, time travel for channeltimeout, no discovery for l1reorg), and
// Go allows exactly one TestMain per package — so those cases structurally cannot share one. The
// three below need the opposite: a real-CL sysext devnet, which is precisely the system this
// package's TestMain already gates on. Sharing it also means they inherit its
// DEVNET_EXPECT_PRECONDITIONS_MET guarantee, so a run with no devnet fails instead of skipping.
//
// A separate umbrella rather than more subtests of TestL1Glamsterdam_System_RealCL keeps the
// design doc's §1/§5 split intact and lets either module be run on its own with -run.
func TestL1Glamsterdam_Derivation_RealCLBeacon(gt *testing.T) {
	gt.Run("TestL1Beacon_ConfigSpec_PostGlamsterdam", runL1BeaconConfigSpec)
	gt.Run("TestL1Beacon_BlobsFetch_PostGlamsterdam", runL1BeaconBlobsFetch)
}
