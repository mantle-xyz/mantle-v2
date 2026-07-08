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

// amsterdamAddedKeys are the top-level block keys the L1 Glamsterdam (Amsterdam
// EL) upgrade introduces. A Mantle Arsia L2 must never surface them on its user
// JSON-RPC, so that an external SDK/indexer sees no schema change.
//   - blockAccessListHash: EIP-7928. op-geth's RPCMarshalHeader has NO branch that
//     emits this key, so its absence is structural (a weaker signal on its own).
//   - slotNumber:          EIP-7843. RPCMarshalHeader emits it iff head.SlotNumber
//     != nil, so its absence is DISCRIMINATING — it proves the Arsia L2 set no
//     EIP-7843 slot number even while the L1 runs Glamsterdam.
var amsterdamAddedKeys = []string{"blockAccessListHash", "slotNumber"}

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

// TestExternal_L2RPCSchemaStable proves the Mantle L2's external
// eth_getBlockByNumber schema is byte-for-byte the stable Arsia key set — with
// none of the L1 Glamsterdam (Amsterdam EL) header keys — even after the L1
// crosses the Amsterdam boundary. An SDK/indexer reading L2 blocks therefore
// sees no change.
//
// Three complementary assertions on the decoded top-level key SET:
//
//	(a) MUST NOT contain any Amsterdam-added key (strict): "blockAccessListHash"
//	    (EIP-7928), "slotNumber" (EIP-7843). slotNumber is discriminating.
//	(b) MUST contain the exact Arsia key set: no expected key is missing.
//	(c) DISCRIMINATING exactness: NO key outside the known Arsia set may appear,
//	    so ANY future Amsterdam/Glamsterdam top-level key trips the test — not
//	    only the two named in (a).
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

	// (a) MUST NOT contain any Amsterdam-added key (strict, discriminating).
	for _, k := range amsterdamAddedKeys {
		require.NotContainsf(fields, k,
			"post-Amsterdam L2 block schema must not expose the Glamsterdam header key %q; an external SDK/indexer must keep seeing the unchanged Arsia schema", k)
	}

	// (b) MUST contain the exact Arsia key set: no expected key is missing.
	var missing []string
	for _, k := range arsiaBlockKeys {
		if _, ok := fields[k]; !ok {
			missing = append(missing, k)
		}
	}
	sort.Strings(missing)
	require.Emptyf(missing, "L2 (Arsia) block schema is missing expected keys: %v", missing)

	// (c) DISCRIMINATING exactness: no key outside the known Arsia set. This catches
	// ANY new top-level key generically, not just the two named in (a).
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
