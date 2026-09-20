package system

import "testing"

// TestL1Glamsterdam_System_RealCL holds the §5 cases a PR runner cannot host. Each is here
// for its own reason, and "needs a real CL" is not one of them:
//
//   - BatcherSubmission takes the batcher's DA mode from the devnet descriptor instead of
//     pinning it, which is exactly where its value comes from -- pinned under sysgo it would
//     only restate what elysium/da/blob and elysium/da/calldata already assert.
//   - LongRun is a 30-minute soak.
//   - HighTPS drives a ~1000-tx load.
//
// The rest of §5 -- smoke, mid-flight, proposer, verifier convergence, restart recovery --
// referenced no CL at all and was held out of the gate only by this package's compat
// restriction. Each now lives in its own sysgo package beside this file and runs per PR.
func TestL1Glamsterdam_System_RealCL(gt *testing.T) {
	gt.Run("TestL1Glamsterdam_BatcherSubmissionE2E", runBatcherSubmission)
	gt.Run("TestL1Glamsterdam_LongRun_30min", runSystemLongRunAcrossUpgrade)
	gt.Run("TestL1Glamsterdam_HighTPS", runL1GlamsterdamHighTPS)
}

// TestL1Glamsterdam_Derivation_RealCLBeacon groups derivation cases that need
// the real-CL devnet gate from this package's TestMain, while keeping them
// selectable separately from the broader system suite.
//
// These two do genuinely need a real CL: they assert against the beacon API itself, and
// fakebeacon serves a single config key and synthesises its blobs, so running them under
// sysgo would assert nothing about a compliant Glamsterdam beacon.
func TestL1Glamsterdam_Derivation_RealCLBeacon(gt *testing.T) {
	gt.Run("TestL1Beacon_ConfigSpec_PostGlamsterdam", runL1BeaconConfigSpec)
	gt.Run("TestL1Beacon_BlobsFetch_PostGlamsterdam", runL1BeaconBlobsFetch)
}
