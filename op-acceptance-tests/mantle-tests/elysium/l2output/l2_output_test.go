package l2output

import (
	"testing"

	"github.com/ethereum-optimism/optimism/op-devstack/devtest"
	"github.com/ethereum-optimism/optimism/op-devstack/presets"
	"github.com/ethereum-optimism/optimism/op-service/eth"
)

// l2BlockUnderGlamsterdamL1 waits for the L1 to activate Amsterdam, then returns a
// subsequently-produced L2 unsafe block — i.e. an L2 block whose production
// overlaps a live Glamsterdam L1. The Mantle L2 stays on Arsia rules across this
// boundary; these tests assert it does not silently pick up Amsterdam header
// fields.
func l2BlockUnderGlamsterdamL1(t devtest.T, sys *presets.MantleMinimal) eth.BlockInfo {
	l1Config := sys.L1Network.Escape().ChainConfig()
	t.Require().NotNil(l1Config.AmsterdamTime, "L1 AmsterdamTime must be configured")
	sys.L1EL.WaitForTime(*l1Config.AmsterdamTime)

	start := sys.L2EL.BlockRefByLabel(eth.Unsafe).Number
	return sys.L2EL.WaitForUnsafe(func(bi eth.BlockInfo) (bool, error) {
		return bi.NumberU64() >= start+2, nil
	})
}

// TestL2Block_NoBlockAccessListHash asserts a Mantle (Arsia) L2 block carries no
// EIP-7928 block-access-list hash, even while the L1 runs Glamsterdam.
func TestL2Block_NoBlockAccessListHash(gt *testing.T) {
	t := devtest.SerialT(gt)
	sys := presets.NewMantleMinimal(t)

	header := l2BlockUnderGlamsterdamL1(t, sys).Header()
	t.Require().Nil(header.BlockAccessListHash, "L2 (Arsia) block must not carry BlockAccessListHash")
}

// TestL2Block_NoSlotNumber asserts a Mantle (Arsia) L2 block carries no EIP-7843
// slot number, even while the L1 runs Glamsterdam.
func TestL2Block_NoSlotNumber(gt *testing.T) {
	t := devtest.SerialT(gt)
	sys := presets.NewMantleMinimal(t)

	header := l2BlockUnderGlamsterdamL1(t, sys).Header()
	t.Require().Nil(header.SlotNumber, "L2 (Arsia) block must not carry SlotNumber")
}
