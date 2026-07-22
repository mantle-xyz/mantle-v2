package rpcheader

import (
	"encoding/json"
	"testing"

	"github.com/ethereum-optimism/optimism/op-devstack/devtest"
	"github.com/ethereum-optimism/optimism/op-devstack/presets"
	"github.com/ethereum-optimism/optimism/op-service/eth"
	"github.com/ethereum/go-ethereum/common/hexutil"
)

// TestL2RPCHeader_OmitsNewFields verifies an L2-Arsia header property: the Mantle
// L2 header never carries the Amsterdam/Glamsterdam fields "blockAccessListHash"
// (EIP-7928) and "slotNumber" (EIP-7843). This is a property of the L2 staying on
// Arsia rules, NOT of the L1 fork. The L1 runs Glamsterdam here only as the
// environment the L2 is produced against, not as the thing being defended against:
// every assertion below would pass under any L1, so the L1 is the environment, not
// the discriminator.
//
// The two fields are NOT guarded equally well, and it is worth being precise about which
// assertion actually carries which one:
//
//   - slotNumber is genuinely falsifiable from the L2 side. op-geth's RPCMarshalHeader emits
//     the key iff head.SlotNumber != nil, so both the raw-JSON NotContains and the typed
//     nil-check would go red if the L2 ever set it.
//
//   - blockAccessListHash is NOT falsifiable from the L2 side by either check. op-geth's
//     RPCMarshalHeader has no branch for that key at any value, and the typed value is
//     PARSED FROM THAT SAME JSON (sources.RPCHeader -> CreateGethHeader), so a set field
//     would be invisible to both. An earlier version of this comment called the typed check
//     "the ONLY way to catch EIP-7928 adoption"; that was wrong for exactly this reason —
//     the typed check inherits the serializer's blindness rather than escaping it.
//     What would actually catch a BAL-bearing L2 header is the eth client's blockhash
//     verification: TrustRPC is false, so the header is re-hashed from its RLP preimage,
//     which DOES include the BAL field, and the fetch itself fails.
//
// So the nil-checks alone would stay green in a stack that never implements these fields.
// requireGlamsterdamL1Control below is what rules that out: it requires the L1 in this same
// run, read through this same client path, to show both fields NON-nil.
//
// (Marshaling a *types.Header with json.Marshal is NOT a substitute for the raw check:
// the generated tags carry no `omitempty`, so a nil field serializes as `...:null` and
// would spuriously "contain" the keys.)
func TestL2RPCHeader_OmitsNewFields(gt *testing.T) {
	t := devtest.SerialT(gt)
	sys := presets.NewMantleMinimal(t)
	require := t.Require()
	ctx := t.Ctx()

	// Wait for the L1 to activate Amsterdam (Glamsterdam EL) so the L2 block we
	// inspect is genuinely produced while consuming a Glamsterdam L1.
	l1Config := sys.L1Network.Escape().ChainConfig()
	require.NotNil(l1Config.AmsterdamTime, "L1 AmsterdamTime must be configured")
	l1Ref := sys.L1EL.WaitForTime(*l1Config.AmsterdamTime)
	requireGlamsterdamL1Control(t, sys, l1Ref)

	// Advance the L2 a couple of blocks past the Amsterdam boundary so "latest"
	// resolves to a block whose production overlapped a live Glamsterdam L1.
	start := sys.L2EL.BlockRefByLabel(eth.Unsafe).Number
	sys.L2EL.WaitForUnsafe(func(bi eth.BlockInfo) (bool, error) {
		return bi.NumberU64() >= start+2, nil
	})

	head := sys.L2EL.BlockRefByLabel(eth.Unsafe)

	// (1) External RPC surface: the raw eth_getBlockByNumber JSON must omit the
	// slotNumber key (op-geth emits it iff head.SlotNumber != nil). No raw check for
	// blockAccessListHash: RPCMarshalHeader never emits that key, so its absence is
	// structural and can never fail — the typed guard in (2) is the real check for it.
	var raw json.RawMessage
	err := sys.L2EL.Escape().EthClient().RPC().CallContext(ctx, &raw, "eth_getBlockByNumber", hexutil.EncodeUint64(head.Number), false)
	require.NoError(err, "raw eth_getBlockByNumber on the L2 EL must succeed")
	require.NotEmpty(raw, "raw L2 block header response must not be empty")

	body := string(raw)
	require.NotContains(body, "slotNumber",
		"L2 (Arsia) RPC block header must not carry the EIP-7843 slotNumber key (op-geth emits it iff set)")

	// (2) The real guard: the typed L2 header must have both Amsterdam fields nil.
	// This is what actually catches EIP-7928 adoption — RPCMarshalHeader would hide a set
	// BlockAccessListHash, so the raw-key check in (1) cannot observe it. The L2 stays on
	// Arsia rules regardless of the L1 fork, so these hold under any L1.
	info, _, err := sys.L2EL.Escape().EthClient().InfoAndTxsByHash(ctx, head.Hash)
	require.NoError(err, "must read the typed L2 header by hash")
	hdr := info.Header()
	require.Nil(hdr.BlockAccessListHash,
		"L2 (Arsia) header must not carry an EIP-7928 BlockAccessListHash")
	require.Nil(hdr.SlotNumber,
		"L2 (Arsia) header must not carry an EIP-7843 SlotNumber")
}
