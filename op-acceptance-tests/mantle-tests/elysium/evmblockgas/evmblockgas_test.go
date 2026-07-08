package evmblockgas

import (
	"testing"

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
// Clearing one storage slot from non-zero to zero grants a deterministic 4800-gas
// refund. For a single-clear transaction the total gas used (~26k) makes the
// EIP-3529 refund cap (gasUsed / RefundQuotientEIP3529=5, so ~5.2k) larger than
// 4800, so the full 4800 refund is applied uncapped.
const eip3529ClearRefund = uint64(4800)

// TestL2BlockGas_RefundReducesBlockGasUsed asserts the Mantle L2 keeps Arsia
// block-level gas accounting while the L1 runs Glamsterdam — it does NOT adopt
// EIP-7778 "block-level gas accounting".
//
// WHAT EIP-7778 CHANGES (verified in op-geth, sibling clone, branch mantle-elysium):
//   - The whole feature is the fork-gated branch in core/state_transition.go:991-997.
//     Pre-Amsterdam (Arsia) returns `st.gasRemaining.RegularGas` to the block gas
//     pool — and that value has the refund folded in (the refund was added to
//     gasRemaining at core/state_transition.go:960 via st.gasRemaining.Refund).
//     Post-Amsterdam (EIP-7778) returns `st.initialBudget.RegularGas - peakGasUsed`,
//     where peakGasUsed (core/state_transition.go:955) EXCLUDES the refund.
//   - core/gaspool.go documents the two regimes on ReturnGas (gaspool.go:52-69):
//     pre-Amsterdam `returned = purchased - remaining` (refund included),
//     post-Amsterdam `returned = purchased - gasUsed` (refund excluded).
//   - The block header GasUsed is gp.Used() (= initial - remaining),
//     core/state_processor.go:137. The receipt CumulativeGasUsed is
//     gp.CumulativeUsed() (= sum of per-tx gasUsed, refund-reduced in BOTH forks),
//     core/state_processor.go:207 + gaspool.go:64.
//
// THE DISCRIMINATING IDENTITY:
//   - Arsia (what the L2 must keep): the refund is credited back to the block gas
//     pool, so gp.Used() == gp.CumulativeUsed(). Observably, for the block that
//     contains a refunding transaction:
//     block.header.GasUsed == last-receipt.CumulativeGasUsed   (EXACTLY EQUAL).
//   - EIP-7778 (Amsterdam, what the L2 must NOT adopt): the refund is NOT credited
//     to the pool, so the block gas used is HIGHER by the total refund:
//     block.header.GasUsed == last-receipt.CumulativeGasUsed + totalRefund.
//     With our single-slot clear the gap is exactly eip3529ClearRefund = 4800.
//
// So the assertion `blockGasUsed == cumulative` PASSES on Arsia and FAILS on
// EIP-7778 (it would be short by 4800). This identity is refund-independent in
// magnitude — it holds for the whole block regardless of tx count — but it only
// *discriminates* when the block actually contains a refund, which we guarantee
// by construction and confirm by observing the storage slot go 1 -> 0.
//
// COVERAGE: this is the EIP-7778 block-level gas accounting sub-case. The other gas
// sub-cases live in sibling packages: EIP-7976 calldata floor (evmgas), EIP-7981
// access-list repricing (evmaccesslist), EIP-8024 opcodes (evmopcodes).
func TestL2BlockGas_RefundReducesBlockGasUsed(gt *testing.T) {
	t := devtest.SerialT(gt)
	sys := presets.NewMantleMinimal(t)
	require := t.Require()
	ctx := t.Ctx()

	// Ensure the L1 has actually crossed into Glamsterdam (Amsterdam) so this
	// asserts the L2 stays on Arsia *while consuming an upgraded L1*.
	l1Config := sys.L1Network.Escape().ChainConfig()
	require.NotNil(l1Config.AmsterdamTime, "L1 AmsterdamTime must be configured")
	sys.L1EL.WaitForTime(*l1Config.AmsterdamTime)

	wallet := sys.FunderL2.NewFundedEOA(eth.OneEther)
	l2 := sys.L2EL.Escape().EthClient()

	// slot0 is the single storage slot our probe contract sets non-zero in its
	// constructor and then clears on call.
	slot0 := common.Hash{}

	// Deploy the "armed clearer": its constructor sets slot0 = 1 (non-zero) so the
	// first call can clear it and earn the EIP-3529 refund.
	deploy, err := txplan.NewPlannedTx(txplan.Combine(
		wallet.Plan(),
		txplan.WithData(armedClearerInitCode()),
		txplan.WithGasLimit(1_000_000),
	)).Included.Eval(ctx)
	require.NoError(err, "deploy tx must be included")
	require.Equal(types.ReceiptStatusSuccessful, deploy.Status, "deploying the clearer contract must succeed")
	require.NotEqual(common.Address{}, deploy.ContractAddress, "clearer contract must have an address")
	addr := deploy.ContractAddress

	// LIVENESS PRECONDITION: the constructor must have armed slot0 to non-zero,
	// otherwise the later "clear" is a no-op and grants no refund (test vacuous).
	armed, err := l2.GetStorageAt(ctx, addr, slot0, "latest")
	require.NoError(err)
	require.NotEqual(common.Hash{}, armed, "constructor must arm slot0 to a non-zero value")

	// Send the refunding transaction: calling the contract runs SSTORE(slot0, 0),
	// clearing a non-zero slot -> a deterministic 4800-gas EIP-3529 refund.
	clear, err := txplan.NewPlannedTx(txplan.Combine(
		wallet.Plan(),
		txplan.WithTo(&addr),
		txplan.WithGasLimit(1_000_000),
	)).Included.Eval(ctx)
	require.NoError(err, "clear tx must be included")
	require.Equal(types.ReceiptStatusSuccessful, clear.Status, "clear call must succeed")

	// LIVENESS PROOF: slot0 went 1 -> 0, so an SSTORE storage-clear happened this
	// tx and the protocol granted the 4800-gas refund. The block below therefore
	// definitely contains a refund, so the identity check genuinely discriminates.
	cleared, err := l2.GetStorageAt(ctx, addr, slot0, "latest")
	require.NoError(err)
	require.Equal(common.Hash{}, cleared, "clear call must zero slot0 (a real storage-clear refund occurred)")

	// Read the block that contains the refunding tx and the max CumulativeGasUsed
	// across its receipts (== the last tx's cumulative == sum of every tx's
	// refund-reduced GasUsed in the block).
	blockHash := clear.BlockHash
	blockInfo, err := l2.InfoByHash(ctx, blockHash)
	require.NoError(err, "must fetch the block containing the clear tx")
	blockGasUsed := blockInfo.GasUsed()

	var receipts []*types.Receipt
	err = l2.RPC().CallContext(ctx, &receipts, "eth_getBlockReceipts", blockHash)
	require.NoError(err, "must fetch the block receipts")
	require.NotEmpty(receipts, "block must contain receipts")
	cumulative := maxCumulativeGasUsed(receipts)

	// THE DISCRIMINATOR. Arsia: the refund is credited back to the block gas pool,
	// so block.GasUsed == cumulative EXACTLY. EIP-7778 would leave block.GasUsed
	// higher by the block's total refund (here exactly eip3529ClearRefund = 4800),
	// so this equality would FAIL under Amsterdam.
	require.EqualValues(cumulative, blockGasUsed,
		"L2 block GasUsed must equal cumulative receipt gas (Arsia): the storage-clear "+
			"refund is credited back to the block gas pool. Under EIP-7778 block GasUsed "+
			"would be cumulative + %d (refund excluded from the pool).", eip3529ClearRefund)

	// Guard against a vacuous pass: on the EIP-7778 (Amsterdam) accounting the
	// block gas used would have been this larger value, which must differ from the
	// Arsia value we just asserted.
	amsterdamBlockGasUsed := cumulative + eip3529ClearRefund
	require.NotEqualValues(amsterdamBlockGasUsed, blockGasUsed,
		"if the L2 had adopted EIP-7778, block GasUsed would be cumulative + %d", eip3529ClearRefund)

	t.Log("L2 block-level gas accounting stays on Arsia while L1 runs Glamsterdam",
		"blockGasUsed", blockGasUsed,
		"cumulativeReceiptGas", cumulative,
		"arsiaIdentityHolds", blockGasUsed == cumulative,
		"eip7778WouldBe", amsterdamBlockGasUsed,
		"refundExcludedByEip7778", eip3529ClearRefund)
}

// maxCumulativeGasUsed returns the largest CumulativeGasUsed among the receipts,
// which (because cumulative gas is monotonic in tx index) is the last tx's
// cumulative — i.e. the sum of every transaction's refund-reduced GasUsed in the
// block. Taking the max makes the check robust to which tx is last.
func maxCumulativeGasUsed(receipts []*types.Receipt) uint64 {
	var m uint64
	for _, r := range receipts {
		if r.CumulativeGasUsed > m {
			m = r.CumulativeGasUsed
		}
	}
	return m
}

// armedClearerInitCode returns EVM init code (CREATE payload) for a probe contract
// that:
//   - in its constructor sets storage slot 0 to 1 (non-zero) — "arming" it, and
//   - installs runtime code that, when called, runs SSTORE(0, 0) to clear slot 0.
//
// The first call therefore clears a non-zero slot and earns the deterministic
// EIP-3529 storage-clear refund (4800 gas), which is exactly the refund whose
// block-level accounting EIP-7778 changes.
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
