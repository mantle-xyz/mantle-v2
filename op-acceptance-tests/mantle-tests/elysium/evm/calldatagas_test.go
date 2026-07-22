package evm

import (
	"testing"

	"github.com/ethereum-optimism/optimism/op-acceptance-tests/mantle-tests/elysium/internal/testhelpers"
	"github.com/ethereum-optimism/optimism/op-devstack/devtest"
	"github.com/ethereum-optimism/optimism/op-devstack/dsl"
	"github.com/ethereum-optimism/optimism/op-devstack/presets"
	"github.com/ethereum-optimism/optimism/op-service/eth"
	"github.com/ethereum-optimism/optimism/op-service/txplan"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
)

// Calldata gas constants for the Mantle-Arsia L2 floor: 10 gas per token and
// 4 tokens per non-zero byte, or 40 gas/byte.
const (
	txBaseGas            = 21_000 // params.TxGas
	tokensPerNonZeroByte = 4      // params.TxTokenPerNonZeroByte (EIP-7623)
	arsiaFloorPerToken   = 10     // params.TxCostFloorPerToken (EIP-7623)

	// arsiaGasPerNonZeroByte is the Mantle-Arsia calldata floor: 10 * 4 = 40 gas/byte.
	arsiaGasPerNonZeroByte = arsiaFloorPerToken * tokensPerNonZeroByte
)

// TestL2EVM_CalldataGasStaysArsia checks that non-zero calldata remains priced
// at the Arsia 40 gas/byte floor while the L1 runs Glamsterdam. Sibling evm
// packages cover access-list pricing, block-gas accounting, and opcodes.
func runCalldataGasStaysArsia(gt *testing.T) {
	t := devtest.SerialT(gt)
	sys := presets.NewMantleMinimal(t)
	require := t.Require()

	// Exercise the L2 gas schedule while consuming an upgraded L1.
	l1Config := sys.L1Network.Escape().ChainConfig()
	require.NotNil(l1Config.AmsterdamTime, "L1 AmsterdamTime must be configured")
	testhelpers.WaitForGlamsterdamL1(t, sys.L1EL, *l1Config.AmsterdamTime)

	wallet := sys.FunderL2.NewFundedEOA(eth.OneEther)

	// A value-less call to a plain EOA leaves intrinsic calldata cost isolated.
	recipient := common.HexToAddress("0x00000000000000000000000000000000C0FFEE00")

	const (
		smallLen = 1000
		largeLen = 2000
	)

	smallGas := sendNonZeroCalldata(t, wallet, recipient, smallLen)
	largeGas := sendNonZeroCalldata(t, wallet, recipient, largeLen)

	// Exact Arsia cost: 21000 base + 40 gas per non-zero byte.
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
