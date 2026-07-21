package system

import (
	"testing"

	opforks "github.com/ethereum-optimism/optimism/op-core/forks"
	"github.com/ethereum-optimism/optimism/op-devstack/devtest"
	"github.com/ethereum-optimism/optimism/op-devstack/presets"
	"github.com/ethereum-optimism/optimism/op-service/eth"
	suptypes "github.com/ethereum-optimism/optimism/op-supervisor/supervisor/types"
)

// TestL1Glamsterdam_L2Arsia_Smoke proves the Mantle L2 re-derives its boundary
// block byte-for-byte from a Glamsterdam L1 and keeps both heads advancing across
// the L1 Amsterdam (Glamsterdam EL) upgrade.
//
// The Mantle L2 runs Arsia while the L1 upgrades to Glamsterdam. The load-bearing
// assertion is that the EXACT L2 block sealed at the L1-Amsterdam boundary reaches
// cross-safe MATCHED BY HASH (ReachedRef): op-node pulled that block's batch back
// out of the Glamsterdam L1 and re-derived a byte-identical block, so the whole
// sequencer -> batcher -> L1 -> derivation path stays alive AND consistent across
// the fork. A divergent re-derivation at that height would fail the hash match.
//
//  1. wait for the L1 to activate Amsterdam, so every subsequent L2 block is
//     produced while the L2 genuinely consumes a Glamsterdam L1;
//  2. record the L2 unsafe and safe heads at that boundary;
//  3. wait for the L2 to produce ~30 MORE unsafe blocks and assert the unsafe
//     head keeps growing — a sequencer that stalled at the upgrade instant would
//     fail to reach this target (sequencer continuity);
//  4. assert the EXACT boundary block reaches cross-safe by HASH (ReachedRef), and
//     that the safe head advances both past the boundary block and strictly beyond
//     where it started — op-node keeps DERIVING and consolidating from the
//     Glamsterdam L1, not just local sealing.
//
// Discriminating: a regression that stalled L2 block production, stalled derivation
// from a Glamsterdam L1, or re-derived a DIFFERENT block at the boundary height
// would fail this test.
func runL1GlamsterdamL2ArsiaSmoke(gt *testing.T) {
	t := devtest.SerialT(gt)
	sys := presets.NewMantleMinimal(t)
	require := t.Require()

	// (1) Wait for the L1 to activate Amsterdam (Glamsterdam EL).
	l1Config := sys.L1Network.Escape().ChainConfig()
	require.True(sys.L2Chain.IsMantleForkActive(opforks.MantleElysium), "L2 must run with Mantle Elysium active")
	require.NotNil(l1Config.AmsterdamTime, "L1 AmsterdamTime must be configured")
	sys.L1EL.WaitForTime(*l1Config.AmsterdamTime)
	t.Log("L1 Amsterdam activated")

	// (2) Record the L2 unsafe/safe heads at the boundary.
	boundaryRef := sys.L2EL.BlockRefByLabel(eth.Unsafe)
	boundaryUnsafe := boundaryRef.Number
	boundarySafe := sys.L2CL.SyncStatus().SafeL2.Number
	t.Logf("recorded L2 boundary heads: unsafe=%d safe=%d", boundaryUnsafe, boundarySafe)

	// (3) Wait for ~30 more unsafe blocks and assert the unsafe head grew.
	const moreBlocks = uint64(30)
	unsafeTarget := boundaryUnsafe + moreBlocks
	grown := sys.L2EL.WaitForUnsafe(func(bi eth.BlockInfo) (bool, error) {
		return bi.NumberU64() >= unsafeTarget, nil
	})
	require.GreaterOrEqual(grown.NumberU64(), unsafeTarget,
		"L2 unsafe head must advance at least %d blocks past the Glamsterdam boundary", moreBlocks)

	// (4) Assert the EXACT boundary block becomes SAFE — matched by HASH, not just height.
	// ReachedRef proves op-node re-derived the byte-identical block the sequencer produced at the
	// boundary from the Glamsterdam L1 (a divergent re-derivation at that height would fail the
	// hash check), so the whole sequencer -> batcher -> L1 -> derivation path is alive AND consistent.
	// (2s per attempt; generous budget for the batcher -> L1 -> derivation lag.)
	sys.L2CL.ReachedRef(suptypes.CrossSafe, eth.BlockID{Number: boundaryRef.Number, Hash: boundaryRef.Hash}, int(moreBlocks)+120)
	safe := sys.L2CL.SyncStatus().SafeL2
	require.GreaterOrEqual(safe.Number, boundaryUnsafe,
		"L2 safe head must advance past the Glamsterdam boundary block %d", boundaryUnsafe)
	require.Greater(safe.Number, boundarySafe,
		"L2 safe head must strictly advance after the L1 crosses Amsterdam")
}
