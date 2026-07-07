package l2output

import (
	"testing"

	"github.com/ethereum-optimism/optimism/op-devstack/devtest"
	"github.com/ethereum-optimism/optimism/op-devstack/presets"
	"github.com/ethereum-optimism/optimism/op-service/eth"
	"github.com/ethereum-optimism/optimism/op-service/txplan"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
)

// deployInitCode returns EVM init code that deploys runtimeLen bytes of runtime
// code (all STOP). The size of the returned runtime code is what EIP-170 bounds.
func deployInitCode(runtimeLen int) []byte {
	hi, lo := byte(runtimeLen>>8), byte(runtimeLen)
	prefix := []byte{
		0x61, hi, lo, // PUSH2 runtimeLen
		0x60, 0x00, //   PUSH1 <prefixLen>  (patched below to the runtime offset)
		0x60, 0x00, //   PUSH1 0
		0x39,       //   CODECOPY   mem[0:runtimeLen] = code[prefixLen:prefixLen+runtimeLen]
		0x61, hi, lo, // PUSH2 runtimeLen
		0x60, 0x00, //   PUSH1 0
		0xf3, //         RETURN     mem[0:runtimeLen]
	}
	prefix[4] = byte(len(prefix)) // the runtime blob starts right after the prefix
	return append(prefix, make([]byte, runtimeLen)...)
}

// TestL2CodeSizeStillOldLimit asserts the Mantle L2 keeps the EIP-170 24 KiB
// contract code-size limit while the L1 runs Glamsterdam — i.e. it does not adopt
// any Amsterdam-era code-size change.
func TestL2CodeSizeStillOldLimit(gt *testing.T) {
	t := devtest.SerialT(gt)
	sys := presets.NewMantleMinimal(t)
	require := t.Require()
	ctx := t.Ctx()

	l1Config := sys.L1Network.Escape().ChainConfig()
	require.NotNil(l1Config.AmsterdamTime, "L1 AmsterdamTime must be configured")
	sys.L1EL.WaitForTime(*l1Config.AmsterdamTime)

	wallet := sys.FunderL2.NewFundedEOA(eth.OneEther)

	const eip170Limit = 24576

	// A contract exactly at the limit must still deploy (control).
	atLimit, err := txplan.NewPlannedTx(txplan.Combine(
		wallet.Plan(),
		txplan.WithData(deployInitCode(eip170Limit)),
		txplan.WithGasLimit(20_000_000),
	)).Included.Eval(ctx)
	require.NoError(err)
	require.Equal(types.ReceiptStatusSuccessful, atLimit.Status, "a 24 KiB contract must still deploy on L2")
	require.NotEqual(common.Address{}, atLimit.ContractAddress, "deployed contract must have an address")

	// One byte over the limit must be rejected (EIP-170 not raised on the L2).
	overLimit, err := txplan.NewPlannedTx(txplan.Combine(
		wallet.Plan(),
		txplan.WithData(deployInitCode(eip170Limit+1)),
		txplan.WithGasLimit(20_000_000),
	)).Included.Eval(ctx)
	require.NoError(err, "the deploy tx must be included even though creation fails")
	require.Equal(types.ReceiptStatusFailed, overLimit.Status, "L2 (Arsia) must reject a >24 KiB contract")
}
