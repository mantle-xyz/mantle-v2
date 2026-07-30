package evm

import (
	"testing"

	"github.com/ethereum-optimism/optimism/op-acceptance-tests/mantle-tests/elysium/internal/testhelpers"
	"github.com/ethereum-optimism/optimism/op-devstack/devtest"
	"github.com/ethereum-optimism/optimism/op-devstack/presets"
	"github.com/ethereum-optimism/optimism/op-service/eth"
	"github.com/ethereum-optimism/optimism/op-service/txplan"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
)

// EIP-3529 storage-clear refund (op-geth params/protocol_params.go:88):
//
//	SstoreClearsScheduleRefundEIP3529 = SstoreResetGasEIP2200(5000) -
//	    ColdSloadCostEIP2929(2100) + TxAccessListStorageKeyGas(1900) = 4800
//
// A single storage clear grants the full 4800-gas refund under the EIP-3529 cap.
const eip3529ClearRefund = uint64(4800)

// TestL2BlockGas_RefundReducesBlockGasUsed verifies an L2-internal Arsia property: storage-clear
// refunds are still credited back to the block gas pool while the L1 runs Glamsterdam.
//
// The probe clears one non-zero storage slot. Arsia credits the refund back to
// the block gas pool; EIP-7778 would leave block GasUsed higher by 4800.
func runBlockGasRefundCredited(gt *testing.T) {
	t := devtest.SerialT(gt)
	sys := presets.NewMantleMinimal(t)
	require := t.Require()
	ctx := t.Ctx()

	// Exercise the L2-internal property while consuming an upgraded L1.
	l1Config := sys.L1Network.Escape().ChainConfig()
	require.NotNil(l1Config.AmsterdamTime, "L1 AmsterdamTime must be configured")
	testhelpers.WaitForGlamsterdamL1(t, sys.L1EL, *l1Config.AmsterdamTime)

	wallet := sys.FunderL2.NewFundedEOA(eth.OneEther)
	l2 := sys.L2EL.Escape().EthClient()

	// slot0 is armed in the constructor and cleared on call.
	slot0 := common.Hash{}

	// Deploy the "armed clearer" probe contract.
	deploy, err := txplan.NewPlannedTx(txplan.Combine(
		wallet.Plan(),
		txplan.WithData(armedClearerInitCode()),
		txplan.WithGasLimit(1_000_000),
	)).Included.Eval(ctx)
	require.NoError(err, "deploy tx must be included")
	require.Equal(types.ReceiptStatusSuccessful, deploy.Status, "deploying the clearer contract must succeed")
	require.NotEqual(common.Address{}, deploy.ContractAddress, "clearer contract must have an address")
	addr := deploy.ContractAddress

	// Guard against a vacuous clear: slot0 must start non-zero.
	armed, err := l2.GetStorageAt(ctx, addr, slot0, "latest")
	require.NoError(err)
	require.NotEqual(common.Hash{}, armed, "constructor must arm slot0 to a non-zero value")

	// Calling the contract runs SSTORE(slot0, 0), earning the EIP-3529 refund.
	clear, err := txplan.NewPlannedTx(txplan.Combine(
		wallet.Plan(),
		txplan.WithTo(&addr),
		txplan.WithGasLimit(1_000_000),
	)).Included.Eval(ctx)
	require.NoError(err, "clear tx must be included")
	require.Equal(types.ReceiptStatusSuccessful, clear.Status, "clear call must succeed")

	// Confirm the transaction really cleared storage and earned a refund.
	cleared, err := l2.GetStorageAt(ctx, addr, slot0, "latest")
	require.NoError(err)
	require.Equal(common.Hash{}, cleared, "clear call must zero slot0 (a real storage-clear refund occurred)")

	// Compare block GasUsed with the max receipt cumulative gas in that block.
	blockHash := clear.BlockHash
	blockInfo, err := l2.InfoByHash(ctx, blockHash)
	require.NoError(err, "must fetch the block containing the clear tx")
	blockGasUsed := blockInfo.GasUsed()

	var receipts []*types.Receipt
	err = l2.RPC().CallContext(ctx, &receipts, "eth_getBlockReceipts", blockHash)
	require.NoError(err, "must fetch the block receipts")
	require.NotEmpty(receipts, "block must contain receipts")
	cumulative := maxCumulativeGasUsed(receipts)

	// Arsia credits the refund back to the block gas pool, so this equality holds.
	require.EqualValues(cumulative, blockGasUsed,
		"L2 block GasUsed must equal cumulative receipt gas (Arsia): the storage-clear "+
			"refund is credited back to the block gas pool. Under EIP-7778 block GasUsed "+
			"would be cumulative + %d (refund excluded from the pool).", eip3529ClearRefund)

	// Logged for reference only; the equality above already rules this value out.
	amsterdamBlockGasUsed := cumulative + eip3529ClearRefund

	t.Log("L2 block-level gas accounting stays on Arsia while L1 runs Glamsterdam",
		"blockGasUsed", blockGasUsed,
		"cumulativeReceiptGas", cumulative,
		"arsiaIdentityHolds", blockGasUsed == cumulative,
		"eip7778WouldBe", amsterdamBlockGasUsed,
		"refundExcludedByEip7778", eip3529ClearRefund)
}

// maxCumulativeGasUsed returns the block's cumulative receipt gas.
func maxCumulativeGasUsed(receipts []*types.Receipt) uint64 {
	var m uint64
	for _, r := range receipts {
		if r.CumulativeGasUsed > m {
			m = r.CumulativeGasUsed
		}
	}
	return m
}

// armedClearerInitCode deploys a probe whose first call clears storage slot 0.
func armedClearerInitCode() []byte {
	// runtime: PUSH1 0x00 ; PUSH1 0x00 ; SSTORE ; STOP  -> slot0 = 0 (clear)
	runtime := []byte{0x60, 0x00, 0x60, 0x00, 0x55, 0x00}
	// constructor: PUSH1 0x01 ; PUSH1 0x00 ; SSTORE     -> slot0 = 1 (arm)
	ctor := []byte{0x60, 0x01, 0x60, 0x00, 0x55}

	n := len(runtime)
	hi, lo := byte(n>>8), byte(n)
	// return-runtime trailer: CODECOPY the runtime blob out of the init code and
	// RETURN it as the deployed code.
	ret := []byte{
		0x61, hi, lo, // PUSH2 n
		0x60, 0x00, //   PUSH1 <off>   (patched below to the runtime offset)
		0x60, 0x00, //   PUSH1 0
		0x39,         // CODECOPY  mem[0:n] = code[off:off+n]
		0x61, hi, lo, // PUSH2 n
		0x60, 0x00, //   PUSH1 0
		0xf3, //         RETURN    mem[0:n]
	}

	init := append(append([]byte{}, ctor...), ret...)
	off := len(init) // the runtime blob starts right after ctor+ret
	// patch the PUSH1 <off> operand (the byte right after the PUSH1 at ret[3]).
	init[len(ctor)+4] = byte(off)
	return append(init, runtime...)
}
