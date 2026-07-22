package preactivation

import (
	"math/big"
	"testing"
	"time"

	opforks "github.com/ethereum-optimism/optimism/op-core/forks"
	"github.com/ethereum-optimism/optimism/op-devstack/devtest"
	"github.com/ethereum-optimism/optimism/op-devstack/presets"
	"github.com/ethereum-optimism/optimism/op-service/eth"
)

// TestBoundary_L1PreActivationBlock is the mirror image of TestBoundary_L1ActivationBlock:
// it proves that the LAST pre-activation L1 block — the block immediately BEFORE Amsterdam
// (Glamsterdam EL) activates — is still in the OLD (legacy) header format, and that the
// Mantle L2 (running Arsia EL rules) derives a valid SAFE block from that pre-Amsterdam
// origin.
//
// Together the activation and pre-activation tests pin the fork transition to the EXACT
// activation block: NEW format (EIP-7928 BlockAccessListHash + EIP-7843 SlotNumber present)
// at/after activation, OLD format (both fields absent) strictly before it.
//
// The discriminating claims are narrow and about the block just below the boundary:
//
//	(a) IsAmsterdam(parent) == false — the block at height activation-1 is genuinely
//	    pre-Amsterdam, so it is the LAST legacy block, not some earlier one; and
//	(b) that pre-Amsterdam L1 header carries NEITHER new field: BlockAccessListHash == nil
//	    AND SlotNumber == nil. A miscalibrated fork boundary (activation one block too late)
//	    would set these fields on this very block; a real legacy header leaves them nil.
//	    For contrast the activation block (parent+1) is confirmed to carry BOTH — old format
//	    before, new format at/after, boundary exactly at activation; and
//	(c) the L2 derives a SAFE block whose L1 origin IS that pre-Amsterdam block — op-node
//	    ingests the legacy origin without stalling, exactly as it does the new-format one.
//
// GOTCHA obeyed: WithForkAtL1Offset's offset is in SECONDS, not blocks, so the activation
// height is not assumable. The activation block is found DYNAMICALLY as the first
// IsAmsterdam L1 block; the pre-activation block is its parent (activation-1).
//
// Uses the minimal preset with auto-FakePoS and WaitForTime; no TestSequencer drives L1 —
// the L1 advances on its own clock and the L2 derives from it.
func TestBoundary_L1PreActivationBlock(gt *testing.T) {
	t := devtest.SerialT(gt)
	sys := presets.NewMantleMinimal(t)
	require := t.Require()
	ctx := t.Ctx()

	rollupCfg := sys.L2Chain.Escape().RollupConfig()
	l1Config := sys.L1Network.Escape().ChainConfig()

	require.True(sys.L2Chain.IsMantleForkActive(opforks.MantleElysium),
		"L2 must run with Mantle Elysium active (Arsia EL header rules)")
	require.NotNil(rollupCfg.MantleElysiumTime, "MantleElysiumTime must be configured")
	require.NotNil(l1Config.AmsterdamTime, "L1 AmsterdamTime must be configured")

	// Wait for the L1 to reach/cross the Amsterdam (Glamsterdam EL) activation time.
	t.Log("Waiting for L1 Amsterdam to activate")
	sys.L1EL.WaitForTime(*l1Config.AmsterdamTime)
	t.Log("L1 Amsterdam activated")

	// --- Find the Amsterdam activation L1 block DYNAMICALLY -------------------
	// The first block for which IsAmsterdam(num, time) holds. The fork offset is
	// SECONDS, not blocks, so we must not assume a fixed height.
	l1Head := sys.L1EL.BlockRefByLabel(eth.Unsafe).Number
	var activation uint64
	foundActivation := false
	for n := uint64(0); n <= l1Head; n++ {
		ref := sys.L1EL.BlockRefByNumber(n)
		if l1Config.IsAmsterdam(new(big.Int).SetUint64(n), ref.Time) {
			activation = n
			foundActivation = true
			break
		}
	}
	require.True(foundActivation, "must find the first Amsterdam L1 block at or below the current L1 head")
	require.Greater(activation, uint64(0),
		"Amsterdam must activate above L1 genesis (offset > 0) so a real pre-activation parent block exists")

	// The pre-activation block: the block immediately before the activation block.
	preActivation := activation - 1
	preRef := sys.L1EL.BlockRefByNumber(preActivation)

	// (a) The pre-activation block is genuinely PRE-Amsterdam. This proves it is the LAST
	// legacy block — the exact block just below the fork boundary — not some earlier block.
	require.False(l1Config.IsAmsterdam(new(big.Int).SetUint64(preActivation), preRef.Time),
		"the block before the activation block must be pre-Amsterdam — it is the last legacy (old-format) L1 block")

	// (b) The typed pre-Amsterdam L1 header is in the OLD format: it carries NEITHER of the
	// two Amsterdam header fields. Both must be nil on a legacy header. This is the
	// discriminating no-new-fields guard: if the fork boundary were miscalibrated one block
	// too late, these fields would appear on exactly this block.
	preHash := preRef.Hash
	preInfo, _, err := sys.L1EL.EthClient().InfoAndTxsByHash(ctx, preHash)
	require.NoError(err, "must read the typed pre-Amsterdam L1 header")
	preHdr := preInfo.Header()
	require.Nil(preHdr.BlockAccessListHash,
		"the last pre-Amsterdam L1 block must be old-format — no EIP-7928 BlockAccessListHash")
	require.Nil(preHdr.SlotNumber,
		"the last pre-Amsterdam L1 block must be old-format — no EIP-7843 SlotNumber")
	t.Log("Last pre-Amsterdam L1 block located", "number", preActivation, "hash", preHash)

	// (b-contrast) The activation block (parent+1) DOES carry BOTH new Amsterdam header
	// fields. This pins the transition to the exact boundary: old format at activation-1,
	// new format at activation — the change happens across this single block edge.
	activationHash := sys.L1EL.BlockRefByNumber(activation).Hash
	actInfo, _, err := sys.L1EL.EthClient().InfoAndTxsByHash(ctx, activationHash)
	require.NoError(err, "must read the typed Amsterdam activation L1 header")
	actHdr := actInfo.Header()
	require.NotNil(actHdr.BlockAccessListHash,
		"the Amsterdam activation L1 block (pre-activation's child) must carry an EIP-7928 BlockAccessListHash — new format at the boundary")
	require.NotNil(actHdr.SlotNumber,
		"the Amsterdam activation L1 block (pre-activation's child) must carry an EIP-7843 SlotNumber — new format at the boundary")
	require.Equal(preHash, actHdr.ParentHash,
		"the activation block must be the direct child of the pre-activation block — the boundary is this single block edge")

	// --- Wait for the L2 SAFE chain to derive PAST the pre-activation origin ---
	// Requiring the safe head's L1 origin to be strictly beyond the pre-activation block
	// guarantees the L2 block(s) whose L1 origin IS that block are themselves safe and fully
	// derived: op-node did not stall or choke on the last legacy L1 block.
	l2BlockTime := time.Duration(rollupCfg.BlockTime) * time.Second
	for {
		safe := sys.L2CL.SyncStatus().SafeL2
		if safe.L1Origin.Number > preActivation {
			break
		}
		t.Log("L2 safe head not yet derived past the pre-Amsterdam origin, waiting...",
			"safeOrigin", safe.L1Origin.Number, "preActivation", preActivation)
		select {
		case <-time.After(l2BlockTime):
		case <-ctx.Done():
			require.Fail("L2 never derived a safe block past the pre-Amsterdam L1 origin")
		}
	}

	// --- Locate the SAFE L2 block whose L1 origin IS the pre-activation block --
	// L1Origin.Number is monotonic non-decreasing in the L2 block number and cannot skip an
	// L1 block, so walk the safe chain down from the safe head until the origin equals the
	// pre-activation height.
	safeHead := sys.L2CL.SyncStatus().SafeL2
	var l2AtPre eth.L2BlockRef
	foundL2 := false
	for n := safeHead.Number; ; n-- {
		ref := sys.L2EL.BlockRefByNumber(n)
		if ref.L1Origin.Number == preActivation {
			l2AtPre = ref
			foundL2 = true
			break
		}
		if ref.L1Origin.Number < preActivation || n == 0 {
			break
		}
	}

	// (c) A SAFE L2 block whose L1 origin is exactly the pre-activation block exists: the L2
	// derives fine from the last legacy L1 origin.
	require.True(foundL2,
		"a safe L2 block whose L1 origin is the last pre-Amsterdam block must exist — derivation must not stall on the legacy origin")
	require.Equal(preActivation, l2AtPre.L1Origin.Number,
		"the located L2 block's L1 origin must be the pre-activation block number")
	require.Equal(preHash, l2AtPre.L1Origin.Hash,
		"the located L2 block's L1 origin hash must be the pre-activation block hash (same block, not a namesake)")
	// NOTE: no "l2AtPre.Number <= safeHead.Number" assertion. The scan above starts AT the safe head
	// and walks down, so that holds by construction and would pass unconditionally. The block's
	// safety comes from where it was found, not from re-checking the bound.
	t.Log("L2 block derived from the pre-Amsterdam L1 origin",
		"l2", l2AtPre.Number, "l1Origin", l2AtPre.L1Origin.Number)
}
