package epochcross

import (
	"math/big"
	"testing"
	"time"

	"github.com/ethereum-optimism/optimism/op-acceptance-tests/mantle-tests/elysium/internal/testhelpers"
	opforks "github.com/ethereum-optimism/optimism/op-core/forks"
	"github.com/ethereum-optimism/optimism/op-devstack/devtest"
	"github.com/ethereum-optimism/optimism/op-devstack/presets"
	"github.com/ethereum-optimism/optimism/op-service/eth"
)

// TestBoundary_ActivationAtL1EpochBoundary verifies derivation across the
// Amsterdam activation block when that L1 block also starts a beacon epoch.
//
// The test asserts the environment instead of trusting offset arithmetic:
// the activation header must carry BAL/SlotNumber, SlotNumber must be divisible
// by SLOTS_PER_EPOCH, and the safe L2 epoch opener must anchor to that exact L1
// block by hash.
func TestBoundary_ActivationAtL1EpochBoundary(gt *testing.T) {
	t := devtest.SerialT(gt)
	sys := presets.NewMantleMinimal(t)
	require := t.Require()
	ctx := t.Ctx()

	l1Config := sys.L1Network.Escape().ChainConfig()
	require.True(sys.L2Chain.IsMantleForkActive(opforks.MantleElysium), "L2 must run with Mantle Elysium active")
	require.NotNil(l1Config.AmsterdamTime, "L1 AmsterdamTime must be configured")

	// Cross Amsterdam and locate the activation L1 block.
	testhelpers.WaitForGlamsterdamL1(t, sys.L1EL, *l1Config.AmsterdamTime)
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

	// This case only differs from TestBoundary_L1ActivationBlock if activation
	// really lands on a beacon-epoch boundary.
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

	// Find the safe L2 block that opened the epoch anchored at the activation block.
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

	// The reverse scan starts from the safe head, so the opener's safety comes
	// from where it was found; the L1-origin hash proves correctness.
	t.Log("L2 derived across an epoch-boundary activation", "activation", activation, "epochOpenerL2", opener.Number)
}
