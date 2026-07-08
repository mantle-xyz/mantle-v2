package rpcheader

import (
	"encoding/json"
	"testing"

	"github.com/ethereum-optimism/optimism/op-devstack/devtest"
	"github.com/ethereum-optimism/optimism/op-devstack/presets"
	"github.com/ethereum-optimism/optimism/op-service/eth"
	"github.com/ethereum/go-ethereum/common/hexutil"
)

// TestL2RPCHeader_OmitsNewFields asserts the Mantle L2 EL's raw
// JSON-RPC block header omits the Glamsterdam/Amsterdam header keys
// "blockAccessListHash" (EIP-7928) and "slotNumber" (EIP-7843). The Mantle L2
// stays on Arsia rules even while the L1 runs Glamsterdam, so these fields must
// never appear in an L2 block's user-RPC response.
//
// Two complementary checks:
//  1. The *raw* eth_getBlockByNumber payload (op-geth's RPCMarshalHeader) must omit both
//     keys. "slotNumber" is DISCRIMINATING: RPCMarshalHeader emits it iff head.SlotNumber
//     != nil, so its absence proves the L2 set no EIP-7843 slot number. NOTE:
//     "blockAccessListHash" is a WEAKER signal — RPCMarshalHeader has no branch that ever
//     emits it, so its absence is structural, not proof of non-adoption.
//  2. The typed L2 header must therefore ALSO have BlockAccessListHash == nil (and
//     SlotNumber == nil). This is the discriminating guard for EIP-7928 that the raw RPC
//     key cannot give: a BAL-adopting L2 would set the field while RPCMarshalHeader still
//     hides it, so check (1) alone would be a false green.
//
// (Marshaling a *types.Header with json.Marshal is NOT a substitute for check 1: the
// generated tags carry no `omitempty`, so a nil field serializes as `...:null` and would
// spuriously "contain" the keys.)
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

	// (1) External RPC surface: the raw eth_getBlockByNumber JSON must omit both keys.
	var raw json.RawMessage
	err := sys.L2EL.Escape().EthClient().RPC().CallContext(ctx, &raw, "eth_getBlockByNumber", hexutil.EncodeUint64(head.Number), false)
	require.NoError(err, "raw eth_getBlockByNumber on the L2 EL must succeed")
	require.NotEmpty(raw, "raw L2 block header response must not be empty")

	body := string(raw)
	require.NotContains(body, "slotNumber",
		"L2 (Arsia) RPC block header must not carry the EIP-7843 slotNumber key (discriminating: op-geth emits it iff set)")
	require.NotContains(body, "blockAccessListHash",
		"L2 (Arsia) RPC block header must not surface the EIP-7928 blockAccessListHash key")

	// (2) Discriminating guard: the typed L2 header must have both Amsterdam fields nil.
	// This is what actually catches EIP-7928 adoption — RPCMarshalHeader would hide a set
	// BlockAccessListHash, so the raw-key check in (1) cannot observe it.
	info, _, err := sys.L2EL.Escape().EthClient().InfoAndTxsByHash(ctx, head.Hash)
	require.NoError(err, "must read the typed L2 header by hash")
	hdr := info.Header()
	require.Nil(hdr.BlockAccessListHash,
		"L2 (Arsia) header must not carry an EIP-7928 BlockAccessListHash while L1 runs Glamsterdam")
	require.Nil(hdr.SlotNumber,
		"L2 (Arsia) header must not carry an EIP-7843 SlotNumber while L1 runs Glamsterdam")
}
