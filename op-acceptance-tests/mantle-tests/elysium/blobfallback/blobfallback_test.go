package blobfallback

import (
	"context"
	"fmt"
	"math/big"
	"os"
	"testing"
	"time"

	"github.com/ethereum-optimism/optimism/op-service/apis"
	"github.com/ethereum-optimism/optimism/op-service/client"
	"github.com/ethereum-optimism/optimism/op-service/eth"
	"github.com/ethereum-optimism/optimism/op-service/sources"
	"github.com/ethereum-optimism/optimism/op-service/testlog"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/ethereum/go-ethereum/log"
	"github.com/stretchr/testify/require"
)

// These tests drive op-node's blob-fetch client against a REAL post-Gloas L1 CL (Prysm/Lighthouse),
// exercising the /blobs -> /blob_sidecars fallback that fakebeacon (a fork-agnostic mock) cannot
// meaningfully test. They complement realclblob (which checks the primary blob fetch) by forcing
// the /blobs endpoint down so the fallback is exercised against the real beacon.
//
// Not CI tests: they skip unless L1_EL_URL + L1_BEACON_URL point at a real post-Gloas geth+beacon
// (same convention as realclblob). On a post-Gloas Prysm the /blob_sidecars endpoint may itself be
// broken (500); if the forced fallback then fails, that red IS the finding — op-node has no
// data_column_sidecars fallback, so Mantle must keep the primary /blobs path working.

// blobsEndpointDown wraps a BeaconClient and forces /blobs to fail, so GetBlobs must fall back to
// /blob_sidecars. Every other method passes through to the real client.
type blobsEndpointDown struct {
	apis.BeaconClient
}

func (blobsEndpointDown) BeaconBlobs(_ context.Context, _ uint64, _ []eth.IndexedBlobHash) (eth.APIBeaconBlobsResponse, error) {
	return eth.APIBeaconBlobsResponse{}, fmt.Errorf("injected fault: /blobs endpoint is down")
}

// realCLEnv reads the real-CL endpoints, skipping if unset.
func realCLEnv(t *testing.T) (elURL, beaconURL string) {
	elURL = os.Getenv("L1_EL_URL")
	beaconURL = os.Getenv("L1_BEACON_URL")
	if elURL == "" || beaconURL == "" {
		t.Skip("set L1_EL_URL and L1_BEACON_URL to a real post-Gloas geth+beacon carrying blobs")
	}
	return elURL, beaconURL
}

// findBlobBlock scans recent L1 blocks for one carrying batcher blobs, returning its ref and the
// indexed blob hashes in block order.
func findBlobBlock(ctx context.Context, t *testing.T, el *ethclient.Client) (eth.L1BlockRef, []eth.IndexedBlobHash) {
	head, err := el.BlockByNumber(ctx, nil)
	require.NoError(t, err, "get L1 head")
	lo := uint64(0)
	if head.NumberU64() > 64 {
		lo = head.NumberU64() - 64
	}
	for n := head.NumberU64(); n > lo; n-- {
		blk, err := el.BlockByNumber(ctx, new(big.Int).SetUint64(n))
		require.NoError(t, err)
		idx := uint64(0)
		var hashes []eth.IndexedBlobHash
		for _, tx := range blk.Transactions() {
			if tx.Type() != types.BlobTxType {
				continue
			}
			for _, h := range tx.BlobHashes() {
				hashes = append(hashes, eth.IndexedBlobHash{Index: idx, Hash: h})
				idx++
			}
		}
		if len(hashes) > 0 {
			return eth.L1BlockRef{
				Hash:       blk.Hash(),
				Number:     blk.NumberU64(),
				ParentHash: blk.ParentHash(),
				Time:       blk.Time(),
			}, hashes
		}
	}
	require.FailNow(t, "no L1 block with blobs in the last 64 blocks — is the blob spammer running and past Gloas?")
	return eth.L1BlockRef{}, nil
}

// TestL1Beacon_BlobSidecarFallback_RealCL forces the real beacon's /blobs endpoint down and
// requires GetBlobs to fall back to /blob_sidecars and still return the correct blobs (KZG
// commitment matching the versioned hash, in the requested order).
func TestL1Beacon_BlobSidecarFallback_RealCL(t *testing.T) {
	elURL, beaconURL := realCLEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	logger := testlog.Logger(t, log.LevelInfo)

	el, err := ethclient.DialContext(ctx, elURL)
	require.NoError(t, err, "dial L1 EL")
	defer el.Close()
	ref, hashes := findBlobBlock(ctx, t, el)
	t.Logf("found L1 block %d carrying %d blobs", ref.Number, len(hashes))

	realBeacon := sources.NewBeaconHTTPClient(client.NewBasicHTTPClient(beaconURL, logger))
	down := sources.NewL1BeaconClient(blobsEndpointDown{realBeacon}, sources.L1BeaconClientConfig{})

	blobs, err := down.GetBlobs(ctx, ref, hashes)
	require.NoError(t, err, "GetBlobs must succeed via the /blob_sidecars fallback when /blobs is down")
	require.Len(t, blobs, len(hashes), "the fallback must return exactly the requested blobs")
	for i, blob := range blobs {
		require.NotNilf(t, blob, "fallback blob %d must be present", i)
		commitment, err := blob.ComputeKZGCommitment()
		require.NoErrorf(t, err, "fallback blob %d KZG commitment", i)
		require.Equalf(t, hashes[i].Hash, eth.KZGToVersionedHash(commitment),
			"fallback blob %d must match its versioned hash", i)
	}
}

// TestL1Beacon_BlobOrderConsistency_RealCL requires the real beacon's main /blobs path and its
// /blob_sidecars fallback to return BYTE-IDENTICAL blobs, in the same order, for the same hashes.
func TestL1Beacon_BlobOrderConsistency_RealCL(t *testing.T) {
	elURL, beaconURL := realCLEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	logger := testlog.Logger(t, log.LevelInfo)

	el, err := ethclient.DialContext(ctx, elURL)
	require.NoError(t, err, "dial L1 EL")
	defer el.Close()
	ref, hashes := findBlobBlock(ctx, t, el)
	require.GreaterOrEqual(t, len(hashes), 1, "need at least one blob to compare")

	realBeacon := sources.NewBeaconHTTPClient(client.NewBasicHTTPClient(beaconURL, logger))
	mainPath := sources.NewL1BeaconClient(realBeacon, sources.L1BeaconClientConfig{})                    // /blobs
	fallback := sources.NewL1BeaconClient(blobsEndpointDown{realBeacon}, sources.L1BeaconClientConfig{}) // /blob_sidecars

	mainBlobs, err := mainPath.GetBlobs(ctx, ref, hashes)
	require.NoError(t, err, "main-path GetBlobs (/blobs) must succeed against the real beacon")
	fbBlobs, err := fallback.GetBlobs(ctx, ref, hashes)
	require.NoError(t, err, "fallback GetBlobs (/blob_sidecars) must succeed against the real beacon")
	require.Len(t, mainBlobs, len(hashes))
	require.Len(t, fbBlobs, len(hashes))
	for i := range hashes {
		require.NotNilf(t, mainBlobs[i], "main-path blob %d must be present", i)
		require.NotNilf(t, fbBlobs[i], "fallback blob %d must be present", i)
		require.Truef(t, *mainBlobs[i] == *fbBlobs[i],
			"blob %d must be byte-identical between the main /blobs path and the /blob_sidecars fallback", i)
	}
}
