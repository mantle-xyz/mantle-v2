package evmgas

import (
	"testing"

	"github.com/ethereum-optimism/optimism/op-devstack/devtest"
	"github.com/ethereum-optimism/optimism/op-devstack/dsl"
	"github.com/ethereum-optimism/optimism/op-devstack/presets"
	"github.com/ethereum-optimism/optimism/op-service/eth"
	"github.com/ethereum-optimism/optimism/op-service/txplan"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
)

// Calldata gas constants (see op-geth core/state_transition.go FloorDataGas and
// params/protocol_params.go). Mantle's L2 runs on Arsia rules, which price the
// EIP-7623 calldata floor at TxCostFloorPerToken = 10 gas per token, where a
// non-zero byte is worth TxTokenPerNonZeroByte = 4 tokens. A value-less call to a
// plain EOA runs no code, so the data floor dominates the EIP-2028 standard cost,
// and the per-non-zero-byte gas is exactly floorPerToken * tokensPerNonZeroByte =
// 40 gas/byte. This is an L2-fork-internal schedule, independent of the L1 fork.
const (
	txBaseGas            = 21_000 // params.TxGas
	tokensPerNonZeroByte = 4      // params.TxTokenPerNonZeroByte (EIP-7623)
	arsiaFloorPerToken   = 10     // params.TxCostFloorPerToken (EIP-7623)

	// arsiaGasPerNonZeroByte is the Mantle-Arsia calldata floor: 10 * 4 = 40 gas/byte.
	arsiaGasPerNonZeroByte = arsiaFloorPerToken * tokensPerNonZeroByte
)

// TestL2EVM_CalldataGasStaysArsia verifies an L2-Arsia property: the Mantle L2
// prices non-zero calldata at the Arsia EIP-7623 floor of 40 gas/byte (10 gas per
// token * 4 tokens per non-zero byte). This pricing is governed by the L2 fork,
// not the L1. The test runs while the L1 is on Glamsterdam (Amsterdam), but the
// L1 is only the environment here, not the discriminator — the assertions pin the
// L2's own gas schedule and would hold under any L1.
//
// It sends two value-less calls to the same plain EOA carrying 1000 and 2000
// non-zero calldata bytes. Because such a call executes no code, the EIP-7623 data
// floor governs the cost, so each tx's gas is exactly 21000 + 40 * len. The two
// exact-value assertions pin both the base and the per-byte floor rate directly.
//
// COVERAGE: this covers ONLY the calldata-floor sub-case of the L2 gas schedule.
// Sibling packages cover the others: access-list repricing (evmaccesslist),
// block-level gas accounting (evmblockgas), opcodes (evmopcodes).
func TestL2EVM_CalldataGasStaysArsia(gt *testing.T) {
	t := devtest.SerialT(gt)
	sys := presets.NewMantleMinimal(t)
	require := t.Require()

	// The L1 is on Glamsterdam (Amsterdam) here — that is the environment the L2
	// runs in, not what this test discriminates on. Wait for the crossing so the
	// L2's Arsia pricing is exercised alongside an upgraded L1.
	l1Config := sys.L1Network.Escape().ChainConfig()
	require.NotNil(l1Config.AmsterdamTime, "L1 AmsterdamTime must be configured")
	sys.L1EL.WaitForTime(*l1Config.AmsterdamTime)

	wallet := sys.FunderL2.NewFundedEOA(eth.OneEther)

	// A plain EOA recipient (no code, well above the precompile range and not a
	// Mantle 0x42.. predeploy): a value-less call to it runs no code, so the tx's
	// gas is intrinsic + calldata only and the EIP-7623 data floor governs.
	recipient := common.HexToAddress("0x00000000000000000000000000000000C0FFEE00")

	const (
		smallLen = 1000
		largeLen = 2000
	)

	smallGas := sendNonZeroCalldata(t, wallet, recipient, smallLen)
	largeGas := sendNonZeroCalldata(t, wallet, recipient, largeLen)

	// Exact per-tx cost for a value-less EOA call: 21000 base + 40 gas/non-zero byte
	// (the Arsia EIP-7623 calldata floor). Pinning both lengths fixes the base and
	// the per-byte floor rate exactly.
	require.EqualValues(txBaseGas+arsiaGasPerNonZeroByte*smallLen, smallGas,
		"small-calldata tx gas must match Arsia floor pricing (40 gas/non-zero byte)")
	require.EqualValues(txBaseGas+arsiaGasPerNonZeroByte*largeLen, largeGas,
		"large-calldata tx gas must match Arsia floor pricing (40 gas/non-zero byte)")

	t.Log("L2 calldata gas priced at the Arsia floor",
		"smallGas", smallGas,
		"largeGas", largeGas,
		"arsiaGasPerNonZeroByte", arsiaGasPerNonZeroByte)
}

// sendNonZeroCalldata sends a value-less L2 tx carrying dataLen bytes of 0x01
// (all non-zero) to recipient and returns the receipt's GasUsed.
func sendNonZeroCalldata(t devtest.T, wallet *dsl.EOA, recipient common.Address, dataLen int) uint64 {
	data := make([]byte, dataLen)
	for i := range data {
		data[i] = 0x01
	}
	receipt, err := txplan.NewPlannedTx(txplan.Combine(
		wallet.Plan(),
		txplan.WithTo(&recipient),
		txplan.WithData(data),
		txplan.WithGasLimit(20_000_000),
	)).Included.Eval(t.Ctx())
	t.Require().NoError(err)
	t.Require().Equal(types.ReceiptStatusSuccessful, receipt.Status, "calldata tx must succeed")
	return receipt.GasUsed
}
