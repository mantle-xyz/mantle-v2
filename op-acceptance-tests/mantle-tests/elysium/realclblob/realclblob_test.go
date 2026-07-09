package realclblob

import (
	"context"
	"math/big"
	"os"
	"testing"
	"time"

	"github.com/ethereum-optimism/optimism/op-service/client"
	"github.com/ethereum-optimism/optimism/op-service/eth"
	"github.com/ethereum-optimism/optimism/op-service/sources"
	"github.com/ethereum-optimism/optimism/op-service/testlog"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/ethereum/go-ethereum/log"
	"github.com/stretchr/testify/require"
)

// TestOpNodeFetchesBlobsFromRealCL is the Gloas real-CL check that the sysgo/fakebeacon
// suite structurally cannot do. It points OUR op-node's L1BeaconClient (the exact code
// under test, compiled from this branch) at a REAL post-Gloas beacon (Prysm/Lighthouse)
// and asserts it can fetch the blobs of a real L1 block.
//
// This is the ISOLATED operation the Gloas beacon-API quirk breaks: post-Gloas the
// blob_kzg_commitments moved out of the block body (ePBS), so Prysm's blob_sidecars
// endpoint returns 500 and Lighthouse's /blobs + /blob_sidecars return 400 for blocks
// that DO carry blobs; op-node has no data_column_sidecars fallback (its GetBlobs only
// falls /blobs -> /blob_sidecars). fakebeacon cannot reproduce this because it is a
// fork-agnostic mock. If op-node cannot fetch, GetBlobs errors and this test goes red.
//
// It needs a running real geth+beacon L1 with blobs, post-Gloas (e.g. rde running the
// glamsterdam-l1only profile with the L1 blob spammer). Set L1_EL_URL + L1_BEACON_URL.
// It skips otherwise, so it is not a CI test.
func TestOpNodeFetchesBlobsFromRealCL(t *testing.T) {
	elURL := os.Getenv("L1_EL_URL")
	beaconURL := os.Getenv("L1_BEACON_URL")
	if elURL == "" || beaconURL == "" {
		t.Skip("set L1_EL_URL and L1_BEACON_URL to a real post-Gloas geth+beacon carrying blobs")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	logger := testlog.Logger(t, log.LevelInfo)

	// 1) Find a recent L1 block that carries blobs, and collect its indexed blob hashes.
	el, err := ethclient.DialContext(ctx, elURL)
	require.NoError(t, err, "dial L1 EL")
	defer el.Close()

	head, err := el.BlockByNumber(ctx, nil)
	require.NoError(t, err, "get L1 head")

	var (
		ref    eth.L1BlockRef
		hashes []eth.IndexedBlobHash
	)
	lo := uint64(0)
	if head.NumberU64() > 64 {
		lo = head.NumberU64() - 64
	}
	for n := head.NumberU64(); n > lo; n-- {
		blk, err := el.BlockByNumber(ctx, new(big.Int).SetUint64(n))
		require.NoError(t, err)
		idx := uint64(0)
		var found []eth.IndexedBlobHash
		for _, tx := range blk.Transactions() {
			if tx.Type() != types.BlobTxType {
				continue
			}
			for _, h := range tx.BlobHashes() {
				found = append(found, eth.IndexedBlobHash{Index: idx, Hash: h})
				idx++
			}
		}
		if len(found) > 0 {
			ref = eth.L1BlockRef{
				Hash:       blk.Hash(),
				Number:     blk.NumberU64(),
				ParentHash: blk.ParentHash(),
				Time:       blk.Time(),
			}
			hashes = found
			break
		}
	}
	require.NotEmpty(t, hashes,
		"no L1 block with blobs in the last 64 blocks — is the blob spammer running and is the chain past Gloas?")
	t.Logf("found L1 block %d (t=%d) carrying %d blobs", ref.Number, ref.Time, len(hashes))

	// 2) Build OUR op-node L1 beacon client pointed at the real beacon.
	beaconCl := sources.NewL1BeaconClient(
		sources.NewBeaconHTTPClient(client.NewBasicHTTPClient(beaconURL, logger)),
		sources.L1BeaconClientConfig{},
	)

	// 3) The Gloas check: op-node must fetch those blobs from the real post-Gloas beacon.
	blobs, err := beaconCl.GetBlobs(ctx, ref, hashes)
	require.NoError(t, err,
		"op-node must fetch blobs from the real post-Gloas beacon; a Gloas beacon-API quirk (Prysm 500 / Lighthouse 400, no data_column fallback) breaks this")
	require.Len(t, blobs, len(hashes), "op-node must return every requested blob")
	t.Logf("op-node fetched %d/%d blobs from the real beacon at L1 block %d — the derivation blob path survives the real CL",
		len(blobs), len(hashes), ref.Number)
}
