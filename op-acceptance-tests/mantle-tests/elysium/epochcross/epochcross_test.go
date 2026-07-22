package epochcross

import (
	"math/big"
	"testing"
	"time"

	opforks "github.com/ethereum-optimism/optimism/op-core/forks"
	"github.com/ethereum-optimism/optimism/op-devstack/devtest"
	"github.com/ethereum-optimism/optimism/op-devstack/presets"
	"github.com/ethereum-optimism/optimism/op-service/eth"
)

// TestBoundary_ActivationAtL1EpochBoundary verifies the L2 derives correctly across the Amsterdam
// (Glamsterdam) activation L1 block, in the environment where that activation block also starts a
// beacon EPOCH. OP-Stack derivation advances its L1 origin one block at a time and does not branch
// on beacon-epoch position, so nothing epoch-specific is EXPECTED here. The epoch boundary is the
// environment, not the discriminator: this is a derive-across-activation check that runs at a
// boundary, and its value over TestBoundary_L1ActivationBlock is precisely that environment.
//
// Because the environment IS the point, the environment is asserted rather than assumed: the
// activation block's own EIP-7843 SlotNumber must be a multiple of SLOTS_PER_EPOCH. init_test.go
// gets there by arithmetic (192s offset / 6s L1 block time = block 32 = the first slot of epoch 1),
// and that arithmetic silently breaks if the L1 block time changes or a slot is missed — at which
// point this test would degrade into a duplicate of TestBoundary_L1ActivationBlock while still
// passing. The slot assertion makes that degradation loud.
//
//   - the activation L1 block is genuinely Glamsterdam (its header carries BAL + SlotNumber);
//   - that block starts a beacon epoch (SlotNumber % 32 == 0);
//   - the L2 opens an epoch anchored at that activation block, and that opener is SAFE and anchors
//     to the genuine activation block BY L1-ORIGIN HASH — op-node derived it from the real
//     activation block, not from a same-height namesake on a discarded branch.
//
// Flips red if derivation stalls or diverges across the activation block, or if the activation
// block is no longer on an epoch boundary (the environment this case exists to cover).
func TestBoundary_ActivationAtL1EpochBoundary(gt *testing.T) {
	t := devtest.SerialT(gt)
	sys := presets.NewMantleMinimal(t)
	require := t.Require()
	ctx := t.Ctx()

	l1Config := sys.L1Network.Escape().ChainConfig()
	require.True(sys.L2Chain.IsMantleForkActive(opforks.MantleElysium), "L2 must run with Mantle Elysium active")
	require.NotNil(l1Config.AmsterdamTime, "L1 AmsterdamTime must be configured")

	// Cross Amsterdam and locate the activation L1 block.
	sys.L1EL.WaitForTime(*l1Config.AmsterdamTime)
	l1Head := sys.L1EL.BlockRefByLabel(eth.Unsafe).Number
	var activation uint64
	for n := uint64(1); n <= l1Head; n++ {
		ref := sys.L1EL.BlockRefByNumber(n)
		if l1Config.IsAmsterdam(new(big.Int).SetUint64(n), ref.Time) {
			activation = n
			break
		}
	}
	require.Greater(activation, uint64(1), "activation block must exist with a pre-Amsterdam parent")

	// The activation block is genuinely Glamsterdam.
	aInfo, _, err := sys.L1EL.Escape().EthClient().InfoAndTxsByNumber(ctx, activation)
	require.NoError(err, "must read the activation L1 block")
	require.NotNil(aInfo.Header().BlockAccessListHash, "epoch-boundary activation block must carry a BAL hash")
	require.NotNil(aInfo.Header().SlotNumber, "epoch-boundary activation block must carry a SlotNumber")

	// ENVIRONMENT GUARD. This case only differs from TestBoundary_L1ActivationBlock by running at a
	// beacon-epoch boundary, so assert that it actually is one instead of trusting init_test.go's
	// seconds-to-blocks arithmetic. The block's own EIP-7843 SlotNumber is the authoritative slot,
	// so this survives an L1 block-time change; it fails on a missed slot, which is the point.
	const slotsPerEpoch = 32 // beacon SLOTS_PER_EPOCH
	activationSlot := *aInfo.Header().SlotNumber
	require.Zerof(activationSlot%slotsPerEpoch,
		"the activation L1 block (#%d, slot %d) must start a beacon epoch — this case exists to cover that "+
			"environment; recalibrate amsterdamOffset in init_test.go, otherwise this duplicates "+
			"TestBoundary_L1ActivationBlock", activation, activationSlot)
	t.Log("activation block starts a beacon epoch", "l1Block", activation, "slot", activationSlot,
		"epoch", activationSlot/slotsPerEpoch)

	// Wait for the L2 safe head to derive PAST the activation block.
	require.Eventually(func() bool {
		return sys.L2CL.SyncStatus().SafeL2.L1Origin.Number > activation
	}, 240*time.Second, 2*time.Second, "L2 safe head must derive past the epoch-boundary activation block")

	// Find the SAFE L2 block that opened the epoch anchored at the activation block.
	safeHead := sys.L2CL.SyncStatus().SafeL2.Number
	var opener eth.L2BlockRef
	found := false
	for n := safeHead; n > 0; n-- {
		b := sys.L2EL.BlockRefByNumber(n)
		if b.L1Origin.Number == activation {
			opener = b
			found = true
		} else if found && b.L1Origin.Number < activation {
			break
		}
	}
	require.True(found, "the L2 must open an epoch at the epoch-boundary activation block")
	require.Equal(aInfo.Hash(), opener.L1Origin.Hash,
		"the epoch opener must anchor to the genuine activation L1 block by hash")

	// NOTE: no "opener.Number <= safe head" assertion here. The scan above starts AT the safe head
	// and walks down, so the opener is at or below it by construction and the safe head only ever
	// advances — such a check is true unconditionally and proves nothing. The opener's safety comes
	// from where it was found; its correctness comes from the L1-origin hash equality above.
	t.Log("L2 derived across an epoch-boundary activation", "activation", activation, "epochOpenerL2", opener.Number)
}
