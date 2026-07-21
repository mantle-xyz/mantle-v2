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
// The real guard is the TYPED nil-check on the L2 header:
//   - require.Nil(hdr.BlockAccessListHash) is the ONLY way to catch EIP-7928 adoption.
//     op-geth's RPCMarshalHeader has no branch that ever emits a "blockAccessListHash"
//     key, so a raw-JSON key check could never observe a set field — that is why the
//     raw "blockAccessListHash" NotContains was removed as vacuous (it can never fail).
//   - require.Nil(hdr.SlotNumber) catches EIP-7843 adoption.
//
// The raw eth_getBlockByNumber "slotNumber" NotContains is a secondary corroboration
// on the external RPC surface: RPCMarshalHeader emits "slotNumber" iff head.SlotNumber
// != nil, so its absence confirms the same property from the user-facing JSON.
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
	sys.L1EL.WaitForTime(*l1Config.AmsterdamTime)

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
