package evmaccesslist

import (
	"fmt"
	"testing"

	"github.com/ethereum-optimism/optimism/op-devstack/devtest"
	"github.com/ethereum-optimism/optimism/op-devstack/dsl"
	"github.com/ethereum-optimism/optimism/op-devstack/presets"
	"github.com/ethereum-optimism/optimism/op-service/eth"
	"github.com/ethereum-optimism/optimism/op-service/txplan"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
)

// Access-list gas constants (see op-geth core/state_transition.go IntrinsicGas and
// params/protocol_params.go).
//
// EIP-7981 ("Increase access list cost") is gated on rules.IsAmsterdam in op-geth:
//   - IntrinsicGas: core/state_transition.go:145 (called at :828 with rules.IsAmsterdam)
//   - FloorDataGas: core/state_transition.go:182
//
// Under Mantle-Arsia rules (L2 AmsterdamTime == nil, so rules.IsAmsterdam == false)
// an access-list entry is charged ONLY the base EIP-2930 cost:
//   - per address:     TxAccessListAddressGas    = 2400 gas
//   - per storage key: TxAccessListStorageKeyGas = 1900 gas
//
// When Amsterdam/EIP-7981 is active, op-geth adds an EXTRA per-entry charge
// (state_transition.go:146-157):
//   - addressCost    = AddressLength(20) * TxCostFloorPerToken7976(16) * TxTokenPerNonZeroByte(4) = 1280
//   - storageKeyCost = HashLength(32)    * TxCostFloorPerToken7976(16) * TxTokenPerNonZeroByte(4) = 2048
//
// So the per-address cost the L2 must keep is 2400 (Arsia); EIP-7981 would make it
// 2400 + 1280 = 3680. That 2400-vs-3680 gap is the discriminator.
const (
	txBaseGas = 21_000 // params.TxGas

	// arsiaGasPerAddress is the Mantle-Arsia per-access-list-address cost.
	arsiaGasPerAddress = 2400 // params.TxAccessListAddressGas
	// eip7981GasPerAddress is the raised per-address cost the L2 must NOT adopt.
	// 2400 (base) + 20*16*4 (EIP-7981 extra, = 1280) = 3680.
	eip7981GasPerAddress = 3680
)

// TestL2EVM_AccessListGasStaysArsia asserts the Mantle L2 keeps Arsia access-list
// pricing while the L1 runs Glamsterdam — it does NOT adopt EIP-7981's raised
// access-list cost (§3-5 gas repricing).
//
// It sends two value-less calls to the same plain EOA, each carrying an EIP-2930
// access list with a different number of addresses (and zero storage keys). A
// value-less call to an EOA executes no code, and none of the listed addresses is
// ever touched by an opcode, so warming them yields no execution discount: the
// access-list charge is purely additive on top of the 21000 base. That makes the
// receipt's GasUsed exactly 21000 + N*perAddress, and the marginal gas of one
// extra access-list address reveals the per-address rate directly — 2400 gas under
// Arsia versus 3680 under EIP-7981. Using the marginal cancels the 21000 base and
// any fixed per-recipient cost, isolating the access-list rate.
//
// The txs carry the access list on the default DynamicFee (EIP-1559) type; the
// EIP-2930 entries flow through the same IntrinsicGas access-list branch
// (state_transition.go:132-159) that EIP-7981 modifies regardless of tx type.
//
// COVERAGE (§3-5): this covers the EIP-7981 access-list-repricing sub-case that the
// evmgas (EIP-7976 calldata floor) package explicitly left open. It does NOT cover
// EIP-8037 gas repricing or EIP-7778 block-level gas accounting, which remain open
// items on the §3-5 status row.
func TestL2EVM_AccessListGasStaysArsia(gt *testing.T) {
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
	// gas is intrinsic + access-list only.
	recipient := common.HexToAddress("0x00000000000000000000000000000000C0FFEE00")

	const (
		smallAddrs = 5
		largeAddrs = 15
	)

	smallGas := sendWithAccessListAddresses(t, wallet, recipient, smallAddrs)
	largeGas := sendWithAccessListAddresses(t, wallet, recipient, largeAddrs)

	// Exact per-tx cost for a value-less EOA call carrying only access-list
	// addresses: 21000 + N*2400 on Arsia.
	require.EqualValues(txBaseGas+arsiaGasPerAddress*smallAddrs, smallGas,
		"small access-list tx gas must match Arsia pricing (2400 gas/address)")
	require.EqualValues(txBaseGas+arsiaGasPerAddress*largeAddrs, largeGas,
		"large access-list tx gas must match Arsia pricing (2400 gas/address)")

	// The relationship that discriminates Arsia from EIP-7981: the marginal gas of
	// one extra access-list address. The 21000 base and any fixed execution cancel
	// out, leaving just the per-address rate.
	require.Greater(largeGas, smallGas, "more access-list addresses must cost more gas")
	deltaGas := largeGas - smallGas
	deltaAddrs := uint64(largeAddrs - smallAddrs)
	require.Zero(deltaGas%deltaAddrs, "marginal gas should be a whole number of gas per address")
	marginal := deltaGas / deltaAddrs

	require.EqualValues(arsiaGasPerAddress, marginal,
		"L2 must price access-list addresses at the Arsia rate (2400 gas/address)")
	require.Less(marginal, uint64(eip7981GasPerAddress),
		"L2 must NOT adopt EIP-7981's raised access-list cost (3680 gas/address)")

	t.Log("Access-list gas stays on Arsia pricing while L1 runs Glamsterdam",
		"smallGas", smallGas,
		"largeGas", largeGas,
		"marginalPerAddress", marginal,
		"arsiaExpected", arsiaGasPerAddress,
		"eip7981Rejected", eip7981GasPerAddress)
}

// sendWithAccessListAddresses sends a value-less EIP-2930 (type 0x01) L2 tx to
// recipient carrying an access list of n distinct addresses, each with zero
// storage keys, and returns the receipt's GasUsed. The listed addresses are
// deterministic and unrelated to the sender or recipient, so no opcode ever
// touches them and their only effect is the flat per-address access-list charge.
func sendWithAccessListAddresses(t devtest.T, wallet *dsl.EOA, recipient common.Address, n int) uint64 {
	al := make(types.AccessList, n)
	for i := 0; i < n; i++ {
		al[i] = types.AccessTuple{
			// 0xAC..0001, 0xAC..0002, ... — distinct, never touched by execution.
			Address:     common.HexToAddress(fmt.Sprintf("0xAC00000000000000000000000000000000%06d", i+1)),
			StorageKeys: nil,
		}
	}
	receipt, err := txplan.NewPlannedTx(txplan.Combine(
		wallet.Plan(),
		txplan.WithTo(&recipient),
		txplan.WithAccessList(al),
		txplan.WithGasLimit(20_000_000),
	)).Included.Eval(t.Ctx())
	t.Require().NoError(err)
	t.Require().Equal(types.ReceiptStatusSuccessful, receipt.Status, "access-list tx must succeed")
	return receipt.GasUsed
}
