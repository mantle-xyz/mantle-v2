package l2output

import (
	"testing"

	"github.com/ethereum-optimism/optimism/op-devstack/devtest"
	"github.com/ethereum-optimism/optimism/op-devstack/presets"
	"github.com/ethereum-optimism/optimism/op-service/eth"
)

// requireGlamsterdamL1Control proves that THIS run actually produces the EIP-7928 /
// EIP-7843 header fields, by requiring them on the L1 block that crossed Amsterdam.
//
// It exists because the L2 assertions below are negative ("the field must be nil"), and a
// negative assertion is only worth what its control is worth. Two very different states
// both leave the L2 header nil: the L2 correctly omitting an Amsterdam field, and nothing
// in the stack ever producing that field at all. Without a control the tests cannot
// distinguish them, and would stay green even if the fields were unimplemented end to end.
//
// The L1 header is the control, and it is a strict one because it travels the SAME path the
// L2 assertions read: eth_getBlockByNumber JSON -> sources.RPCHeader -> CreateGethHeader
// (op-service/sources/types.go). A non-nil value here proves the field survives production,
// serialisation, transport and parsing, so a nil on the L2 side is a real statement about
// the L2 rather than an artefact of the read path.
//
// It also converts one silent environment degradation into a loud failure. When the
// mise-installed geth is absent, configureDevstackEnvVars falls back to the in-process
// op-geth, whose RPC layer marshals no blockAccessListHash at all (its RPCMarshalHeader has
// no branch for it); every L2 nil-check would then pass vacuously. This control fails
// instead, and says why.
func requireGlamsterdamL1Control(t devtest.T, sys *presets.MantleMinimal, l1Ref eth.BlockRef) {
	require := t.Require()

	l1Info, err := sys.L1EL.Escape().EthClient().InfoByHash(t.Ctx(), l1Ref.Hash)
	require.NoErrorf(err, "read the L1 block %d that crossed AmsterdamTime", l1Ref.Number)
	l1Header := l1Info.Header()

	require.NotNilf(l1Header.BlockAccessListHash,
		"L1 block %d is at/after AmsterdamTime but carries no BlockAccessListHash: this run does "+
			"not produce EIP-7928 headers at all, so the L2 nil-checks prove nothing. Is the L1 EL "+
			"a real Amsterdam-capable geth, or did it fall back to the in-process op-geth?", l1Ref.Number)
	require.NotNilf(l1Header.SlotNumber,
		"L1 block %d is at/after AmsterdamTime but carries no SlotNumber: this run does not produce "+
			"EIP-7843 headers at all, so the L2 nil-checks prove nothing", l1Ref.Number)
}

// l2BlockUnderGlamsterdamL1 waits for the L1 to activate Amsterdam (Glamsterdam), proves the
// L1 genuinely emits the new header fields, then returns a subsequently-produced L2 unsafe
// block. The L1 fork is the environment the block is sampled in, not a discriminator: the
// Mantle L2 stays on Arsia rules regardless of the L1 fork. Callers assert L2-Arsia header
// properties against this block, and the control above is what makes those assertions mean
// something.
func l2BlockUnderGlamsterdamL1(t devtest.T, sys *presets.MantleMinimal) eth.BlockInfo {
	l1Config := sys.L1Network.Escape().ChainConfig()
	t.Require().NotNil(l1Config.AmsterdamTime, "L1 AmsterdamTime must be configured")
	l1Ref := sys.L1EL.WaitForTime(*l1Config.AmsterdamTime)
	requireGlamsterdamL1Control(t, sys, l1Ref)

	start := sys.L2EL.BlockRefByLabel(eth.Unsafe).Number
	return sys.L2EL.WaitForUnsafe(func(bi eth.BlockInfo) (bool, error) {
		return bi.NumberU64() >= start+2, nil
	})
}

// TestL2Block_NoBlockAccessListHash is an L2-Arsia header regression guard: an Arsia L2
// block carries no EIP-7928 block-access-list hash, sampled while the L1 runs Glamsterdam.
//
// The nil check alone is weak — the field is gated on the L2 chain config, so it is nil on
// Arsia for structural reasons and this assertion would hold under any L1. What gives it
// content is the L1 control inside l2BlockUnderGlamsterdamL1: the same run, read through the
// same client path, is required to show a NON-nil BlockAccessListHash on L1. So the pair
// says "this stack does produce EIP-7928 headers, and the L2's is still empty" rather than
// the far weaker "nobody here produces them".
func TestL2Block_NoBlockAccessListHash(gt *testing.T) {
	t := devtest.SerialT(gt)
	sys := presets.NewMantleMinimal(t)

	header := l2BlockUnderGlamsterdamL1(t, sys).Header()
	t.Require().Nil(header.BlockAccessListHash, "L2 (Arsia) block must not carry BlockAccessListHash")
}

// TestL2Block_NoSlotNumber is an L2-Arsia header regression guard: an Arsia L2 block carries
// no EIP-7843 slot number, sampled while the L1 runs Glamsterdam.
//
// As with the BlockAccessListHash case above, the nil check earns its meaning from the L1
// control in l2BlockUnderGlamsterdamL1, which requires the same run to show a NON-nil
// SlotNumber on L1. Without that control a stack that never implements the field would be
// indistinguishable from an L2 that correctly omits it.
func TestL2Block_NoSlotNumber(gt *testing.T) {
	t := devtest.SerialT(gt)
	sys := presets.NewMantleMinimal(t)

	header := l2BlockUnderGlamsterdamL1(t, sys).Header()
	t.Require().Nil(header.SlotNumber, "L2 (Arsia) block must not carry SlotNumber")
}
