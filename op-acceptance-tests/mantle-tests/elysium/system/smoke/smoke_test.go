package smoke

import (
	"testing"

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

	// (4) Match by hash, not just height, so a divergent re-derivation fails. ReachedRef
	// already pins safe >= boundaryRef.Number; only the strict advance past the safe head
	// recorded at the boundary adds anything on top of it.
	sys.L2CL.ReachedRef(suptypes.CrossSafe, eth.BlockID{Number: boundaryRef.Number, Hash: boundaryRef.Hash}, int(moreBlocks)+120)
	safe := sys.L2CL.SyncStatus().SafeL2
	require.Greater(safe.Number, boundarySafe,
		"L2 safe head must strictly advance after the L1 crosses Amsterdam")
}
