package beaconslot

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestSpacingRule pins the SECONDS_PER_SLOT-vs-L1-block-spacing rule used by
// TestL1Beacon_ConfigSpec_PostGlamsterdam. That test needs a real geth+beacon and therefore
// cannot run in CI; this one has no environment dependency, so the rule it leans on stays
// verifiable everywhere.
//
// The two cases that matter are "missed slot" (must PASS — a widened gap is normal devnet
// behaviour, and treating it as a failure was the original bug) and "divides but never equals"
// (must FAIL — otherwise the check degenerates into a divisibility test that a wrong spec value
// would satisfy).
func TestSpacingRule(t *testing.T) {
	const slot = uint64(12)

	for _, tc := range []struct {
		name    string
		times   []uint64
		slot    uint64
		wantErr string
	}{
		{
			name:  "steady chain, every gap one slot",
			times: []uint64{100, 112, 124, 136, 148},
			slot:  slot,
		},
		{
			name:  "one missed slot widens a gap to two",
			times: []uint64{100, 112, 136, 148},
			slot:  slot,
		},
		{
			name:  "several missed slots in a row",
			times: []uint64{100, 112, 160, 172},
			slot:  slot,
		},
		{
			name:    "spec value merely divides the real block time",
			times:   []uint64{100, 112, 124, 136},
			slot:    1,
			wantErr: "does not equal SECONDS_PER_SLOT",
		},
		{
			name:    "spec value is a multiple of the real block time",
			times:   []uint64{100, 112, 124, 136},
			slot:    24,
			wantErr: "not a whole number",
		},
		{
			name:    "gap is not a whole number of slots",
			times:   []uint64{100, 112, 125, 137},
			slot:    slot,
			wantErr: "not a whole number",
		},
		{
			name:    "timestamps do not advance",
			times:   []uint64{100, 112, 112, 124},
			slot:    slot,
			wantErr: "strictly increase",
		},
		{
			name:    "timestamps go backwards",
			times:   []uint64{100, 112, 108},
			slot:    slot,
			wantErr: "strictly increase",
		},
		{
			name:    "not enough samples",
			times:   []uint64{100},
			slot:    slot,
			wantErr: "at least 2 block timestamps",
		},
		{
			name:    "zero slot time",
			times:   []uint64{100, 112},
			slot:    0,
			wantErr: "must be non-zero",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := SpacingError(tc.times, tc.slot)
			if tc.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			require.Contains(t, err.Error(), tc.wantErr)
		})
	}
}
