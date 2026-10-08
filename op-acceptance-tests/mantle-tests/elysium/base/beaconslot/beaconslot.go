// Package beaconslot holds the beacon slot-timing rule used by the real-CL cases in the
// elysium/system suite.
//
// It is separate so the slot-spacing rule remains unit-testable without a real
// devnet; the system suite itself requires DEVNET_EXPECT_PRECONDITIONS_MET.
package beaconslot

import "fmt"

// SpacingError checks that timestamp gaps are positive multiples of
// SECONDS_PER_SLOT, with at least one exact one-slot gap. Missed slots are valid;
// a wrong divisor such as 1s for 12s blocks is not.
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
