package smoke

import (
	"testing"

	"github.com/ethereum-optimism/optimism/op-devstack/devtest"
	"github.com/ethereum-optimism/optimism/op-devstack/presets"
	"github.com/ethereum-optimism/optimism/op-service/eth"
	suptypes "github.com/ethereum-optimism/optimism/op-supervisor/supervisor/types"
	"github.com/ethereum/go-ethereum/core/types"
)

// TestL1Glamsterdam_L2Arsia_Smoke is the full-path liveness + stays-Arsia
// smoke test across the L1 Glamsterdam (Amsterdam EL) upgrade.
//
// The Mantle L2 runs Arsia while the L1 upgrades to Glamsterdam. This test proves
// that once the L1 crosses the Amsterdam boundary the L2 keeps operating
// end-to-end and does NOT silently pick up any Amsterdam header field:
//
//  1. wait for the L1 to activate Amsterdam, so every subsequent L2 block is
//     produced while the L2 genuinely consumes a Glamsterdam L1;
//  2. record the L2 unsafe and safe heads at that boundary;
//  3. wait for the L2 to produce ~30 MORE unsafe blocks and assert the unsafe
//     head keeps growing (local block production stays alive) — a sequencer that
//     stalled at the upgrade instant would fail to reach this target;
//  4. assert the safe head also advances PAST the boundary block, i.e. op-node
//     keeps DERIVING from the Glamsterdam L1 and consolidating the blocks
//     produced across it — the whole sequencer -> batcher -> L1 -> derivation
//     path is alive, not just local sealing;
//  5. sample several post-boundary L2 headers and assert every one stays Arsia:
//     no EIP-7928 BlockAccessListHash, no EIP-7843 SlotNumber, and the Isthmus
//     RequestsHash present and fixed to the empty-requests hash.
//
// Discriminating: this asserts 30 blocks of healthy post-Glamsterdam operation
// with BOTH heads advancing and zero Amsterdam header fields on the L2. A
// regression that stalled L2 block production, stalled derivation from a
// Glamsterdam L1, or leaked an Amsterdam header field (BAL / slot number) onto
// the Arsia L2 would fail this test.
func TestL1Glamsterdam_L2Arsia_Smoke(gt *testing.T) {
	t := devtest.SerialT(gt)
	sys := presets.NewMantleMinimal(t)
	require := t.Require()
	ctx := t.Ctx()

	// (1) Wait for the L1 to activate Amsterdam (Glamsterdam EL).
	l1Config := sys.L1Network.Escape().ChainConfig()
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

	// (5) Sample several post-boundary L2 headers spread across the window; each
	// must stay Arsia (no Amsterdam header fields, Isthmus empty-requests hash).
	for _, off := range []uint64{6, 12, 18, 24, moreBlocks} {
		num := boundaryUnsafe + off

		ref := sys.L2EL.BlockRefByNumber(num)
		info, _, err := sys.L2EL.Escape().EthClient().InfoAndTxsByHash(ctx, ref.Hash)
		require.NoErrorf(err, "must read L2 block %d by hash", num)
		require.Equalf(num, info.NumberU64(), "block %d returned unexpected number", num)

		header := info.Header()

		// No Amsterdam header fields may appear on the Mantle (Arsia) L2.
		require.Nilf(header.BlockAccessListHash,
			"L2 (Arsia) block %d must not carry an EIP-7928 BlockAccessListHash", num)
		require.Nilf(header.SlotNumber,
			"L2 (Arsia) block %d must not carry an EIP-7843 SlotNumber", num)

		// RequestsHash must behave as on Arsia: present (Isthmus) and fixed to the
		// empty-requests hash, i.e. the L2 produces no execution-layer requests.
		require.NotNilf(header.RequestsHash,
			"L2 (Arsia) block %d must carry a requests hash (Isthmus behaviour)", num)
		require.Equalf(types.EmptyRequestsHash, *header.RequestsHash,
			"L2 (Arsia) block %d requests hash must be the empty-requests hash", num)
	}
}
