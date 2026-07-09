package beaconspec

import (
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"testing"
	"time"

	"github.com/ethereum-optimism/optimism/op-devstack/devtest"
	"github.com/ethereum-optimism/optimism/op-devstack/presets"
	"github.com/ethereum-optimism/optimism/op-devstack/stack/match"
	"github.com/ethereum-optimism/optimism/op-service/client"
	"github.com/ethereum-optimism/optimism/op-service/eth"
	"github.com/ethereum-optimism/optimism/op-service/sources"
)

// TestL1Beacon_ConfigSpec_PostGlamsterdam asserts the L1 beacon (fakebeacon) keeps
// serving GET /eth/v1/config/spec with a usable SECONDS_PER_SLOT after the L1 has
// upgraded to Glamsterdam (Amsterdam EL).
//
// op-node/kona derivation reads SECONDS_PER_SLOT from this endpoint to map L1 block
// timestamps to beacon slots when fetching blobs; if it were missing or zero,
// blob-based derivation would break. This is purely an L1-side check — the Mantle L2
// stays on Arsia rules across the boundary, so it neither stops the FakePoS nor
// drives L1.
func TestL1Beacon_ConfigSpec_PostGlamsterdam(gt *testing.T) {
	t := devtest.SerialT(gt)
	sys := presets.NewMantleMinimal(t)
	require := t.Require()
	ctx := t.Ctx()

	// Drive the L1 all the way to Glamsterdam (Amsterdam EL) so the assertion is
	// genuinely "post-Glamsterdam".
	l1Config := sys.L1Network.Escape().ChainConfig()
	require.NotNil(l1Config.AmsterdamTime, "L1 AmsterdamTime must be configured")
	sys.L1EL.WaitForTime(*l1Config.AmsterdamTime)

	// Reach the L1 CL (fakebeacon) exactly the way op-node/kona would: through the
	// beacon HTTP client devstack wires up from L1CLNode.beaconHTTPAddr.
	l1CL := sys.L1Network.Escape().L1CLNode(match.Assume(t, match.FirstL1CL))
	beacon := l1CL.BeaconClient()

	// GET /eth/v1/config/spec -> eth.APIConfigResponse{Data: eth.ReducedConfigData{SecondsPerSlot}}.
	cfg, err := beacon.ConfigSpec(ctx)
	require.NoError(err, "L1 beacon must serve /eth/v1/config/spec after Glamsterdam")

	secondsPerSlot := uint64(cfg.Data.SecondsPerSlot)
	require.Greater(secondsPerSlot, uint64(0), "SECONDS_PER_SLOT must be usable (>0)")

	// Cross-check SECONDS_PER_SLOT against the real L1 block spacing. The fakebeacon
	// serves SECONDS_PER_SLOT = L1 block time, and fakepos advances each L1 block
	// timestamp by exactly the block time in steady state, so two adjacent recent L1
	// blocks pin down the block time without stopping the FakePoS or driving L1.
	head := sys.L1EL.BlockRefByLabel(eth.Unsafe)
	require.Greater(head.Number, uint64(0), "need an L1 block past genesis to determine block time")
	parent := sys.L1EL.BlockRefByNumber(head.Number - 1)
	require.Greater(head.Time, parent.Time, "L1 block timestamps must be strictly increasing")
	l1BlockTime := head.Time - parent.Time

	require.Equal(l1BlockTime, secondsPerSlot, "beacon SECONDS_PER_SLOT must equal the L1 block time")
}

// TestL1Beacon_BlobRoundTrip_PostGlamsterdam proves that SECONDS_PER_SLOT from the L1
// beacon (fakebeacon) is not just non-zero but actually USABLE for blob-based derivation
// after the L1 has upgraded to Glamsterdam (Amsterdam EL).
//
// It mirrors exactly what op-node/kona do: build an sources.L1BeaconClient over the L1
// beacon, find a recent L1 block that carries EIP-4844 blob versioned hashes (the batcher
// posts blob batches, see init_test's DataAvailabilityType=blobs), then GetBlobs for that
// block's eth.L1BlockRef. GetBlobs maps ref.Time -> beacon slot via SECONDS_PER_SLOT
// (getTimeToSlotFn reads /eth/v1/config/spec) and verifies each blob's KZG commitment
// against its versioned hash, so a successful non-empty return is a genuine, discriminating
// round-trip: it fails if the config is unusable, the slot mapping is wrong, or the blob
// is missing/invalid.
func TestL1Beacon_BlobRoundTrip_PostGlamsterdam(gt *testing.T) {
	t := devtest.SerialT(gt)
	sys := presets.NewMantleMinimal(t)
	require := t.Require()
	ctx := t.Ctx()

	// Drive the L1 to Glamsterdam (Amsterdam EL) so this is genuinely post-Glamsterdam.
	l1Config := sys.L1Network.Escape().ChainConfig()
	require.NotNil(l1Config.AmsterdamTime, "L1 AmsterdamTime must be configured")
	sys.L1EL.WaitForTime(*l1Config.AmsterdamTime)

	l1CL := sys.L1Network.Escape().L1CLNode(match.Assume(t, match.FirstL1CL))
	// The exact high-level client op-node/kona use to fetch blobs for derivation.
	beacon := sources.NewL1BeaconClient(l1CL.BeaconClient(), sources.L1BeaconClientConfig{})

	l1Eth := sys.L1EL.EthClient()

	// Scan recent L1 blocks until we find one carrying EIP-4844 blob versioned hashes.
	// Index each hash by its position among all blobs in the block (the sidecar ordering
	// the beacon indexes by), matching the derivation pipeline's blobIndex convention.
	var (
		ref    eth.L1BlockRef
		hashes []eth.IndexedBlobHash
		found  bool
	)
	deadline := time.Now().Add(90 * time.Second)
	for !found && time.Now().Before(deadline) {
		head := sys.L1EL.BlockRefByLabel(eth.Unsafe)
		floor := uint64(1)
		if head.Number > 64 {
			floor = head.Number - 64
		}
		for n := head.Number; n >= floor && !found; n-- {
			info, txs, err := l1Eth.InfoAndTxsByNumber(ctx, n)
			require.NoError(err, "read L1 block %d", n)
			var blockHashes []eth.IndexedBlobHash
			blobIndex := uint64(0)
			for _, tx := range txs {
				for _, h := range tx.BlobHashes() {
					blockHashes = append(blockHashes, eth.IndexedBlobHash{Index: blobIndex, Hash: h})
					blobIndex++
				}
			}
			if len(blockHashes) > 0 {
				ref = eth.InfoToL1BlockRef(info)
				hashes = blockHashes
				found = true
			}
		}
		if !found {
			time.Sleep(time.Second)
		}
	}
	require.True(found, "expected the batcher to post at least one EIP-4844 blob batch to L1 within the deadline")
	t.Logger().Info("found L1 blob-carrying block", "number", ref.Number, "time", ref.Time, "blobs", len(hashes))

	// Explicitly exercise the SECONDS_PER_SLOT-driven timestamp -> slot conversion that
	// blob derivation depends on (GetBlobs performs the same mapping internally).
	cfg, err := l1CL.BeaconClient().ConfigSpec(ctx)
	require.NoError(err)
	secondsPerSlot := uint64(cfg.Data.SecondsPerSlot)
	require.Greater(secondsPerSlot, uint64(0), "SECONDS_PER_SLOT must be usable (>0)")
	gen, err := l1CL.BeaconClient().BeaconGenesis(ctx)
	require.NoError(err)
	genesisTime := uint64(gen.Data.GenesisTime)
	require.GreaterOrEqual(ref.Time, genesisTime, "L1 block must not precede beacon genesis")
	slot := (ref.Time - genesisTime) / secondsPerSlot
	t.Logger().Info("mapped L1 block timestamp to beacon slot via SECONDS_PER_SLOT",
		"time", ref.Time, "genesisTime", genesisTime, "secondsPerSlot", secondsPerSlot, "slot", slot)

	// Round-trip: fetch and verify the blobs.
	blobs, err := beacon.GetBlobs(ctx, ref, hashes)
	require.NoError(err, "blob round-trip via SECONDS_PER_SLOT must succeed")
	require.Len(blobs, len(hashes), "must return exactly the requested blobs")
	for i, blob := range blobs {
		require.NotNil(blob, "blob %d must be present", i)
		commitment, err := blob.ComputeKZGCommitment()
		require.NoError(err, "blob %d must yield a KZG commitment", i)
		require.Equal(hashes[i].Hash, eth.KZGToVersionedHash(commitment),
			"blob %d must hash back to its requested versioned hash", i)
	}
}

// TestL1Beacon_ConfigSpec_GloasForkEpoch_PostGlamsterdam checks whether the L1 beacon
// serves GLOAS_FORK_EPOCH in /eth/v1/config/spec after the L1 has upgraded to Glamsterdam.
//
// The typed beacon.ConfigSpec() cannot answer this: eth.ReducedConfigData
// (op-service/eth/blobs_api.go) has exactly one field, SECONDS_PER_SLOT, guarded by a
// NumField()==1 test (op-service/eth/blobs_api_test.go), so extra keys are dropped. So we
// do a RAW HTTP GET of /eth/v1/config/spec via the beacon's underlying HTTP client and
// decode the raw JSON. If GLOAS_FORK_EPOCH is served we assert it parses; if not, we do
// NOT fake it — we record the exact set of keys the endpoint serves and skip, because the
// devstack fakebeacon is a fork-agnostic mock (its config/spec handler encodes only
// SECONDS_PER_SLOT) and post-Glamsterdam is enforced by the L1 EL, not the beacon spec.
func TestL1Beacon_ConfigSpec_GloasForkEpoch_PostGlamsterdam(gt *testing.T) {
	t := devtest.SerialT(gt)
	sys := presets.NewMantleMinimal(t)
	require := t.Require()
	ctx := t.Ctx()

	l1Config := sys.L1Network.Escape().ChainConfig()
	require.NotNil(l1Config.AmsterdamTime, "L1 AmsterdamTime must be configured")
	sys.L1EL.WaitForTime(*l1Config.AmsterdamTime)

	l1CL := sys.L1Network.Escape().L1CLNode(match.Assume(t, match.FirstL1CL))

	// Reach the raw Beacon HTTP so we can see keys the typed client drops. The in-process
	// fakebeacon (sysgo/CI) does not expose a raw Beacon HTTP endpoint, so this raw-spec
	// inspection can only run against a real CL: skip as an unmet precondition under sysgo and
	// run it under sysext (a devnet with a real Prysm/Lighthouse), where a missing endpoint
	// would instead fail via DEVNET_EXPECT_PRECONDITIONS_MET.
	rawProvider, ok := l1CL.(interface {
		RawBeaconHTTP() client.HTTP
	})
	if !ok {
		t.Skip("L1 CL exposes no raw Beacon HTTP (fakebeacon) — raw /eth/v1/config/spec inspection needs a real CL; run under sysext")
	}

	resp, err := rawProvider.RawBeaconHTTP().Get(ctx, "eth/v1/config/spec", nil,
		http.Header{"Accept": []string{"application/json"}})
	require.NoError(err, "raw GET /eth/v1/config/spec must succeed")
	defer resp.Body.Close()
	require.Equal(http.StatusOK, resp.StatusCode, "raw /eth/v1/config/spec must return 200")

	var rawSpec struct {
		Data map[string]json.RawMessage `json:"data"`
	}
	require.NoError(json.NewDecoder(resp.Body).Decode(&rawSpec), "raw /eth/v1/config/spec must be valid JSON")
	require.NotEmpty(rawSpec.Data, "raw /eth/v1/config/spec must carry a data object")

	served := make([]string, 0, len(rawSpec.Data))
	for k := range rawSpec.Data {
		served = append(served, k)
	}
	sort.Strings(served)
	t.Logger().Info("L1 beacon /eth/v1/config/spec served keys", "keys", served)

	// Cross-check the raw path against the typed client: SECONDS_PER_SLOT must be present in
	// the raw JSON and agree with the typed ConfigSpec value.
	rawSecRaw, hasSec := rawSpec.Data["SECONDS_PER_SLOT"]
	require.True(hasSec, "raw /eth/v1/config/spec must contain SECONDS_PER_SLOT; served keys: %v", served)
	var rawSec string
	require.NoError(json.Unmarshal(rawSecRaw, &rawSec), "SECONDS_PER_SLOT must be a JSON string")
	typed, err := l1CL.BeaconClient().ConfigSpec(ctx)
	require.NoError(err)
	require.Equal(strconv.FormatUint(uint64(typed.Data.SecondsPerSlot), 10), rawSec,
		"raw and typed SECONDS_PER_SLOT must agree")

	// GLOAS_FORK_EPOCH: assert it when served; otherwise document the not-served reality.
	gloasRaw, hasGloas := rawSpec.Data["GLOAS_FORK_EPOCH"]
	if !hasGloas {
		t.Skipf("L1 beacon /eth/v1/config/spec does not serve GLOAS_FORK_EPOCH; the devstack "+
			"fakebeacon is a fork-agnostic mock that serves only %v (see op-e2e/e2eutils/fakebeacon "+
			"config/spec handler). Post-Gloas/Glamsterdam is enforced by the L1 EL (Amsterdam), not "+
			"the beacon spec.", served)
	}
	var gloasStr string
	require.NoError(json.Unmarshal(gloasRaw, &gloasStr), "GLOAS_FORK_EPOCH must be a JSON string")
	gloasEpoch, err := strconv.ParseUint(gloasStr, 10, 64)
	require.NoError(err, "GLOAS_FORK_EPOCH must parse as a uint64 epoch")
	t.Logger().Info("L1 beacon serves GLOAS_FORK_EPOCH", "epoch", gloasEpoch)
}
