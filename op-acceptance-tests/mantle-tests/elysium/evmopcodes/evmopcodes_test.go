package evmopcodes

import (
	"testing"

	"github.com/ethereum-optimism/optimism/op-devstack/devtest"
	"github.com/ethereum-optimism/optimism/op-devstack/presets"
	"github.com/ethereum-optimism/optimism/op-service/eth"
	"github.com/ethereum-optimism/optimism/op-service/txplan"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
)

// deployRuntimeInitCode returns EVM init code that, when run as a CREATE, stores
// the given runtime blob verbatim as the new contract's code. The init code only
// CODECOPYs and RETURNs the blob — it never executes it — so deployment succeeds
// even when the runtime contains an opcode the current EVM does not implement.
func deployRuntimeInitCode(runtime []byte) []byte {
	n := len(runtime)
	hi, lo := byte(n>>8), byte(n)
	prefix := []byte{
		0x61, hi, lo, // PUSH2 n
		0x60, 0x00, //   PUSH1 <prefixLen>  (patched below to the runtime offset)
		0x60, 0x00, //   PUSH1 0
		0x39,         //   CODECOPY   mem[0:n] = code[prefixLen:prefixLen+n]
		0x61, hi, lo, // PUSH2 n
		0x60, 0x00, //   PUSH1 0
		0xf3, //         RETURN     mem[0:n]
	}
	prefix[4] = byte(len(prefix)) // the runtime blob starts right after the prefix
	return append(prefix, runtime...)
}

// TestL2EVM_NoNewOpcodes asserts the Mantle L2 EVM stays on Arsia rules and does
// NOT implement the EIP-8024 stack opcodes (DUPN=0xe6, SWAPN=0xe7, EXCHANGE=0xe8)
// even while the L1 runs Glamsterdam (Amsterdam). Those bytes are undefined
// pre-Osaka, so they must be invalid on Arsia.
//
// For each opcode we deploy a probe contract whose runtime code executes that
// opcode, then call it. Deployment must succeed (the init code only stores the
// blob), but the subsequent call must revert (Status == Failed) because the EVM
// hits an invalid/undefined opcode. If the L2 had silently adopted Amsterdam the
// opcode would be defined and the call would not fail this way.
//
// COVERAGE: this is the EIP-8024 opcode sub-case only. The gas sub-cases live in sibling
// packages: EIP-7976 calldata floor (evmgas), EIP-7981 access-list (evmaccesslist),
// EIP-7778 block-level gas accounting (evmblockgas).
func TestL2EVM_NoNewOpcodes(gt *testing.T) {
	t := devtest.SerialT(gt)
	sys := presets.NewMantleMinimal(t)
	require := t.Require()
	ctx := t.Ctx()

	// Ensure the L1 has actually upgraded to Glamsterdam before probing the L2.
	l1Config := sys.L1Network.Escape().ChainConfig()
	require.NotNil(l1Config.AmsterdamTime, "L1 AmsterdamTime must be configured")
	sys.L1EL.WaitForTime(*l1Config.AmsterdamTime)

	wallet := sys.FunderL2.NewFundedEOA(eth.OneEther)

	newOpcodes := []struct {
		name string
		op   byte
	}{
		{"DUPN", 0xe6},
		{"SWAPN", 0xe7},
		{"EXCHANGE", 0xe8},
	}

	for _, tc := range newOpcodes {
		// runtime = PUSH1 0 ; PUSH1 0 ; <newopcode> ; STOP
		// The two PUSHes give the opcode stack operands to consume; on Arsia the
		// interpreter rejects the undefined opcode before it matters, and if it
		// were somehow defined the STOP would let a well-formed run finish cleanly.
		runtime := []byte{0x60, 0x00, 0x60, 0x00, tc.op, 0x00}

		// Deploy the probe contract. Creation must succeed: the init code only
		// RETURNs the runtime blob, it does not run the new opcode.
		deploy, err := txplan.NewPlannedTx(txplan.Combine(
			wallet.Plan(),
			txplan.WithData(deployRuntimeInitCode(runtime)),
			txplan.WithGasLimit(1_000_000),
		)).Included.Eval(ctx)
		require.NoError(err, "deploy tx for %s probe must be included", tc.name)
		require.Equal(types.ReceiptStatusSuccessful, deploy.Status, "deploying the %s probe contract must succeed", tc.name)
		require.NotEqual(common.Address{}, deploy.ContractAddress, "%s probe contract must have an address", tc.name)

		// Call the deployed contract. Explicit gas is set because an invalid
		// opcode consumes all supplied gas; the call must revert on Arsia.
		addr := deploy.ContractAddress
		call, err := txplan.NewPlannedTx(txplan.Combine(
			wallet.Plan(),
			txplan.WithTo(&addr),
			txplan.WithGasLimit(1_000_000),
		)).Included.Eval(ctx)
		require.NoError(err, "call tx for %s must be included even though it reverts", tc.name)
		require.Equal(types.ReceiptStatusFailed, call.Status, "calling %s (0x%x) must revert on Arsia — the opcode is not supported", tc.name, tc.op)
	}
}
