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

// TestDerivation_BlobPathIntact proves blob DA derivation survives the L1
// Amsterdam upgrade while the L2 stays on Mantle Elysium/Arsia rules.
//
// The batcher is configured for blobs only, so a target L2 block can reach safe
// only if the batcher posts EIP-4844 blob txs to the post-Amsterdam L1 and
// op-node derives the exact block back by number and hash.
func TestDerivation_BlobPathIntact(gt *testing.T) {
	t := devtest.SerialT(gt)
	sys := presets.NewMantleMinimal(t)
	require := t.Require()
	ctx := t.Ctx()

	rollupCfg := sys.L2Chain.Escape().RollupConfig()
	l1Config := sys.L1Network.Escape().ChainConfig()

	require.True(sys.L2Chain.IsMantleForkActive(opforks.MantleElysium), "L2 must run with Mantle Elysium active")
	require.NotNil(l1Config.AmsterdamTime, "L1 AmsterdamTime must be configured")

	// Drive the L1 to Amsterdam so this is genuinely post-fork derivation.
	t.Log("Waiting for L1 Amsterdam to activate")
	sys.L1EL.WaitForTime(*l1Config.AmsterdamTime)
	t.Log("L1 Amsterdam activated")

	l1Eth := sys.L1EL.EthClient()
	isAmsterdam := func(info eth.BlockInfo) bool {
		return l1Config.IsAmsterdam(new(big.Int).SetUint64(info.NumberU64()), info.Time())
	}

	// Phase 1: find a recent post-Amsterdam L1 block carrying batcher blob hashes.
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

	// Phase 2: require one specific L2 block to be re-derived by number and hash.
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
