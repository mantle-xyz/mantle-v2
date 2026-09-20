package smoke

import (
	"testing"
	"time"

	"github.com/ethereum-optimism/optimism/op-acceptance-tests/mantle-tests/elysium/internal/testhelpers"
	opforks "github.com/ethereum-optimism/optimism/op-core/forks"
	"github.com/ethereum-optimism/optimism/op-devstack/devtest"
	"github.com/ethereum-optimism/optimism/op-devstack/presets"
	"github.com/ethereum-optimism/optimism/op-service/eth"
	suptypes "github.com/ethereum-optimism/optimism/op-supervisor/supervisor/types"
)

// TestL1Glamsterdam_L2Arsia_Smoke proves the L2 keeps producing and deriving
// after the L1 Amsterdam upgrade.
//
// The key assertion is hash-based: the exact boundary L2 block must reach
// cross-safe, proving op-node re-derived the byte-identical block from the
// Glamsterdam L1 rather than only advancing to the same height.
func TestL1Glamsterdam_L2Arsia_Smoke(gt *testing.T) {
	t := devtest.SerialT(gt)
	sys := presets.NewMantleMinimal(t)
	require := t.Require()

	// (1) Wait for the L1 to activate Amsterdam (Glamsterdam EL).
	l1Config := sys.L1Network.Escape().ChainConfig()
	require.True(sys.L2Chain.IsMantleForkActive(opforks.MantleElysium), "L2 must run with Mantle Elysium active")
	require.NotNil(l1Config.AmsterdamTime, "L1 AmsterdamTime must be configured")
	testhelpers.WaitForGlamsterdamL1(t, sys.L1EL, *l1Config.AmsterdamTime)
	t.Log("L1 Amsterdam activated")

	// (2) Record the L2 unsafe/safe heads at the boundary.
	boundaryRef := sys.L2EL.BlockRefByLabel(eth.Unsafe)
	boundaryUnsafe := boundaryRef.Number
	boundarySafe := sys.L2CL.SyncStatus().SafeL2.Number
	t.Logf("recorded L2 boundary heads: unsafe=%d safe=%d", boundaryUnsafe, boundarySafe)

	// (3) Wait for ~30 more unsafe blocks. WaitForUnsafe fails the test itself if the
	// predicate never holds, so reasserting its own condition afterwards proves nothing.
	const moreBlocks = uint64(30)
	unsafeTarget := boundaryUnsafe + moreBlocks
	sys.L2EL.WaitForUnsafe(func(bi eth.BlockInfo) (bool, error) {
		return bi.NumberU64() >= unsafeTarget, nil
	})

	// (4) Match by hash, not just height, so a divergent re-derivation fails.
	sys.L2CL.ReachedRef(suptypes.CrossSafe, eth.BlockID{Number: boundaryRef.Number, Hash: boundaryRef.Hash}, int(moreBlocks)+120)

	// ReachedRef already pins safe >= boundaryRef.Number, and boundarySafe trails boundaryRef.Number
	// (the safe head lags the unsafe head), so comparing the new safe head against boundarySafe
	// would be satisfied the moment ReachedRef returned and could never fail on its own. Require
	// the safe head to move strictly PAST the boundary block instead: derivation that reproduces
	// the boundary block byte-identically and then stalls -- while the sequencer happily keeps
	// producing the 30 blocks step (3) waits for -- is precisely the regression this step exists
	// to catch, and it is invisible to any check anchored on boundarySafe.
	require.Eventuallyf(func() bool {
		return sys.L2CL.SyncStatus().SafeL2.Number > boundaryRef.Number
	}, 120*time.Second, time.Second,
		"L2 safe head must derive strictly past the boundary block #%d rather than stalling on it "+
			"(safe head at the boundary was #%d)", boundaryRef.Number, boundarySafe)
}
