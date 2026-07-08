package derivblob

import (
	"math/big"
	"testing"
	"time"

	opforks "github.com/ethereum-optimism/optimism/op-core/forks"
	"github.com/ethereum-optimism/optimism/op-devstack/devtest"
	"github.com/ethereum-optimism/optimism/op-devstack/presets"
	"github.com/ethereum-optimism/optimism/op-service/eth"
	suptypes "github.com/ethereum-optimism/optimism/op-supervisor/supervisor/types"
)

// TestDerivation_BlobPathIntact proves that the EIP-4844 blob DA derivation path
// survives the L1 Glamsterdam (Amsterdam EL) upgrade: the batcher keeps posting L2
// batches as type-3 blob txs to the Glamsterdam L1, and the L2 safe head keeps
// advancing by deriving those blobs.
//
// The batcher submits *only* blobs (init_test sets DataAvailabilityType=blobs), so the
// L2 safe head can advance solely by successfully traveling the blob DA path off the
// Glamsterdam L1: fetching blob sidecars, verifying KZG commitments against the versioned
// hashes carried in the type-3 txs, and decoding batches. If the L1 upgrade had broken any
// of that, the batcher would fail to post or the pipeline would stall, and the safe head
// would not advance past the post-Amsterdam blob-carrying L1 origin.
//
// Discriminating on three counts:
//  1. It requires a genuinely post-Amsterdam L1 block that carries the batcher's blob
//     versioned hashes (type-3 txs) *and* the Amsterdam-only header fields, so it fails if
//     the batcher cannot post EIP-4844 blobs to a Glamsterdam L1.
//  2. It anchors a specific L2 block (number AND hash) built on a post-Amsterdam blob-block
//     L1 origin and requires that exact block to reach the SAFE head, so it fails if blob
//     derivation stalls across the boundary OR reconstructs a divergent block (a height-only
//     wait would miss the latter; the hash match catches it).
//  3. It cross-checks that the L1 origin the safe head references is itself post-Amsterdam,
//     so a safe head that only ever derived pre-Amsterdam L1 would not pass.
//
// Meanwhile the L2 stays on its own Mantle fork rules (asserted via IsMantleForkActive) — it
// consumes a Glamsterdam L1 without adopting Amsterdam.
func TestDerivation_BlobPathIntact(gt *testing.T) {
	t := devtest.SerialT(gt)
	sys := presets.NewMantleMinimal(t)
	require := t.Require()
	ctx := t.Ctx()

	rollupCfg := sys.L2Chain.Escape().RollupConfig()
	l1Config := sys.L1Network.Escape().ChainConfig()

	require.True(sys.L2Chain.IsMantleForkActive(opforks.MantleElysium), "L2 must run with Mantle Elysium active")
	require.NotNil(l1Config.AmsterdamTime, "L1 AmsterdamTime must be configured")

	// Drive the L1 all the way to Glamsterdam (Amsterdam EL) so this is genuinely
	// post-Glamsterdam derivation.
	t.Log("Waiting for L1 Amsterdam to activate")
	sys.L1EL.WaitForTime(*l1Config.AmsterdamTime)
	t.Log("L1 Amsterdam activated")

	l1Eth := sys.L1EL.EthClient()
	isAmsterdam := func(info eth.BlockInfo) bool {
		return l1Config.IsAmsterdam(new(big.Int).SetUint64(info.NumberU64()), info.Time())
	}

	// Phase 1: find a recent POST-Amsterdam L1 block that carries the batcher's EIP-4844 blob
	// versioned hashes (type-3 txs). This proves the batcher can still post blob batches to a
	// Glamsterdam L1. Scan the recent window from the head downward so we pick the most recent
	// such block, making "the safe head advances past it" a meaningful liveness requirement.
	var blobBlock eth.BlockInfo
	found := false
	deadline := time.Now().Add(120 * time.Second)
	for !found && time.Now().Before(deadline) {
		head := sys.L1EL.BlockRefByLabel(eth.Unsafe)
		floor := uint64(1)
		if head.Number > 64 {
			floor = head.Number - 64
		}
		for n := head.Number; n >= floor && !found; n-- {
			info, txs, err := l1Eth.InfoAndTxsByNumber(ctx, n)
			require.NoError(err, "read L1 block %d", n)
			if !isAmsterdam(info) {
				continue
			}
			blobCount := 0
			for _, tx := range txs {
				blobCount += len(tx.BlobHashes())
			}
			if blobCount > 0 {
				blobBlock = info
				found = true
			}
		}
		if !found {
			select {
			case <-time.After(time.Second):
			case <-ctx.Done():
				require.Fail("context cancelled before a post-Amsterdam blob batch appeared on L1")
			}
		}
	}
	require.True(found, "batcher must post an EIP-4844 blob batch to a post-Amsterdam L1 within the deadline")

	// The blob block must be genuinely post-Amsterdam: carry the Amsterdam-only header fields.
	blobHeader := blobBlock.Header()
	require.NotNil(blobHeader.BlockAccessListHash, "post-Amsterdam blob-carrying L1 block must carry BlockAccessListHash")
	require.NotNil(blobHeader.SlotNumber, "post-Amsterdam blob-carrying L1 block must carry SlotNumber")
	t.Log("found post-Amsterdam L1 block carrying batcher blobs", "number", blobBlock.NumberU64(), "time", blobBlock.Time())

	// Phase 2: anchor a SPECIFIC L2 block and prove IT — matched by number AND hash — was
	// reconstructed byte-identically from the blob DA path. Capture an unsafe L2 block whose
	// L1 origin is at/after the post-Amsterdam blob block, then require that exact block to
	// reach the SAFE head via ReachedRef (which matches the hash, unlike a height-only wait).
	// Because the batcher posts ONLY blobs, a block can become safe solely by fetching blob
	// sidecars off the Glamsterdam L1, verifying KZG commitments against the versioned hashes,
	// and decoding batches; matching the hash proves the reconstruction is byte-identical, not
	// merely that the safe height advanced (which a divergent re-derivation would also satisfy).
	l2BlockTime := time.Duration(rollupCfg.BlockTime) * time.Second
	var target eth.L2BlockRef
	require.Eventually(func() bool {
		target = sys.L2EL.BlockRefByLabel(eth.Unsafe)
		return target.L1Origin.Number >= blobBlock.NumberU64()
	}, 120*time.Second, l2BlockTime, "an L2 unsafe block must build on a post-Amsterdam blob-block L1 origin")

	sys.L2CL.ReachedRef(suptypes.CrossSafe, eth.BlockID{Number: target.Number, Hash: target.Hash}, 120)
	t.Log("L2 block reconstructed byte-identically from the blob DA path across the Glamsterdam boundary",
		"l2", target.Number, "l1Origin", target.L1Origin.Number)

	// Phase 3: cross-check the reconstructed block's L1 origin is itself post-Amsterdam and
	// carries the Amsterdam-only header fields, so the round-trip genuinely spanned a Glamsterdam L1.
	originInfo, _, err := l1Eth.InfoAndTxsByHash(ctx, target.L1Origin.Hash)
	require.NoError(err, "L1 origin of the reconstructed safe L2 block must exist on L1")
	require.True(isAmsterdam(originInfo), "the L1 origin referenced by the reconstructed L2 block must be post-Amsterdam")
	originHeader := originInfo.Header()
	require.NotNil(originHeader.BlockAccessListHash, "post-Amsterdam L2 safe-head L1 origin must carry BlockAccessListHash")
	require.NotNil(originHeader.SlotNumber, "post-Amsterdam L2 safe-head L1 origin must carry SlotNumber")
	t.Log("reconstructed L2 block's L1 origin is a Glamsterdam block",
		"safeOrigin", originInfo.NumberU64(), "blobBlock", blobBlock.NumberU64())
}
