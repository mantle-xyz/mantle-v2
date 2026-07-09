package sequenceractive

import (
	"math/big"
	"testing"

	opforks "github.com/ethereum-optimism/optimism/op-core/forks"
	"github.com/ethereum-optimism/optimism/op-devstack/devtest"
	"github.com/ethereum-optimism/optimism/op-devstack/presets"
	"github.com/ethereum-optimism/optimism/op-service/eth"
)

// TestBoundary_SequencerActiveAcrossUpgrade proves the L2 sequencer keeps producing blocks and
// ingesting L1 headers in real time across the L1 Glamsterdam (Amsterdam) activation, without
// stalling at the exact upgrade instant. The Mantle L2 stays on Arsia.
//
// The discriminating signal is the sequencer's UNSAFE L1 origin: a sequencer that choked on the
// first Amsterdam header would keep sealing unsafe blocks pinned to a stale pre-Amsterdam origin
// and its origin would never advance past the activation block. This asserts the opposite:
//
//  1. the unsafe L1 origin advances PAST the activation block (real-time ingestion, no stall);
//  2. the unsafe block that opened the activation epoch anchors BY HASH to the genuine activation
//     L1 block (the sequencer consumed the real Amsterdam block, not a divergent one);
//  3. that L1 origin is genuinely Glamsterdam (BAL + SlotNumber), yet the L2 block the sequencer
//     produced from it stays Arsia (no header-field leak).
//
// Flips red if the sequencer stalls at the boundary (origin never crosses -> timeout), consumes a
// divergent activation block (hash mismatch), or leaks an Amsterdam header field onto the L2.
func TestBoundary_SequencerActiveAcrossUpgrade(gt *testing.T) {
	t := devtest.SerialT(gt)
	sys := presets.NewMantleMinimal(t)
	require := t.Require()
	ctx := t.Ctx()

	l1Config := sys.L1Network.Escape().ChainConfig()
	require.True(sys.L2Chain.IsMantleForkActive(opforks.MantleElysium), "L2 must run with Mantle Elysium active")
	require.NotNil(l1Config.AmsterdamTime, "L1 AmsterdamTime must be configured")

	// 1) Cross Amsterdam and locate the activation L1 block A (first post-Amsterdam L1 block).
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
	aRef := sys.L1EL.BlockRefByNumber(activation)

	preHead := sys.L2EL.BlockRefByLabel(eth.Unsafe)

	// 2) DISCRIMINATING: the sequencer's unsafe L1 origin must advance PAST the activation block.
	//    A stall on the first Amsterdam header would pin the origin below it and this would time out.
	crossed := sys.L2EL.WaitForUnsafe(func(bi eth.BlockInfo) (bool, error) {
		return sys.L2EL.BlockRefByLabel(eth.Unsafe).L1Origin.Number > activation, nil
	})
	require.Greater(crossed.NumberU64(), preHead.Number,
		"the sequencer must keep producing unsafe blocks while crossing the upgrade")

	// 3) The unsafe block that OPENED the activation epoch must anchor by hash to the genuine
	//    activation L1 block. Scan downward for the lowest unsafe block with origin == activation.
	head := sys.L2EL.BlockRefByLabel(eth.Unsafe).Number
	var openerA eth.L2BlockRef
	found := false
	for n := head; n > 0; n-- {
		b := sys.L2EL.BlockRefByNumber(n)
		if b.L1Origin.Number == activation {
			openerA = b
			found = true
		} else if found && b.L1Origin.Number < activation {
			break
		}
	}
	require.True(found, "the sequencer's unsafe chain must open an L2 epoch at the activation L1 block")
	require.Equal(aRef.Hash, openerA.L1Origin.Hash,
		"the activation epoch opener must anchor to the genuine activation L1 block by hash")

	// 4) That origin is genuinely Glamsterdam (BAL + SlotNumber), yet the L2 block the sequencer
	//    produced from it stays Arsia — the sequencer ingested a real Amsterdam header without leak.
	aInfo, _, err := sys.L1EL.Escape().EthClient().InfoAndTxsByHash(ctx, openerA.L1Origin.Hash)
	require.NoError(err, "must read the activation L1 origin")
	require.NotNil(aInfo.Header().BlockAccessListHash, "activation L1 origin must carry a BAL hash (genuine Glamsterdam)")
	require.NotNil(aInfo.Header().SlotNumber, "activation L1 origin must carry a SlotNumber (genuine Glamsterdam)")

	l2Info, _, err := sys.L2EL.Escape().EthClient().InfoAndTxsByHash(ctx, openerA.Hash)
	require.NoError(err, "must read the activation epoch opener L2 block")
	require.Nil(l2Info.Header().BlockAccessListHash, "the L2 block opening the activation epoch must stay Arsia (no BAL)")
	require.Nil(l2Info.Header().SlotNumber, "the L2 block opening the activation epoch must stay Arsia (no SlotNumber)")
	t.Log("sequencer ingested the activation block and kept producing", "activation", activation, "openerL2", openerA.Number, "unsafe", head)
}
