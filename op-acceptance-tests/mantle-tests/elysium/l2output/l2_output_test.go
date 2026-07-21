package l2output

import (
	"testing"

	"github.com/ethereum-optimism/optimism/op-devstack/devtest"
	"github.com/ethereum-optimism/optimism/op-devstack/presets"
	"github.com/ethereum-optimism/optimism/op-service/eth"
)

// l2BlockUnderGlamsterdamL1 waits for the L1 to activate Amsterdam (Glamsterdam),
// then returns a subsequently-produced L2 unsafe block. The L1 fork is the
// environment the block is sampled in, not a discriminator: the Mantle L2 stays on
// Arsia rules regardless of the L1 fork, so an Arsia header looks the same under
// any L1. Callers assert L2-Arsia header properties against this block.
func l2BlockUnderGlamsterdamL1(t devtest.T, sys *presets.MantleMinimal) eth.BlockInfo {
	l1Config := sys.L1Network.Escape().ChainConfig()
	t.Require().NotNil(l1Config.AmsterdamTime, "L1 AmsterdamTime must be configured")
	sys.L1EL.WaitForTime(*l1Config.AmsterdamTime)

	start := sys.L2EL.BlockRefByLabel(eth.Unsafe).Number
	return sys.L2EL.WaitForUnsafe(func(bi eth.BlockInfo) (bool, error) {
		return bi.NumberU64() >= start+2, nil
	})
}

// TestL2Block_NoBlockAccessListHash is an L2-Arsia header regression guard: an
// Arsia L2 block carries no EIP-7928 block-access-list hash. That field is an
// optional Amsterdam-era header field, gated on the L2 chain config, so it is
// structurally always nil on Arsia — this check would pass under any L1, and the
// Glamsterdam L1 is only the environment, not the discriminator. The nil check
// still guards against the L2 config silently adopting the Amsterdam header field.
func TestL2Block_NoBlockAccessListHash(gt *testing.T) {
	t := devtest.SerialT(gt)
	sys := presets.NewMantleMinimal(t)

	header := l2BlockUnderGlamsterdamL1(t, sys).Header()
	t.Require().Nil(header.BlockAccessListHash, "L2 (Arsia) block must not carry BlockAccessListHash")
}

// TestL2Block_NoSlotNumber is an L2-Arsia header regression guard: an Arsia L2
// block carries no EIP-7843 slot number. That field is an optional Amsterdam-era
// header field, gated on the L2 chain config, so it is structurally always nil on
// Arsia — this check would pass under any L1, and the Glamsterdam L1 is only the
// environment, not the discriminator. The nil check still guards against the L2
// config silently adopting the Amsterdam header field.
func TestL2Block_NoSlotNumber(gt *testing.T) {
	t := devtest.SerialT(gt)
	sys := presets.NewMantleMinimal(t)

	header := l2BlockUnderGlamsterdamL1(t, sys).Header()
	t.Require().Nil(header.SlotNumber, "L2 (Arsia) block must not carry SlotNumber")
}
