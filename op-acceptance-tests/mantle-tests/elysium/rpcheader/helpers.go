package rpcheader

import (
	"github.com/ethereum-optimism/optimism/op-devstack/devtest"
	"github.com/ethereum-optimism/optimism/op-devstack/presets"
	"github.com/ethereum-optimism/optimism/op-service/eth"
)

// requireGlamsterdamL1Control proves that THIS run actually produces the EIP-7928 /
// EIP-7843 header fields, by requiring them on the L1 block that crossed Amsterdam.
//
// The L2 assertions in this package are negative ("the key/field must be absent"), and a
// negative assertion is only worth what its control is worth: an L2 that correctly omits an
// Amsterdam field and a stack where nothing ever produces that field both leave the L2 side
// empty. The L1 header is the control, and a strict one, because it is read through the SAME
// path (eth_getBlockByNumber JSON -> sources.RPCHeader -> CreateGethHeader). It also turns
// one silent degradation loud: if the mise-installed geth is missing, configureDevstackEnvVars
// falls back to the in-process op-geth, whose RPC emits no blockAccessListHash at all.
//
// Kept deliberately identical to the copy in the l2output package, which carries the longer
// rationale; change both together.
func requireGlamsterdamL1Control(t devtest.T, sys *presets.MantleMinimal, l1Ref eth.BlockRef) {
	require := t.Require()

	l1Info, err := sys.L1EL.Escape().EthClient().InfoByHash(t.Ctx(), l1Ref.Hash)
	require.NoErrorf(err, "read the L1 block %d that crossed AmsterdamTime", l1Ref.Number)
	l1Header := l1Info.Header()

	require.NotNilf(l1Header.BlockAccessListHash,
		"L1 block %d is at/after AmsterdamTime but carries no BlockAccessListHash: this run does "+
			"not produce EIP-7928 headers at all, so the L2 checks prove nothing. Is the L1 EL a "+
			"real Amsterdam-capable geth, or did it fall back to the in-process op-geth?", l1Ref.Number)
	require.NotNilf(l1Header.SlotNumber,
		"L1 block %d is at/after AmsterdamTime but carries no SlotNumber: this run does not produce "+
			"EIP-7843 headers at all, so the L2 checks prove nothing", l1Ref.Number)
}
