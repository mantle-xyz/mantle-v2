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

// TestDerivation_LegalNewFieldHeader_NoAttributePollution verifies op-node can
// parse a valid Glamsterdam L1 header without shifting L1 attributes into the L2.
//
// The load-bearing check is cross-source: the base fee read directly from the
// L1 origin header must match the base fee op-node embedded in the L2 L1-info
// deposit. Number/hash round-trips and nil L2 BAL/SlotNumber checks are kept as
// secondary self-consistency guards.
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

		// Gate the base-fee round-trip on a genuine Glamsterdam L1 origin.
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

		// Cross-source: op-node's decoded base fee must match the L1 EL header.
		require.Equalf(l1Info.BaseFee().Uint64(), info.BaseFee.Uint64(),
			"L2 block %d L1-info base fee must match the Glamsterdam L1 origin", n)

		// Secondary self-consistency: op-node produced both sides.
		require.Equalf(b.L1Origin.Number, info.Number, "L2 block %d L1-info number must match its origin", n)
		require.Equalf(b.L1Origin.Hash, info.BlockHash, "L2 block %d L1-info block hash must match its origin", n)

		// Structural guard: Arsia L2 headers must not absorb L1-only fields.
		require.Nilf(l2Info.Header().BlockAccessListHash, "L2 block %d must not carry a BAL hash", n)
		require.Nilf(l2Info.Header().SlotNumber, "L2 block %d must not carry a SlotNumber", n)
		checked++
	}
	require.GreaterOrEqual(checked, 2, "must have checked at least two post-Amsterdam-origin L2 blocks")
	t.Log("post-Amsterdam L1 headers flow into L2 attributes without pollution", "checked", checked)
}
