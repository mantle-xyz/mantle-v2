package headernopollut

import (
	"math/big"
	"testing"
	"time"

	opforks "github.com/ethereum-optimism/optimism/op-core/forks"
	"github.com/ethereum-optimism/optimism/op-devstack/devtest"
	"github.com/ethereum-optimism/optimism/op-devstack/presets"
	"github.com/ethereum-optimism/optimism/op-node/rollup/derive"
	"github.com/ethereum-optimism/optimism/op-service/eth"
)

// TestDerivation_LegalNewFieldHeader_NoAttributePollution verifies that op-node parses a
// well-formed Glamsterdam L1 header's BASE FEE without corruption and round-trips it into the L2
// block's L1-info deposit, even though that header now carries the new EIP-7928 BlockAccessListHash
// and EIP-7843 SlotNumber fields.
//
// The load-bearing assertion is a CROSS-SOURCE base-fee round-trip: the base fee read straight from
// the L1 origin header (via the L1 EL) must equal the base fee op-node decoded and embedded in the
// L2 L1-info deposit. Those two values come from independent producers — the L1 client vs. op-node's
// parse of the L1 header — so if the new Amsterdam fields shifted or corrupted op-node's read of the
// base fee, the two would diverge and this flips red. The check is gated on the L1 origin genuinely
// being Glamsterdam (BAL + SlotNumber present on the L1 header), so it only ever exercises op-node
// against a real post-Amsterdam header — that gate is what makes the base-fee check L1-sensitive.
//
// Secondary, weaker checks (kept, but NOT the headline):
//
//   - The number and block-hash round-trip (L1-info decoded from the deposit vs. the L2 block's own
//     L1Origin metadata) is SEMI-CIRCULAR: op-node produced both sides from the same L1 attributes,
//     so it is a self-consistency check, not independent evidence of a correct parse.
//   - The L2-stays-Arsia checks (no BlockAccessListHash / SlotNumber on the L2 header) are
//     STRUCTURAL: an Arsia L2 never carries those fields regardless of which L1 it runs against, so
//     they would pass under any L1. They guard that op-node did not absorb an L1 field, but they are
//     not an L1-sensitive discriminator.
//
// This is the POSITIVE half of the new-field-header behaviour; the malicious/inconsistent-header
// rejection is the negative half (needs bad-header injection, runs under sysext).
func TestDerivation_LegalNewFieldHeader_NoAttributePollution(gt *testing.T) {
	t := devtest.SerialT(gt)
	sys := presets.NewMantleMinimal(t)
	require := t.Require()
	ctx := t.Ctx()

	rollupCfg := sys.L2Chain.Escape().RollupConfig()
	l1Config := sys.L1Network.Escape().ChainConfig()
	require.True(sys.L2Chain.IsMantleForkActive(opforks.MantleElysium), "L2 must run with Mantle Elysium active")
	require.NotNil(l1Config.AmsterdamTime, "L1 AmsterdamTime must be configured")

	// Cross Amsterdam and wait for the L2 unsafe origin to cross it too, then build a few more.
	sys.L1EL.WaitForTime(*l1Config.AmsterdamTime)
	require.Eventually(func() bool {
		o := sys.L2EL.BlockRefByLabel(eth.Unsafe).L1Origin.Number
		r := sys.L1EL.BlockRefByNumber(o)
		return l1Config.IsAmsterdam(new(big.Int).SetUint64(o), r.Time)
	}, 120*time.Second, time.Second, "L2 unsafe origin must cross Amsterdam")
	start := sys.L2EL.BlockRefByLabel(eth.Unsafe).Number
	sys.L2EL.WaitForUnsafe(func(bi eth.BlockInfo) (bool, error) { return bi.NumberU64() >= start+12, nil })

	// Sample post-Amsterdam-origin L2 blocks and verify the attribute flow.
	checked := 0
	head := sys.L2EL.BlockRefByLabel(eth.Unsafe).Number
	for n := head; n > 0 && checked < 4; n-- {
		b := sys.L2EL.BlockRefByNumber(n)
		l1Ref := sys.L1EL.BlockRefByNumber(b.L1Origin.Number)
		if !l1Config.IsAmsterdam(new(big.Int).SetUint64(b.L1Origin.Number), l1Ref.Time) {
			continue
		}

		// Gate: the L1 origin the L2 block anchors to must be a genuine Glamsterdam block. This is
		// what makes the base-fee round-trip below L1-sensitive — it only runs against a real
		// post-Amsterdam header carrying the new EIP-7928/EIP-7843 fields.
		l1Info, _, err := sys.L1EL.Escape().EthClient().InfoAndTxsByHash(ctx, b.L1Origin.Hash)
		require.NoErrorf(err, "read L1 origin of L2 block %d", n)
		require.Equalf(b.L1Origin.Number, l1Info.NumberU64(), "L2 block %d L1 origin number mismatch", n)
		require.NotNilf(l1Info.Header().BlockAccessListHash, "L1 origin of L2 block %d must be genuine Glamsterdam (BAL)", n)
		require.NotNilf(l1Info.Header().SlotNumber, "L1 origin of L2 block %d must be genuine Glamsterdam (SlotNumber)", n)

		// Decode the L2 block's L1-info deposit (first tx).
		l2Info, l2Txs, err := sys.L2EL.Escape().EthClient().InfoAndTxsByHash(ctx, b.Hash)
		require.NoErrorf(err, "read L2 block %d", n)
		require.NotEmptyf(l2Txs, "L2 block %d must carry an L1-info deposit tx", n)
		info, err := derive.L1BlockInfoFromBytes(rollupCfg, l2Info.Time(), l2Txs[0].Data())
		require.NoErrorf(err, "decode L1-info of L2 block %d", n)

		// HEADLINE, cross-source: the base fee op-node decoded into the L1-info deposit must equal the
		// base fee read straight from the L1 origin header (l1Info, fetched above from the L1 EL).
		// Independent producers — a mis-parse of the Glamsterdam header corrupting op-node's base-fee
		// read would make these diverge.
		require.Equalf(l1Info.BaseFee().Uint64(), info.BaseFee.Uint64(),
			"L2 block %d L1-info base fee must match the Glamsterdam L1 origin", n)

		// Secondary, semi-circular: number/hash come from the same producer (op-node wrote both the
		// L1Origin metadata and the deposit from the same L1 attributes), so this is self-consistency,
		// not independent evidence.
		require.Equalf(b.L1Origin.Number, info.Number, "L2 block %d L1-info number must match its origin", n)
		require.Equalf(b.L1Origin.Hash, info.BlockHash, "L2 block %d L1-info block hash must match its origin", n)

		// Secondary, structural: an Arsia L2 header never carries BAL / SlotNumber regardless of the
		// L1 fork, so these would pass under any L1. Belt-and-suspenders that op-node did not absorb an
		// L1 field onto the L2 — not an L1-sensitive discriminator.
		require.Nilf(l2Info.Header().BlockAccessListHash, "L2 block %d must not carry a BAL hash", n)
		require.Nilf(l2Info.Header().SlotNumber, "L2 block %d must not carry a SlotNumber", n)
		checked++
	}
	require.GreaterOrEqual(checked, 2, "must have checked at least two post-Amsterdam-origin L2 blocks")
	t.Log("post-Amsterdam L1 headers flow into L2 attributes without pollution", "checked", checked)
}
