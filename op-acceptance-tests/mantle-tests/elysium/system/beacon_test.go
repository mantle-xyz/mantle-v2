package system

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"testing"

	"github.com/ethereum-optimism/optimism/op-acceptance-tests/mantle-tests/elysium/base/beaconslot"
	"github.com/ethereum-optimism/optimism/op-devstack/devtest"
	"github.com/ethereum-optimism/optimism/op-devstack/presets"
	"github.com/ethereum-optimism/optimism/op-devstack/stack/match"
	"github.com/ethereum-optimism/optimism/op-service/apis"
	"github.com/ethereum-optimism/optimism/op-service/client"
	"github.com/ethereum-optimism/optimism/op-service/eth"
	"github.com/ethereum-optimism/optimism/op-service/sources"
	gethtypes "github.com/ethereum/go-ethereum/core/types"
)

// The beacon cases below drive op-node's own L1 beacon client against the devnet's REAL
// consensus layer. They exist because the sysgo suite structurally cannot cover this: its L1 CL
// is fakebeacon, a fork-agnostic mock, so the post-Gloas beacon-API behaviour these cases are
// about (blob_kzg_commitments moved out of the block body by ePBS, which makes Prysm's
// blob_sidecars endpoint 500 and Lighthouse's /blobs and /blob_sidecars 400 for blocks that DO
// carry blobs) simply does not exist there.
//
// Every case is guarded on the sampled block genuinely being post-Gloas. Without that guard a
// pre-Gloas Deneb/Electra/Fulu beacon would satisfy the blob fetches identically and the case
// would verify nothing fork-specific at all.

// expectedSecondsPerSlotEnv optionally pins SECONDS_PER_SLOT to the value a particular devnet is
// known to run. It is deliberately opt-in: slot time is a property of the devnet's genesis, not of
// Glamsterdam, and the environments this suite targets do not agree on it (rde-v4's profiles run
// 12s, kurtosis devnets run 2s). Hardcoding one would turn a correctly-running chain red.
const expectedSecondsPerSlotEnv = "ELYSIUM_EXPECTED_SECONDS_PER_SLOT"

// l1Beacon bundles the L1 clients the beacon cases share. They come from the devnet descriptor
// via the node frontends, so these cases need no hand-set endpoint environment variables.
type l1Beacon struct {
	sys    *presets.MantleMinimal
	beacon apis.BeaconClient
	http   client.HTTP
}

func newL1Beacon(t devtest.T, sys *presets.MantleMinimal) *l1Beacon {
	cl := sys.L1Network.Escape().L1CLNode(match.FirstL1CL)
	return &l1Beacon{
		sys:    sys,
		beacon: cl.BeaconClient(),
		http:   cl.HTTPClient(),
	}
}

// forkVersion returns the consensus-layer fork name ("deneb", "electra", "fulu", "gloas", …) the
// beacon reports for the block at a slot. The top-level "version" field of
// /eth/v2/beacon/blocks/{slot} is the beacon API's fork discriminator for that block.
func (b *l1Beacon) forkVersion(t devtest.T, slot uint64) string {
	require := t.Require()
	resp, err := b.http.Get(t.Ctx(), fmt.Sprintf("/eth/v2/beacon/blocks/%d", slot), nil, nil)
	require.NoErrorf(err, "fetch beacon block at slot %d to confirm its fork", slot)
	defer resp.Body.Close()
	require.Equalf(http.StatusOK, resp.StatusCode,
		"beacon block at slot %d must be available to confirm the fork (got HTTP %d)", slot, resp.StatusCode)
	var body struct {
		Version string `json:"version"`
	}
	require.NoErrorf(json.NewDecoder(resp.Body).Decode(&body), "decode beacon block fork version at slot %d", slot)
	require.NotEmptyf(body.Version, "beacon block at slot %d returned no fork version", slot)
	return body.Version
}

// rawConfigSpec reads /eth/v1/config/spec as its full key/value set. The typed ConfigSpec only
// models SECONDS_PER_SLOT, so the fork-schedule keys have to come from the raw response.
//
// Values are kept as raw JSON rather than strings: the spec is NOT a flat string map. Prysm serves
// BLOB_SCHEDULE as an array of objects ([{"EPOCH":"0","MAX_BLOBS_PER_BLOCK":"15"}, …]), so decoding
// into map[string]string fails outright on the whole document — taking the scalar fork-schedule
// keys this case actually reads down with it.
func (b *l1Beacon) rawConfigSpec(t devtest.T) map[string]json.RawMessage {
	require := t.Require()
	resp, err := b.http.Get(t.Ctx(), "/eth/v1/config/spec", nil, nil)
	require.NoError(err, "fetch raw /eth/v1/config/spec")
	defer resp.Body.Close()
	require.Equalf(http.StatusOK, resp.StatusCode,
		"real L1 beacon must serve raw /eth/v1/config/spec (got HTTP %d)", resp.StatusCode)
	var body struct {
		Data map[string]json.RawMessage `json:"data"`
	}
	require.NoError(json.NewDecoder(resp.Body).Decode(&body), "decode raw /eth/v1/config/spec")
	require.NotEmpty(body.Data, "raw /eth/v1/config/spec data must not be empty")
	return body.Data
}

// configSpecUint64 reads a scalar spec key. The beacon API encodes every numeric config value as a
// JSON string, so the raw value is unquoted before parsing.
func configSpecUint64(t devtest.T, rawSpec map[string]json.RawMessage, key string) uint64 {
	require := t.Require()
	raw, ok := rawSpec[key]
	require.Truef(ok, "/eth/v1/config/spec must include %s", key)
	var value string
	require.NoErrorf(json.Unmarshal(raw, &value),
		"/eth/v1/config/spec %s=%s must be a quoted scalar", key, raw)
	out, err := strconv.ParseUint(value, 10, 64)
	require.NoErrorf(err, "/eth/v1/config/spec %s=%q must be uint64", key, value)
	return out
}

// secondsPerSlot reads the value op-node uses to map L1 timestamps to beacon slots.
func (b *l1Beacon) secondsPerSlot(t devtest.T) uint64 {
	require := t.Require()
	cfg, err := b.beacon.ConfigSpec(t.Ctx())
	require.NoError(err, "real L1 beacon must serve /eth/v1/config/spec")
	secondsPerSlot := uint64(cfg.Data.SecondsPerSlot)
	require.Greater(secondsPerSlot, uint64(0), "SECONDS_PER_SLOT must be usable (>0)")
	return secondsPerSlot
}

// slotOf maps an L1 block to its beacon slot exactly as op-node does: genesis time plus
// SECONDS_PER_SLOT.
func (b *l1Beacon) slotOf(t devtest.T, ref eth.L1BlockRef, secondsPerSlot uint64) uint64 {
	require := t.Require()
	genesis, err := b.beacon.BeaconGenesis(t.Ctx())
	require.NoError(err, "real L1 beacon must serve /eth/v1/beacon/genesis")
	genesisTime := uint64(genesis.Data.GenesisTime)
	require.GreaterOrEqual(ref.Time, genesisTime, "sampled L1 block must be at/after beacon genesis")
	return (ref.Time - genesisTime) / secondsPerSlot
}

// findBlobBlock scans recent L1 blocks for one carrying blobs and returns it with its indexed
// blob hashes in block order.
func (b *l1Beacon) findBlobBlock(t devtest.T) (eth.L1BlockRef, []eth.IndexedBlobHash) {
	require := t.Require()
	ctx := t.Ctx()
	l1Eth := b.sys.L1EL.EthClient()
	head := b.sys.L1EL.BlockRefByLabel(eth.Unsafe)
	floor := uint64(1)
	if head.Number > 64 {
		floor = head.Number - 64
	}
	for n := head.Number; n >= floor; n-- {
		info, txs, err := l1Eth.InfoAndTxsByNumber(ctx, n)
		require.NoErrorf(err, "read L1 block %d", n)
		idx := uint64(0)
		var hashes []eth.IndexedBlobHash
		for _, tx := range txs {
			if tx.Type() != gethtypes.BlobTxType {
				continue
			}
			for _, h := range tx.BlobHashes() {
				hashes = append(hashes, eth.IndexedBlobHash{Index: idx, Hash: h})
				idx++
			}
		}
		if len(hashes) > 0 {
			return eth.InfoToL1BlockRef(info), hashes
		}
	}
	require.Fail("no L1 block with blobs in the last 64 blocks — is the batcher posting blobs and is the chain past Gloas?")
	return eth.L1BlockRef{}, nil
}

// gloasBlobBlock finds a blob-carrying L1 block and requires it to be genuinely post-Gloas,
// returning it with its blob hashes, mapped slot and the slot time used for the mapping.
func (b *l1Beacon) gloasBlobBlock(t devtest.T) (eth.L1BlockRef, []eth.IndexedBlobHash, uint64, uint64) {
	require := t.Require()
	secondsPerSlot := b.secondsPerSlot(t)
	ref, hashes := b.findBlobBlock(t)
	slot := b.slotOf(t, ref, secondsPerSlot)
	version := b.forkVersion(t, slot)
	require.Equalf("gloas", version,
		"sampled L1 block %d maps to beacon slot %d whose fork version is %q, not \"gloas\": this run does not exercise the post-Gloas beacon-API path",
		ref.Number, slot, version)
	t.Logf("confirmed L1 block %d (slot %d) is a post-Gloas beacon block carrying %d blobs",
		ref.Number, slot, len(hashes))
	return ref, hashes, slot, secondsPerSlot
}

// requireBlobsMatch checks the returned blobs against the requested hashes, in order, by
// recomputing each KZG commitment — so a reordered or substituted blob fails.
func requireBlobsMatch(t devtest.T, blobs []*eth.Blob, hashes []eth.IndexedBlobHash) {
	require := t.Require()
	require.Len(blobs, len(hashes), "beacon must return every requested blob")
	for i, blob := range blobs {
		require.NotNilf(blob, "returned blob %d must be present", i)
		commitment, err := blob.ComputeKZGCommitment()
		require.NoErrorf(err, "returned blob %d KZG commitment", i)
		require.Equalf(hashes[i].Hash, eth.KZGToVersionedHash(commitment),
			"returned blob %d must match requested hash %s in order", i, hashes[i].Hash)
	}
}

// runL1BeaconConfigSpec checks the consensus-layer configuration path op-node relies on for blob
// derivation: /eth/v1/config/spec must report a SECONDS_PER_SLOT that agrees with the L1's real
// block spacing, the Gloas fork must be scheduled, and that same genesis-time + SECONDS_PER_SLOT
// mapping must locate a known blob block whose blobs the real beacon actually serves.
//
// The slot time is checked for CONSISTENCY rather than against a hardcoded number — it is a
// property of the devnet's genesis, not of Glamsterdam. Set ELYSIUM_EXPECTED_SECONDS_PER_SLOT to
// additionally pin it for a devnet whose value is known.
func runL1BeaconConfigSpec(gt *testing.T) {
	t := devtest.SerialT(gt)
	sys := presets.NewMantleMinimal(t)
	require := t.Require()

	b := newL1Beacon(t, sys)
	secondsPerSlot := b.secondsPerSlot(t)

	if raw := os.Getenv(expectedSecondsPerSlotEnv); raw != "" {
		want, err := strconv.ParseUint(raw, 10, 64)
		require.NoErrorf(err, "%s=%q must be a uint64", expectedSecondsPerSlotEnv, raw)
		require.Equalf(want, secondsPerSlot,
			"beacon reports SECONDS_PER_SLOT=%d but %s pins it to %d", secondsPerSlot, expectedSecondsPerSlotEnv, want)
	} else {
		t.Logf("%s unset: not pinning a slot time (beacon reports SECONDS_PER_SLOT=%d)", expectedSecondsPerSlotEnv, secondsPerSlot)
	}

	// The spec value must describe the chain it came from: sample a window of L1 blocks and
	// require the spacing rule to hold. A missed slot legitimately widens a gap, so the rule
	// tolerates multiples but still requires the tightest gap to be exactly one slot.
	requireSlotTimeMatchesL1(t, sys, secondsPerSlot)

	rawSpec := b.rawConfigSpec(t)
	gloasForkEpoch := configSpecUint64(t, rawSpec, "GLOAS_FORK_EPOCH")
	require.NotEqual(^uint64(0), gloasForkEpoch, "GLOAS_FORK_EPOCH must be configured, not disabled")
	slotsPerEpoch := configSpecUint64(t, rawSpec, "SLOTS_PER_EPOCH")
	require.Greater(slotsPerEpoch, uint64(0), "SLOTS_PER_EPOCH must be usable (>0)")

	ref, hashes, slot, _ := b.gloasBlobBlock(t)
	require.GreaterOrEqualf(slot/slotsPerEpoch, gloasForkEpoch,
		"sampled blob block %d maps to slot %d, before GLOAS_FORK_EPOCH=%d", ref.Number, slot, gloasForkEpoch)

	// Close the loop: the mapping the spec values produced must actually address the blobs.
	resp, err := b.beacon.BeaconBlobs(t.Ctx(), slot, hashes)
	require.NoErrorf(err, "real beacon must serve the known blobs at mapped slot %d", slot)
	requireBlobsMatch(t, resp.Data, hashes)
	t.Logf("config/spec is self-consistent: SECONDS_PER_SLOT=%d, GLOAS_FORK_EPOCH=%d, slot %d served %d blobs",
		secondsPerSlot, gloasForkEpoch, slot, len(resp.Data))
}

// requireSlotTimeMatchesL1 samples a window of recent L1 blocks and applies the slot-spacing rule
// (beaconslot.SpacingError, unit-tested in that package because this case cannot run in CI).
func requireSlotTimeMatchesL1(t devtest.T, sys *presets.MantleMinimal, secondsPerSlot uint64) {
	const sampleBlocks = uint64(8)
	require := t.Require()

	head := sys.L1EL.BlockRefByLabel(eth.Unsafe)
	require.Greaterf(head.Number, sampleBlocks, "need more than %d L1 blocks to sample slot spacing", sampleBlocks)

	first := head.Number - sampleBlocks
	times := make([]uint64, 0, sampleBlocks+1)
	for n := first; n <= head.Number; n++ {
		times = append(times, sys.L1EL.BlockRefByNumber(n).Time)
	}
	require.NoErrorf(beaconslot.SpacingError(times, secondsPerSlot),
		"beacon SECONDS_PER_SLOT=%d is inconsistent with the real L1 block spacing over blocks %d..%d",
		secondsPerSlot, first, head.Number)
	t.Logf("SECONDS_PER_SLOT=%d matches the real L1 block spacing over blocks %d..%d",
		secondsPerSlot, first, head.Number)
}

// runL1BeaconBlobsFetch points OUR op-node L1BeaconClient — the exact code under test, compiled
// from this branch — at the devnet's real beacon and requires it to fetch a real L1 block's blobs.
//
// This is the isolated operation the post-Gloas beacon-API quirk breaks: with blob_kzg_commitments
// moved out of the block body, Prysm's blob_sidecars endpoint returns 500 and Lighthouse returns
// 400 on both endpoints for blocks that DO carry blobs, and op-node's GetBlobs only falls
// /blobs -> /blob_sidecars, with no data_column_sidecars fallback. If it cannot fetch, this is red.
func runL1BeaconBlobsFetch(gt *testing.T) {
	t := devtest.SerialT(gt)
	sys := presets.NewMantleMinimal(t)
	require := t.Require()

	b := newL1Beacon(t, sys)
	ref, hashes, slot, _ := b.gloasBlobBlock(t)

	beaconCl := sources.NewL1BeaconClient(b.beacon, sources.L1BeaconClientConfig{})
	blobs, err := beaconCl.GetBlobs(t.Ctx(), ref, hashes)
	require.NoError(err,
		"op-node must fetch blobs from the real post-Gloas beacon; a Gloas beacon-API quirk (Prysm 500 / Lighthouse 400, no data_column fallback) breaks this")
	requireBlobsMatch(t, blobs, hashes)
	t.Logf("op-node fetched %d/%d blobs from the real post-Gloas beacon at L1 block %d (slot %d)",
		len(blobs), len(hashes), ref.Number, slot)
}

// There is deliberately no main-path-vs-/blob_sidecars-fallback comparison case here.
//
// Such a case (TestL1Beacon_BlobOrderConsistency) compared the blobs returned by
// GET /eth/v1/beacon/blobs against those returned by GET /eth/v1/beacon/blob_sidecars for the same
// hashes, to catch a fallback that silently reorders or substitutes blobs. The beacon-API spec has
// since retired that second path: blob_sidecars/{block_id} was DEPRECATED in beacon-APIs v4.0.0
// (which is where /blobs/{block_id} was introduced to replace it) and REMOVED outright as of
// v5.0.0-alpha.0, together with the blob_sidecar SSE event. A spec-current beacon therefore has
// only one path, so the comparison has nothing left to compare and could never pass.
//
// The property that case existed to protect — blobs coming back in the requested order — is not
// lost: requireBlobsMatch recomputes each blob's KZG commitment and pins it to hashes[i] by index,
// and runL1BeaconBlobsFetch applies it to the real /blobs response.
