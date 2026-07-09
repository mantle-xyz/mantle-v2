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

// epochLenBlocks is the L1 (beacon) epoch length in blocks: 32 slots at one L1 block per slot.
const epochLenBlocks = uint64(32)

// TestBoundary_ActivationAtL1EpochBoundary drives the Amsterdam activation to land exactly on an
// L1 epoch boundary (the offset is chosen so activation is L1 block 32 = 32 slots) and asserts the
// L2 derives across it correctly. OP-Stack derivation advances its L1 origin one block at a time
// and does not branch on beacon-epoch position, so an activation on an epoch boundary must derive
// exactly like any other — this pins that: no epoch-boundary-specific derivation glitch.
//
//   - the activation L1 block sits on an epoch boundary (number % 32 == 0) and is genuinely
//     Glamsterdam (carries BAL + SlotNumber);
//   - the L2 opens the epoch anchored at the activation block, that block reaches the SAFE head
//     matched by hash (op-node derived the byte-identical block from the epoch-boundary origin),
//     and it stays Arsia.
//
// Flips red if derivation stalls or diverges at an epoch-boundary activation, or leaks a header
// field onto the L2.
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

	// The offset was chosen so the activation lands on an L1 epoch boundary.
	require.Equalf(uint64(0), activation%epochLenBlocks,
		"activation L1 block %d must land on an epoch boundary (multiple of %d)", activation, epochLenBlocks)

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

	// That opener must be SAFE (derived byte-identically) and stay Arsia.
	require.LessOrEqual(opener.Number, sys.L2CL.SyncStatus().SafeL2.Number,
		"the epoch opener must be at or below the safe head")
	l2Info, _, err := sys.L2EL.Escape().EthClient().InfoAndTxsByHash(ctx, opener.Hash)
	require.NoError(err, "must read the epoch-opener L2 block")
	require.Nil(l2Info.Header().BlockAccessListHash, "the epoch-opener L2 block must stay Arsia (no BAL)")
	require.Nil(l2Info.Header().SlotNumber, "the epoch-opener L2 block must stay Arsia (no SlotNumber)")
	t.Log("L2 derived across an epoch-boundary activation", "activation", activation, "epochOpenerL2", opener.Number)
}
