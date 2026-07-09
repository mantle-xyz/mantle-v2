package blobfallback

import (
	"context"
	"fmt"
	"math/big"
	"testing"
	"time"

	opforks "github.com/ethereum-optimism/optimism/op-core/forks"
	"github.com/ethereum-optimism/optimism/op-devstack/devtest"
	"github.com/ethereum-optimism/optimism/op-devstack/presets"
	"github.com/ethereum-optimism/optimism/op-devstack/stack/match"
	"github.com/ethereum-optimism/optimism/op-service/apis"
	"github.com/ethereum-optimism/optimism/op-service/eth"
	"github.com/ethereum-optimism/optimism/op-service/sources"
)

// blobsEndpointDown wraps a BeaconClient and forces the post-Fulu /blobs endpoint to fail, so that
// L1BeaconClient.GetBlobs must fall back to /blob_sidecars. Every other method (including
// BeaconBlobSideCars, ConfigSpec, BeaconGenesis) passes through to the real client.
type blobsEndpointDown struct {
	apis.BeaconClient
}

func (blobsEndpointDown) BeaconBlobs(_ context.Context, _ uint64, _ []eth.IndexedBlobHash) (eth.APIBeaconBlobsResponse, error) {
	return eth.APIBeaconBlobsResponse{}, fmt.Errorf("injected fault: /blobs endpoint is down")
}

// findPostAmsterdamBlobBlock crosses Amsterdam and returns a post-Amsterdam L1 block carrying
// batcher blobs together with its indexed blob hashes in block order.
func findPostAmsterdamBlobBlock(t devtest.T, sys *presets.MantleMinimal) (eth.L1BlockRef, []eth.IndexedBlobHash) {
	require := t.Require()
	ctx := t.Ctx()
	l1Config := sys.L1Network.Escape().ChainConfig()
	require.NotNil(l1Config.AmsterdamTime, "L1 AmsterdamTime must be configured")

	sys.L1EL.WaitForTime(*l1Config.AmsterdamTime)

	l1Eth := sys.L1EL.EthClient()
	var (
		ref    eth.L1BlockRef
		hashes []eth.IndexedBlobHash
	)
	require.Eventually(func() bool {
		head := sys.L1EL.BlockRefByLabel(eth.Unsafe)
		floor := uint64(1)
		if head.Number > 64 {
			floor = head.Number - 64
		}
		for n := head.Number; n >= floor; n-- {
			info, txs, err := l1Eth.InfoAndTxsByNumber(ctx, n)
			require.NoErrorf(err, "read L1 block %d", n)
			if !l1Config.IsAmsterdam(new(big.Int).SetUint64(info.NumberU64()), info.Time()) {
				continue
			}
			idx := uint64(0)
			var hs []eth.IndexedBlobHash
			for _, tx := range txs {
				for _, h := range tx.BlobHashes() {
					hs = append(hs, eth.IndexedBlobHash{Index: idx, Hash: h})
					idx++
				}
			}
			if len(hs) > 0 {
				ref = sys.L1EL.BlockRefByNumber(n)
				hashes = hs
				return true
			}
		}
		return false
	}, 120*time.Second, 2*time.Second, "a post-Amsterdam L1 block carrying batcher blobs must appear")

	return ref, hashes
}

// TestL1Beacon_BlobSidecarFallback_PostGlamsterdam proves op-node's blob fetch survives a failing
// post-Fulu /blobs endpoint after the L1 upgrades to Glamsterdam: with /blobs forced down via an
// injected fault, GetBlobs must fall back to /blob_sidecars and still return the correct blobs
// (each blob's KZG commitment matches its versioned hash, in the requested order).
//
// Flips red if the fallback path is broken (GetBlobs errors) or returns wrong/misordered blobs.
func TestL1Beacon_BlobSidecarFallback_PostGlamsterdam(gt *testing.T) {
	t := devtest.SerialT(gt)
	sys := presets.NewMantleMinimal(t)
	require := t.Require()
	ctx := t.Ctx()
	require.True(sys.L2Chain.IsMantleForkActive(opforks.MantleElysium), "L2 must run with Mantle Elysium active")

	ref, hashes := findPostAmsterdamBlobBlock(t, sys)
	t.Log("found post-Amsterdam blob block", "block", ref.Number, "blobs", len(hashes))

	l1CL := sys.L1Network.Escape().L1CLNode(match.Assume(t, match.FirstL1CL))
	// /blobs forced down -> GetBlobs must fall back to /blob_sidecars.
	down := sources.NewL1BeaconClient(blobsEndpointDown{l1CL.BeaconClient()}, sources.L1BeaconClientConfig{})

	blobs, err := down.GetBlobs(ctx, ref, hashes)
	require.NoError(err, "GetBlobs must succeed via the /blob_sidecars fallback when /blobs is down")
	require.Len(blobs, len(hashes), "the fallback must return exactly the requested blobs")
	for i, blob := range blobs {
		require.NotNilf(blob, "fallback blob %d must be present", i)
		commitment, err := blob.ComputeKZGCommitment()
		require.NoErrorf(err, "fallback blob %d KZG commitment", i)
		require.Equalf(hashes[i].Hash, eth.KZGToVersionedHash(commitment),
			"fallback blob %d must match its versioned hash (correct blob, correct order)", i)
	}
	t.Log("GetBlobs fell back to /blob_sidecars and returned valid blobs", "blobs", len(blobs))
}

// TestL1Beacon_BlobOrderConsistency_PostGlamsterdam proves the main /blobs path and the
// /blob_sidecars fallback return BYTE-IDENTICAL blobs, in the same order, for the same set of
// hashes — so a client that falls back mid-flight cannot silently reorder or corrupt blobs.
//
// Flips red if the two paths disagree on any blob's bytes or ordering.
func TestL1Beacon_BlobOrderConsistency_PostGlamsterdam(gt *testing.T) {
	t := devtest.SerialT(gt)
	sys := presets.NewMantleMinimal(t)
	require := t.Require()
	ctx := t.Ctx()
	require.True(sys.L2Chain.IsMantleForkActive(opforks.MantleElysium), "L2 must run with Mantle Elysium active")

	ref, hashes := findPostAmsterdamBlobBlock(t, sys)
	require.GreaterOrEqual(len(hashes), 1, "need at least one blob to compare")

	l1CL := sys.L1Network.Escape().L1CLNode(match.Assume(t, match.FirstL1CL))
	realCl := l1CL.BeaconClient()
	mainPath := sources.NewL1BeaconClient(realCl, sources.L1BeaconClientConfig{})                    // uses /blobs
	fallback := sources.NewL1BeaconClient(blobsEndpointDown{realCl}, sources.L1BeaconClientConfig{}) // uses /blob_sidecars

	mainBlobs, err := mainPath.GetBlobs(ctx, ref, hashes)
	require.NoError(err, "main-path GetBlobs (/blobs) must succeed")
	fbBlobs, err := fallback.GetBlobs(ctx, ref, hashes)
	require.NoError(err, "fallback GetBlobs (/blob_sidecars) must succeed")
	require.Len(mainBlobs, len(hashes), "main path must return all blobs")
	require.Len(fbBlobs, len(hashes), "fallback must return all blobs")

	for i := range hashes {
		require.NotNilf(mainBlobs[i], "main-path blob %d must be present", i)
		require.NotNilf(fbBlobs[i], "fallback blob %d must be present", i)
		require.Truef(*mainBlobs[i] == *fbBlobs[i],
			"blob %d must be byte-identical between the main /blobs path and the /blob_sidecars fallback (no order mismatch)", i)
	}
	t.Log("main path and fallback returned byte-identical blobs in the same order", "blobs", len(hashes))
}
