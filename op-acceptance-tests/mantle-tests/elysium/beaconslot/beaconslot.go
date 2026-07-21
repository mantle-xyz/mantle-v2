// Package beaconslot holds the beacon slot-timing rule used by the real-CL cases in the
// elysium/system suite.
//
// It is a separate package on purpose. The system suite is gated to a real devnet and force-sets
// DEVNET_EXPECT_PRECONDITIONS_MET, and the orchestrator is initialised before any test runs, so a
// package-local unit test there could never execute without a devnet. Keeping the rule here means
// it stays verifiable everywhere, which matters precisely because the case that applies it cannot
// run in CI.
package beaconslot

import "fmt"

// SpacingError reports why a run of consecutive L1 block timestamps is inconsistent with the
// beacon's SECONDS_PER_SLOT, or nil if it is consistent. This is the mapping op-node depends on to
// turn an L1 block timestamp into a beacon slot when it fetches blobs.
//
// Post-merge every L1 block sits on a slot, so each timestamp gap is a whole number of slots:
// exactly SECONDS_PER_SLOT when no slot was missed, a larger multiple when one was. Comparing a
// single adjacent pair would therefore go red on any missed slot, which is normal devnet behaviour
// and not a finding. So the rule has two halves: every gap must be a positive whole multiple of
// SECONDS_PER_SLOT, AND the tightest gap must equal it exactly.
//
// The second half is what keeps this honest. A spec value that merely DIVIDES the real block time
// (say 1 against 12s blocks) satisfies divisibility at every gap, yet never equals one — with only
// the first half the check would degenerate into something a wrong value passes.
func SpacingError(times []uint64, secondsPerSlot uint64) error {
	if secondsPerSlot == 0 {
		return fmt.Errorf("SECONDS_PER_SLOT must be non-zero")
	}
	if len(times) < 2 {
		return fmt.Errorf("need at least 2 block timestamps to measure spacing, got %d", len(times))
	}
	tightest := ^uint64(0)
	for i := 1; i < len(times); i++ {
		if times[i] <= times[i-1] {
			return fmt.Errorf("block timestamps must strictly increase, saw %d then %d", times[i-1], times[i])
		}
		gap := times[i] - times[i-1]
		if gap%secondsPerSlot != 0 {
			return fmt.Errorf("a %ds gap is not a whole number of %ds slots", gap, secondsPerSlot)
		}
		if gap < tightest {
			tightest = gap
		}
	}
	if tightest != secondsPerSlot {
		return fmt.Errorf("the tightest gap is %ds, which does not equal SECONDS_PER_SLOT=%ds", tightest, secondsPerSlot)
	}
	return nil
}
