package rpcschema

import (
	"encoding/json"
	"sort"
	"testing"

	"github.com/ethereum-optimism/optimism/op-acceptance-tests/mantle-tests/elysium/internal/testhelpers"
	"github.com/ethereum-optimism/optimism/op-devstack/devtest"
	"github.com/ethereum-optimism/optimism/op-devstack/presets"
	"github.com/ethereum-optimism/optimism/op-service/eth"
	"github.com/ethereum/go-ethereum/common/hexutil"
)

// arsiaBlockKeys is the top-level key set op-geth's RPCMarshalBlock emits for a Mantle Arsia L2
// block via eth_getBlockByNumber(_, false). It is intentionally strict: if an op-geth dependency
// bump legitimately changes the Arsia schema, update this list with that bump; otherwise any extra
// key is an external-schema regression.
//
// Amsterdam keys such as blockAccessListHash and slotNumber do not belong here because the L2
// remains on Arsia rules.
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

// TestExternal_L2RPCSchemaStable pins the external L2 block JSON schema while the L1 runs
// Glamsterdam. It asserts the decoded top-level key set is complete for Arsia and has no extras,
// so any leaked Amsterdam key or other schema drift trips the test.
func TestExternal_L2RPCSchemaStable(gt *testing.T) {
	t := devtest.SerialT(gt)
	sys := presets.NewMantleMinimal(t)
	require := t.Require()
	ctx := t.Ctx()

	// Drive the L1 across the Amsterdam (Glamsterdam EL) boundary so the L2 block
	// we inspect is genuinely produced while consuming a Glamsterdam L1.
	l1Config := sys.L1Network.Escape().ChainConfig()
	require.NotNil(l1Config.AmsterdamTime, "L1 AmsterdamTime must be configured")
	testhelpers.WaitForGlamsterdamL1(t, sys.L1EL, *l1Config.AmsterdamTime)

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
