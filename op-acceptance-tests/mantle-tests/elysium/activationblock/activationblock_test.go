package activationblock

import (
	"math/big"
	"testing"
	"time"

	opforks "github.com/ethereum-optimism/optimism/op-core/forks"
	"github.com/ethereum-optimism/optimism/op-devstack/devtest"
	"github.com/ethereum-optimism/optimism/op-devstack/presets"
	"github.com/ethereum-optimism/optimism/op-service/eth"
)

// TestBoundary_L1ActivationBlock proves the Mantle L2 correctly
// derives the L2 block whose L1 origin IS the EXACT Amsterdam (Glamsterdam EL)
// activation block — the first L1 block for which IsAmsterdam holds, and which
// therefore is the first to carry the new EIP-7928 BlockAccessListHash and
// EIP-7843 SlotNumber header fields.
//
// The L2 runs Mantle Arsia EL rules while consuming a Glamsterdam L1. The
// discriminating claim is narrow and about the fork-transition block
// specifically: op-node ingests the VERY FIRST Amsterdam L1 block — with its
// brand-new BAL/SlotNumber header fields present — and still derives a valid,
// SAFE Arsia L2 block whose L1 origin IS that block. If derivation choked on the
// activation block's new header fields, no SAFE L2 block would ever carry that
// L1 origin; that is the failure mode this test rules out at the exact boundary.
//
// GOTCHA obeyed: WithForkAtL1Offset's offset is in SECONDS, not blocks, so the
// activation height is not assumable. The activation block is found DYNAMICALLY
// as the first IsAmsterdam L1 block, and confirmed to be the exact boundary by
// checking that its parent is pre-Amsterdam.
//
// Uses the minimal preset with auto-FakePoS and WaitForTime; no TestSequencer
// drives L1 — the L1 advances on its own clock and the L2 derives from it.
func TestBoundary_L1ActivationBlock(gt *testing.T) {
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
		"Amsterdam must activate above L1 genesis (offset > 0) so the activation block is a real post-genesis block")

	// The block immediately before the activation block must be pre-Amsterdam: this
	// proves `activation` is the EXACT fork-transition block, not merely some later
	// post-fork block.
	parent := sys.L1EL.BlockRefByNumber(activation - 1)
	require.False(l1Config.IsAmsterdam(new(big.Int).SetUint64(activation-1), parent.Time),
		"the block before the activation block must be pre-Amsterdam — activation must be the exact boundary")

	// (precondition) The activation L1 block genuinely carries the NEW Amsterdam
	// header fields. This is exactly what op-node must ingest at the boundary without
	// choking; if these were absent the test would not actually exercise the new fields.
	activationHash := sys.L1EL.BlockRefByNumber(activation).Hash
	l1Info, _, err := sys.L1EL.EthClient().InfoAndTxsByHash(ctx, activationHash)
	require.NoError(err, "must read the typed Amsterdam activation L1 header")
	l1Hdr := l1Info.Header()
	require.NotNil(l1Hdr.BlockAccessListHash,
		"the Amsterdam activation L1 block must carry an EIP-7928 BlockAccessListHash")
	require.NotNil(l1Hdr.SlotNumber,
		"the Amsterdam activation L1 block must carry an EIP-7843 SlotNumber")
	t.Log("Amsterdam activation L1 block located", "number", activation, "hash", activationHash)

	// --- Wait for the L2 SAFE chain to derive PAST the activation origin -------
	// Requiring the safe head's L1 origin to be strictly beyond the activation block
	// guarantees the L2 block(s) whose L1 origin IS the activation block are themselves
	// safe and fully derived: op-node did not stall or choke on the activation block's
	// new header fields.
	l2BlockTime := time.Duration(rollupCfg.BlockTime) * time.Second
	for {
		safe := sys.L2CL.SyncStatus().SafeL2
		if safe.L1Origin.Number > activation {
			break
		}
		t.Log("L2 safe head not yet derived past the Amsterdam activation origin, waiting...",
			"safeOrigin", safe.L1Origin.Number, "activation", activation)
		select {
		case <-time.After(l2BlockTime):
		case <-ctx.Done():
			require.Fail("L2 never derived a safe block past the Amsterdam activation L1 origin")
		}
	}

	// --- Locate the SAFE L2 block whose L1 origin IS the activation block ------
	// L1Origin.Number is monotonic non-decreasing in the L2 block number and cannot
	// skip an L1 block, so walk the safe chain down from the safe head until the origin
	// equals the activation height.
	safeHead := sys.L2CL.SyncStatus().SafeL2
	var l2AtActivation eth.L2BlockRef
	foundL2 := false
	for n := safeHead.Number; ; n-- {
		ref := sys.L2EL.BlockRefByNumber(n)
		if ref.L1Origin.Number == activation {
			l2AtActivation = ref
			foundL2 = true
			break
		}
		if ref.L1Origin.Number < activation || n == 0 {
			break
		}
	}

	// A SAFE L2 block whose L1 origin is exactly the activation block exists.
	require.True(foundL2,
		"a safe L2 block whose L1 origin is the Amsterdam activation block must exist — derivation must not choke at the exact boundary")
	require.Equal(activation, l2AtActivation.L1Origin.Number,
		"the located L2 block's L1 origin must be the activation block number")
	require.Equal(activationHash, l2AtActivation.L1Origin.Hash,
		"the located L2 block's L1 origin hash must be the activation block hash (same block, not a namesake)")
	// NOTE: no "l2AtActivation.Number <= safeHead.Number" assertion. The scan above starts AT the
	// safe head and walks down, so that holds by construction and would pass unconditionally. The
	// block's safety comes from where it was found, not from re-checking the bound.
	t.Log("L2 block derived from the Amsterdam activation L1 origin",
		"l2", l2AtActivation.Number, "l1Origin", l2AtActivation.L1Origin.Number)
}
