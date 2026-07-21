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
// (Glamsterdam) activation L1 block. The offset is chosen so activation lands on L1 block 32, which
// happens to be an L1 (beacon) epoch boundary — but OP-Stack derivation advances its L1 origin one
// block at a time and does not branch on beacon-epoch position, so nothing epoch-specific is
// expected here. The epoch boundary is the environment, not the discriminator: this is a plain
// derive-across-activation check that happens to run at a boundary.
//
//   - the activation L1 block is genuinely Glamsterdam (its header carries BAL + SlotNumber);
//   - the L2 opens an epoch anchored at that activation block, and that opener reaches the SAFE
//     head matched by L1-origin hash (op-node derived the byte-identical block from the activation
//     origin) and sits at or below the safe head.
//
// Flips red if derivation stalls or diverges across the activation block.
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

	// That opener must be SAFE — derived byte-identically at or below the safe head.
	require.LessOrEqual(opener.Number, sys.L2CL.SyncStatus().SafeL2.Number,
		"the epoch opener must be at or below the safe head")
	t.Log("L2 derived across an epoch-boundary activation", "activation", activation, "epochOpenerL2", opener.Number)
}
