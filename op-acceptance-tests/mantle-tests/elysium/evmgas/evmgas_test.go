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
// params/protocol_params.go). Mantle's L2 stays on Arsia rules, which price the
// EIP-7623 calldata floor at TxCostFloorPerToken = 10 gas per token, where a
// non-zero byte is worth TxTokenPerNonZeroByte = 4 tokens. EIP-7976
// (Amsterdam/Glamsterdam, gated on rules.IsAmsterdam) raises the floor to
// TxCostFloorPerToken7976 = 16 gas per token. A value-less call to a plain EOA
// runs no code, so the data floor dominates the 16 gas/byte EIP-2028 standard
// cost, and the marginal gas of one extra non-zero calldata byte equals exactly
// floorPerToken * tokensPerNonZeroByte — the value that distinguishes the two
// forks.
const (
	txBaseGas            = 21_000 // params.TxGas
	tokensPerNonZeroByte = 4      // params.TxTokenPerNonZeroByte (EIP-7623)
	arsiaFloorPerToken   = 10     // params.TxCostFloorPerToken (EIP-7623, pre-Amsterdam)
	eip7976FloorPerToken = 16     // params.TxCostFloorPerToken7976 (EIP-7976, Amsterdam)

	// arsiaGasPerNonZeroByte is the Mantle-Arsia floor: 10 * 4 = 40 gas/byte.
	arsiaGasPerNonZeroByte = arsiaFloorPerToken * tokensPerNonZeroByte
	// eip7976GasPerNonZeroByte is the raised floor the L2 must NOT adopt: 16 * 4 = 64 gas/byte.
	eip7976GasPerNonZeroByte = eip7976FloorPerToken * tokensPerNonZeroByte
)

// TestL2EVM_CalldataGasStaysArsia asserts the Mantle L2 keeps Arsia calldata
// pricing while the L1 runs Glamsterdam — it does NOT adopt EIP-7976's raised
// calldata floor.
//
// It sends two value-less calls to the same plain EOA carrying different amounts
// of non-zero calldata. Because such a call executes no code, the EIP-7623 data
// floor governs the cost, so the marginal gas of one extra non-zero calldata byte
// reveals the floor rate directly: 40 gas/byte under Arsia (10 * 4) versus 64
// gas/byte under EIP-7976 (16 * 4). Using the marginal cancels the 21000 base and
// any fixed per-recipient cost, isolating the floor rate. (The non-floor EIP-2028
// standard rate of 16 gas/byte is identical on both forks, so it cannot
// discriminate — only the floor regime can.)
//
// COVERAGE: this covers ONLY the EIP-7976 calldata-floor sub-case. The other "no SFI
// behaviour" gas sub-cases live in sibling packages: EIP-7981 access-list repricing
// (evmaccesslist), EIP-7778 block-level gas accounting (evmblockgas), EIP-8024 opcodes
// (evmopcodes).
func TestL2EVM_CalldataGasStaysArsia(gt *testing.T) {
	t := devtest.SerialT(gt)
	sys := presets.NewMantleMinimal(t)
	require := t.Require()

	// Ensure the L1 has actually crossed into Glamsterdam (Amsterdam) so this
	// asserts the L2 stays on Arsia *while consuming an upgraded L1*.
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

	// Exact per-tx cost for a value-less EOA call: 21000 + floor, floor = 40/byte on Arsia.
	require.EqualValues(txBaseGas+arsiaGasPerNonZeroByte*smallLen, smallGas,
		"small-calldata tx gas must match Arsia floor pricing (40 gas/non-zero byte)")
	require.EqualValues(txBaseGas+arsiaGasPerNonZeroByte*largeLen, largeGas,
		"large-calldata tx gas must match Arsia floor pricing (40 gas/non-zero byte)")

	// The relationship that discriminates Arsia from EIP-7976: the marginal gas of
	// one extra non-zero calldata byte. The 21000 base and any fixed execution
	// cancel out, leaving just the floor rate.
	require.Greater(largeGas, smallGas, "more calldata must cost more gas")
	deltaGas := largeGas - smallGas
	deltaBytes := uint64(largeLen - smallLen)
	require.Zero(deltaGas%deltaBytes, "marginal gas should be a whole number of gas per byte")
	marginal := deltaGas / deltaBytes

	require.EqualValues(arsiaGasPerNonZeroByte, marginal,
		"L2 must price non-zero calldata at the Arsia floor (40 gas/byte)")
	require.Less(marginal, uint64(eip7976GasPerNonZeroByte),
		"L2 must NOT adopt EIP-7976's raised calldata floor (64 gas/byte)")

	t.Log("Calldata gas stays on Arsia pricing while L1 runs Glamsterdam",
		"smallGas", smallGas,
		"largeGas", largeGas,
		"marginalPerNonZeroByte", marginal,
		"arsiaExpected", arsiaGasPerNonZeroByte,
		"eip7976Rejected", eip7976GasPerNonZeroByte)
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
