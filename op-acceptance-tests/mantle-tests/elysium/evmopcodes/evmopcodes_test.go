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
// NOT implement the EIP-8024 stack opcodes (DUPN=0xe6, SWAPN=0xe7, EXCHANGE=0xe8).
// The L2 EVM opcode set is a function of the L2 fork alone: these bytes are
// undefined pre-Osaka, so on an Arsia L2 they are invalid no matter what the L1
// runs. The test exercises the property while the L1 runs Glamsterdam (Amsterdam),
// but the L1 is the environment here, not the discriminator — the same assertion
// holds under any L1.
//
// For each opcode we deploy a probe contract whose runtime code executes that
// opcode, then call it. Deployment must succeed (the init code only stores the
// blob), but the subsequent call must revert (Status == Failed) because the EVM
// hits an invalid/undefined opcode.
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
		// runtime = PUSH1 0 x4 ; <newopcode> ; <immediate 0x00> ; STOP
		//
		// The stack depth and the trailing immediate both matter for the assertion to
		// DISCRIMINATE rather than merely fail. EIP-8024's opcodes take an immediate operand
		// from the byte that follows them, and with immediate 0x00 they address: DUPN the 1st
		// stack item, SWAPN the 1st and 2nd, EXCHANGE the 2nd and 3rd. Four PUSHes therefore
		// leave every one of them with enough operands to run cleanly IF the opcode is
		// implemented, so the call would SUCCEED on an EIP-8024 EVM and the ReceiptStatusFailed
		// assertion below would go red.
		//
		// An earlier version pushed only two values. That was enough for DUPN and SWAPN but not
		// for EXCHANGE, which would have stack-underflowed — and therefore reverted — even on an
		// EIP-8024 EVM, making that third sub-case pass for the wrong reason.
		runtime := []byte{0x60, 0x00, 0x60, 0x00, 0x60, 0x00, 0x60, 0x00, tc.op, 0x00, 0x00}

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
		const callGasLimit = uint64(1_000_000)
		addr := deploy.ContractAddress
		call, err := txplan.NewPlannedTx(txplan.Combine(
			wallet.Plan(),
			txplan.WithTo(&addr),
			txplan.WithGasLimit(callGasLimit),
		)).Included.Eval(ctx)
		require.NoError(err, "call tx for %s must be included even though it reverts", tc.name)
		require.Equal(types.ReceiptStatusFailed, call.Status, "calling %s (0x%x) must revert on Arsia — the opcode is not supported", tc.name, tc.op)

		// Status alone does not say WHY the call failed — a plain REVERT, an out-of-gas, or a
		// bad deployment would satisfy it equally. An undefined opcode is specifically an
		// ErrInvalidOpCode, which consumes the entire gas allowance rather than refunding the
		// unused remainder, so requiring the full limit to be burnt pins the failure to the
		// opcode itself. The probe is 11 bytes and cannot plausibly consume 1M gas any other way.
		require.EqualValuesf(callGasLimit, call.GasUsed,
			"calling %s (0x%x) must fail as an invalid opcode, which consumes the whole gas "+
				"allowance; a partial burn of %d/%d means it failed for some other reason",
			tc.name, tc.op, call.GasUsed, callGasLimit)
	}
}
