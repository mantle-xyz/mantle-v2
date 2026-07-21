package rpcschema

import (
	"encoding/json"
	"sort"
	"testing"

	"github.com/ethereum-optimism/optimism/op-devstack/devtest"
	"github.com/ethereum-optimism/optimism/op-devstack/presets"
	"github.com/ethereum-optimism/optimism/op-service/eth"
	"github.com/ethereum/go-ethereum/common/hexutil"
)

// arsiaBlockKeys is the EXACT top-level key set op-geth's RPCMarshalBlock (which
// wraps RPCMarshalHeader) emits for a Mantle Arsia L2 block fetched via
// eth_getBlockByNumber(_, false). Derived directly from Mantle op-geth
// internal/ethapi/api.go (RPCMarshalHeader @L1295, RPCMarshalBlock @L1341):
//
//   - RPCMarshalHeader, unconditional (16 keys): number..receiptsRoot below.
//   - RPCMarshalHeader, conditional keys that ARE populated on an Arsia
//     (Canyon..Isthmus) L2 header — verified in op-geth beacon/engine/types.go
//     ExecutableDataToBlock / miner/worker.go:
//     baseFeePerGas (BaseFee != nil, L2 is always EIP-1559),
//     withdrawalsRoot (WithdrawalsHash != nil since Canyon; Isthmus storage root),
//     blobGasUsed + excessBlobGas + parentBeaconBlockRoot (Ecotone/Cancun),
//     requestsHash (Isthmus/Skadi → CalcRequestsHash([]) == EmptyRequestsHash).
//     slotNumber is the ONLY conditional key that stays nil on Arsia → omitted.
//   - RPCMarshalBlock additions: size, transactions, uncles, withdrawals
//     (block.Withdrawals() != nil since Canyon).
var arsiaBlockKeys = []string{
	// RPCMarshalHeader, unconditional.
	"number", "hash", "parentHash", "nonce", "mixHash", "sha3Uncles",
	"logsBloom", "stateRoot", "miner", "difficulty", "extraData",
	"gasLimit", "gasUsed", "timestamp", "transactionsRoot", "receiptsRoot",
	// RPCMarshalHeader, conditional keys that are set on an Arsia L2.
	"baseFeePerGas", "withdrawalsRoot", "blobGasUsed", "excessBlobGas",
	"parentBeaconBlockRoot", "requestsHash",
	// RPCMarshalBlock additions.
	"size", "transactions", "uncles", "withdrawals",
}

// TestExternal_L2RPCSchemaStable is an L2-only external-schema regression guard:
// it pins the Mantle L2's eth_getBlockByNumber(_, false) response to the EXACT
// Arsia top-level key set, so an external SDK/indexer reading L2 blocks sees a
// stable schema.
//
// This asserts an L2-Arsia property, not an L1 discriminator — every assertion
// below would pass under ANY L1. The test drives the L1 across the Amsterdam
// (Glamsterdam EL) boundary only to make the environment realistic (the L2 block
// is genuinely produced while consuming a Glamsterdam L1); the L1 is the
// environment, not the discriminator. The Amsterdam header keys stay off the L2
// schema for L2-internal reasons, not because of the L1: op-geth's
// RPCMarshalHeader has NO branch that emits blockAccessListHash (EIP-7928) at
// all, and it emits slotNumber (EIP-7843) only when head.SlotNumber != nil,
// which an Arsia L2 never sets — so slotNumber-nil is an L2-fork property.
//
// Two complementary assertions pin the decoded top-level key SET exactly:
//
//	(a) COMPLETE: every expected Arsia key is present (none missing).
//	(b) EXACT: NO key outside the known Arsia set may appear, so ANY future
//	    Amsterdam/Glamsterdam top-level key that ever leaked onto the L2 schema
//	    (blockAccessListHash, slotNumber, or anything new) trips the test.
func TestExternal_L2RPCSchemaStable(gt *testing.T) {
	t := devtest.SerialT(gt)
	sys := presets.NewMantleMinimal(t)
	require := t.Require()
	ctx := t.Ctx()

	// Drive the L1 across the Amsterdam (Glamsterdam EL) boundary so the L2 block
	// we inspect is genuinely produced while consuming a Glamsterdam L1.
	l1Config := sys.L1Network.Escape().ChainConfig()
	require.NotNil(l1Config.AmsterdamTime, "L1 AmsterdamTime must be configured")
	sys.L1EL.WaitForTime(*l1Config.AmsterdamTime)

	// Advance the L2 a couple blocks past the boundary so "latest" resolves to a
	// block whose production overlapped a live Glamsterdam L1.
	start := sys.L2EL.BlockRefByLabel(eth.Unsafe).Number
	sys.L2EL.WaitForUnsafe(func(bi eth.BlockInfo) (bool, error) {
		return bi.NumberU64() >= start+2, nil
	})
	head := sys.L2EL.BlockRefByLabel(eth.Unsafe)

	// Fetch the raw post-boundary L2 block JSON exactly as an external SDK/indexer
	// would (eth_getBlockByNumber takes a hex NUMBER/tag, not a hash).
	var raw json.RawMessage
	err := sys.L2EL.Escape().EthClient().RPC().CallContext(ctx, &raw, "eth_getBlockByNumber", hexutil.EncodeUint64(head.Number), false)
	require.NoError(err, "raw eth_getBlockByNumber on the L2 EL must succeed")
	require.NotEmpty(raw, "raw L2 block response must not be empty")

	// Decode into the top-level key set (values are opaque here — we assert the schema).
	var fields map[string]json.RawMessage
	require.NoError(json.Unmarshal(raw, &fields), "L2 block JSON must decode into an object")

	// (a) COMPLETE: the exact Arsia key set is present — no expected key is missing.
	var missing []string
	for _, k := range arsiaBlockKeys {
		if _, ok := fields[k]; !ok {
			missing = append(missing, k)
		}
	}
	sort.Strings(missing)
	require.Emptyf(missing, "L2 (Arsia) block schema is missing expected keys: %v", missing)

	// (b) EXACT: no key outside the known Arsia set. This catches ANY new top-level
	// key generically, including blockAccessListHash/slotNumber.
	allowed := make(map[string]bool, len(arsiaBlockKeys))
	for _, k := range arsiaBlockKeys {
		allowed[k] = true
	}
	var unexpected []string
	for k := range fields {
		if !allowed[k] {
			unexpected = append(unexpected, k)
		}
	}
	sort.Strings(unexpected)
	require.Emptyf(unexpected, "L2 (Arsia) block schema exposes unexpected (non-Arsia) top-level keys: %v", unexpected)
}
