package preactivation

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

// TestBoundary_L1PreActivationBlock is the mirror of
// TestBoundary_L1ActivationBlock: it pins the last pre-Amsterdam L1 block and
// proves the L2 derives a safe block from that legacy origin.
//
// The activation height is discovered dynamically. The parent must be
// pre-Amsterdam with no BAL/SlotNumber fields, the activation child must carry
// both fields, and the safe L2 block must anchor to the parent by hash.
func TestBoundary_L1PreActivationBlock(gt *testing.T) {
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
		"Amsterdam must activate above L1 genesis (offset > 0) so a real pre-activation parent block exists")

	// The pre-activation block is the block immediately before activation.
	preActivation := activation - 1
	preRef := sys.L1EL.BlockRefByNumber(preActivation)

	// (a) The pre-activation block is the last legacy block.
	require.False(l1Config.IsAmsterdam(new(big.Int).SetUint64(preActivation), preRef.Time),
		"the block before the activation block must be pre-Amsterdam — it is the last legacy (old-format) L1 block")

	// (b) The legacy header must carry neither Amsterdam field.
	preHash := preRef.Hash
	preInfo, _, err := sys.L1EL.EthClient().InfoAndTxsByHash(ctx, preHash)
	require.NoError(err, "must read the typed pre-Amsterdam L1 header")
	preHdr := preInfo.Header()
	require.Nil(preHdr.BlockAccessListHash,
		"the last pre-Amsterdam L1 block must be old-format — no EIP-7928 BlockAccessListHash")
	require.Nil(preHdr.SlotNumber,
		"the last pre-Amsterdam L1 block must be old-format — no EIP-7843 SlotNumber")
	t.Log("Last pre-Amsterdam L1 block located", "number", preActivation, "hash", preHash)

	// Contrast: the activation child carries both Amsterdam fields.
	activationHash := sys.L1EL.BlockRefByNumber(activation).Hash
	actInfo, _, err := sys.L1EL.EthClient().InfoAndTxsByHash(ctx, activationHash)
	require.NoError(err, "must read the typed Amsterdam activation L1 header")
	actHdr := actInfo.Header()
	require.NotNil(actHdr.BlockAccessListHash,
		"the Amsterdam activation L1 block (pre-activation's child) must carry an EIP-7928 BlockAccessListHash — new format at the boundary")
	require.NotNil(actHdr.SlotNumber,
		"the Amsterdam activation L1 block (pre-activation's child) must carry an EIP-7843 SlotNumber — new format at the boundary")
	require.Equal(preHash, actHdr.ParentHash,
		"the activation block must be the direct child of the pre-activation block — the boundary is this single block edge")

	// Once the safe head is beyond preActivation, blocks anchored there are fully derived.
	l2BlockTime := time.Duration(rollupCfg.BlockTime) * time.Second
	for {
		safe := sys.L2CL.SyncStatus().SafeL2
		if safe.L1Origin.Number > preActivation {
			break
		}
		t.Log("L2 safe head not yet derived past the pre-Amsterdam origin, waiting...",
			"safeOrigin", safe.L1Origin.Number, "preActivation", preActivation)
		select {
		case <-time.After(l2BlockTime):
		case <-ctx.Done():
			require.Fail("L2 never derived a safe block past the pre-Amsterdam L1 origin")
		}
	}

	// Walk down the safe chain to find the block anchored at preActivation.
	safeHead := sys.L2CL.SyncStatus().SafeL2
	var l2AtPre eth.L2BlockRef
	foundL2 := false
	for n := safeHead.Number; ; n-- {
		ref := sys.L2EL.BlockRefByNumber(n)
		if ref.L1Origin.Number == preActivation {
			l2AtPre = ref
			foundL2 = true
			break
		}
		if ref.L1Origin.Number < preActivation || n == 0 {
			break
		}
	}

	// A safe L2 block whose L1 origin is exactly the pre-activation block exists: the L2
	// derives fine from the last legacy L1 origin.
	require.True(foundL2,
		"a safe L2 block whose L1 origin is the last pre-Amsterdam block must exist — derivation must not stall on the legacy origin")
	require.Equal(preActivation, l2AtPre.L1Origin.Number,
		"the located L2 block's L1 origin must be the pre-activation block number")
	require.Equal(preHash, l2AtPre.L1Origin.Hash,
		"the located L2 block's L1 origin hash must be the pre-activation block hash (same block, not a namesake)")
	// No separate l2AtPre.Number <= safeHead.Number check: the scan starts at the safe head.
	t.Log("L2 block derived from the pre-Amsterdam L1 origin",
		"l2", l2AtPre.Number, "l1Origin", l2AtPre.L1Origin.Number)
}
