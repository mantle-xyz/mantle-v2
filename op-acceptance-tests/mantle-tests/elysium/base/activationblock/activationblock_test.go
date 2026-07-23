package activationblock

import (
	"math/big"
	"testing"
	"time"

	"github.com/ethereum-optimism/optimism/op-acceptance-tests/mantle-tests/elysium/internal/testhelpers"
	"github.com/ethereum-optimism/optimism/op-acceptance-tests/mantle-tests/elysium/internal/testmain"
	opforks "github.com/ethereum-optimism/optimism/op-core/forks"
	"github.com/ethereum-optimism/optimism/op-devstack/devtest"
	"github.com/ethereum-optimism/optimism/op-devstack/presets"
	"github.com/ethereum-optimism/optimism/op-service/eth"
)

// TestBoundary_L1ActivationBlock proves the Mantle L2 derives a safe block from
// the exact first Amsterdam L1 block while keeping Arsia L2 header rules.
//
// The activation height is discovered dynamically because the fork offset is in
// seconds, not blocks. The test confirms the activation block carries the new
// BAL/SlotNumber fields, its parent is pre-Amsterdam, and the safe L2 block
// anchors to the activation block by L1-origin hash.
func TestBoundary_L1ActivationBlock(gt *testing.T) {
	t := devtest.SerialT(gt)
	sys := presets.NewMantleMinimal(t)
	require := t.Require()
	ctx := t.Ctx()

	rollupCfg := sys.L2Chain.Escape().RollupConfig()
	l1Config := sys.L1Network.Escape().ChainConfig()

	require.True(sys.L2Chain.IsMantleForkActive(opforks.MantleElysium),
		"L2 must run with Mantle Elysium active (Arsia EL header rules)")
	require.NotNil(rollupCfg.MantleElysiumTime, "MantleElysiumTime must be configured")
	require.NotNil(l1Config.AmsterdamTime, "L1 AmsterdamTime must be configured")

	// Wait for the L1 to reach/cross the Amsterdam activation time.
	t.Log("Waiting for L1 Amsterdam to activate")
	testhelpers.WaitForGlamsterdamL1(t, sys.L1EL, *l1Config.AmsterdamTime)
	t.Log("L1 Amsterdam activated")

	// Find the first block for which IsAmsterdam(num, time) holds.
	l1Head := sys.L1EL.BlockRefByLabel(eth.Unsafe).Number
	var activation uint64
	foundActivation := false
	for n := uint64(0); n <= l1Head; n++ {
		ref := sys.L1EL.BlockRefByNumber(n)
		if l1Config.IsAmsterdam(new(big.Int).SetUint64(n), ref.Time) {
			activation = n
			foundActivation = true
			break
		}
	}
	require.True(foundActivation, "must find the first Amsterdam L1 block at or below the current L1 head")
	require.Greater(activation, uint64(0),
		"Amsterdam must activate above L1 genesis (offset > 0) so the activation block is a real post-genesis block")

	// The parent being pre-Amsterdam proves activation is the exact boundary.
	parent := sys.L1EL.BlockRefByNumber(activation - 1)
	require.False(l1Config.IsAmsterdam(new(big.Int).SetUint64(activation-1), parent.Time),
		"the block before the activation block must be pre-Amsterdam — activation must be the exact boundary")

	// The boundary block must genuinely carry the new Amsterdam header fields.
	activationHash := sys.L1EL.BlockRefByNumber(activation).Hash
	l1Info, _, err := sys.L1EL.EthClient().InfoAndTxsByHash(ctx, activationHash)
	require.NoError(err, "must read the typed Amsterdam activation L1 header")
	l1Hdr := l1Info.Header()
	require.NotNil(l1Hdr.BlockAccessListHash,
		"the Amsterdam activation L1 block must carry an EIP-7928 BlockAccessListHash")
	require.NotNil(l1Hdr.SlotNumber,
		"the Amsterdam activation L1 block must carry an EIP-7843 SlotNumber")

	// amsterdamOffset must name a whole slot, or the activation block sits mid-slot and every
	// slot-indexed claim below it is arithmetic about a boundary that does not exist. The two
	// operands are genuinely independent: the offset is a test-side constant in testmain, while
	// SECONDS_PER_SLOT comes from the L1 network config, so raising the L1 block time to 12s
	// makes 30%12=6 and this goes red.
	secondsPerSlot := testhelpers.L1SecondsPerSlot(t, sys)
	require.Zerof(testmain.DefaultAmsterdamOffset%secondsPerSlot,
		"amsterdamOffset=%ds is not a whole number of %ds slots, so it does not name a slot boundary",
		testmain.DefaultAmsterdamOffset, secondsPerSlot)

	// The slot number must be the RIGHT one, not merely present — every other case here only
	// checks the field is non-nil, which a builder emitting a constant would satisfy.
	//
	// Be precise about what this is, because it looks like more than it is. It is NOT a
	// cross-component consistency check between the beacon's slot clock and the builder's: both
	// sides descend from the same l1Net.blockTime and the same genesis timestamp — fakebeacon
	// reports blockTime as SECONDS_PER_SLOT (fakebeacon/blobs.go), fakepos derives slotNumber as
	// elapsed/blockTime (geth/fakepos.go) — so they are two consumers of one config field and
	// cannot disagree about it. Nor is there a compiled-in divergent branch to trip, the way the
	// EIP-gated gas assertions have rules.IsAmsterdam.
	//
	// What it does catch, by pinning the value rather than its shape: a builder emitting a
	// constant slot (unless that constant happens to equal offset/blockTime), and a builder
	// dividing by something other than the slot time it reports. That is the whole list.
	//
	// It notably does NOT catch numbering by block height, which is the most likely wrong
	// formula: at this offset the activation block IS block offset/blockTime, so height and
	// slot are the same number and the two are indistinguishable here. Nor a missed slot,
	// which fakepos cannot produce — it derives each block time from the previous head rather
	// than from a wall clock, so the sequence has no gaps to find.
	//
	// So: a regression guard on the builder's slot numbering, whose only write site is the one
	// line in fakepos — worth keeping at this price, but neither evidence of agreement between
	// two independent sources nor a broad check on the formula.
	expectedSlot := testmain.DefaultAmsterdamOffset / secondsPerSlot
	require.EqualValuesf(expectedSlot, *l1Hdr.SlotNumber,
		"activation L1 block #%d reports slot %d, but %ds after genesis at %ds per slot is slot %d: "+
			"the builder's slot numbering has changed, or a slot was missed before activation",
		activation, *l1Hdr.SlotNumber, testmain.DefaultAmsterdamOffset, secondsPerSlot, expectedSlot)

	t.Log("Amsterdam activation L1 block located", "number", activation, "hash", activationHash,
		"slot", *l1Hdr.SlotNumber)

	// Once the safe head's L1 origin is beyond activation, any L2 block anchored
	// at activation is fully derived.
	l2BlockTime := time.Duration(rollupCfg.BlockTime) * time.Second
	for {
		safe := sys.L2CL.SyncStatus().SafeL2
		if safe.L1Origin.Number > activation {
			break
		}
		t.Log("L2 safe head not yet derived past the Amsterdam activation origin, waiting...",
			"safeOrigin", safe.L1Origin.Number, "activation", activation)
		select {
		case <-time.After(l2BlockTime):
		case <-ctx.Done():
			require.Fail("L2 never derived a safe block past the Amsterdam activation L1 origin")
		}
	}

	// Walk down the safe chain to find the block anchored at activation.
	safeHead := sys.L2CL.SyncStatus().SafeL2
	var l2AtActivation eth.L2BlockRef
	foundL2 := false
	for n := safeHead.Number; ; n-- {
		ref := sys.L2EL.BlockRefByNumber(n)
		if ref.L1Origin.Number == activation {
			l2AtActivation = ref
			foundL2 = true
			break
		}
		if ref.L1Origin.Number < activation || n == 0 {
			break
		}
	}

	// A safe L2 block whose L1 origin is exactly the activation block exists.
	require.True(foundL2,
		"a safe L2 block whose L1 origin is the Amsterdam activation block must exist — derivation must not choke at the exact boundary")
	require.Equal(activation, l2AtActivation.L1Origin.Number,
		"the located L2 block's L1 origin must be the activation block number")
	require.Equal(activationHash, l2AtActivation.L1Origin.Hash,
		"the located L2 block's L1 origin hash must be the activation block hash (same block, not a namesake)")
	// The reverse scan starts from the safe head, so safety comes from where the
	// block was found rather than a redundant height check.
	t.Log("L2 block derived from the Amsterdam activation L1 origin",
		"l2", l2AtActivation.Number, "l1Origin", l2AtActivation.L1Origin.Number)
}
