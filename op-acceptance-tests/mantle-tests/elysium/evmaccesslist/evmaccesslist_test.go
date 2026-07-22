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

	// arsiaGasPerStorageKey is the Mantle-Arsia per-access-list-storage-key cost.
	arsiaGasPerStorageKey = 1900 // params.TxAccessListStorageKeyGas
	// eip7981GasPerStorageKey is the raised per-storage-key cost the L2 must NOT adopt.
	// 1900 (base) + 32*16*4 (EIP-7981 extra, = 2048) = 3948.
	eip7981GasPerStorageKey = 3948
)

// TestL2EVM_AccessListGasStaysArsia verifies an L2-internal Arsia property: the
// Mantle L2 charges EIP-2930 access-list addresses at the base Arsia rate
// (2400 gas/address) and has NOT adopted EIP-7981's raised cost. This pricing is
// gated on the *L2* fork in op-geth core/state_transition.go (IntrinsicGas keys off
// rules.IsAmsterdam, which is false because the L2's AmsterdamTime == nil) — it is
// not L1-sensitive and would hold under any L1. The L1 runs Glamsterdam here only
// as the surrounding environment, not as the discriminator.
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
// COVERAGE: this covers the EIP-7981 access-list-repricing sub-case, sibling to the
// EIP-7976 calldata floor (evmgas). EIP-7778 block-level gas accounting is covered in
// evmblockgas; EIP-8024 opcodes in evmopcodes.
func TestL2EVM_AccessListGasStaysArsia(gt *testing.T) {
	t := devtest.SerialT(gt)
	sys := presets.NewMantleMinimal(t)
	require := t.Require()

	// Establish the environment: drive the L1 across into Glamsterdam (Amsterdam)
	// so the L2-internal access-list pricing below is exercised *while consuming an
	// upgraded L1*. The L1 fork is the environment, not the discriminator — the
	// per-address rate is gated on the L2 fork and is independent of the L1.
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

	smallGas := sendWithAccessList(t, wallet, recipient, smallAddrs, 0)
	largeGas := sendWithAccessList(t, wallet, recipient, largeAddrs, 0)

	// Exact per-tx cost for a value-less EOA call carrying only access-list
	// addresses: 21000 + N*2400 on Arsia.
	require.EqualValues(txBaseGas+arsiaGasPerAddress*smallAddrs, smallGas,
		"small access-list tx gas must match Arsia pricing (2400 gas/address)")
	require.EqualValues(txBaseGas+arsiaGasPerAddress*largeAddrs, largeGas,
		"large access-list tx gas must match Arsia pricing (2400 gas/address)")

	// The relationship that discriminates Arsia from EIP-7981: the marginal gas of
	// one extra access-list address. The 21000 base and any fixed execution cancel
	// out, leaving just the per-address rate.
	deltaGas := largeGas - smallGas
	deltaAddrs := uint64(largeAddrs - smallAddrs)
	require.Zero(deltaGas%deltaAddrs, "marginal gas should be a whole number of gas per address")
	marginal := deltaGas / deltaAddrs

	require.EqualValues(arsiaGasPerAddress, marginal,
		"L2 must price access-list addresses at the Arsia rate (2400 gas/address), "+
			"not EIP-7981's raised 3680 gas/address")

	// EIP-7981 reprices BOTH halves of an access list, so measuring only the per-address rate
	// would leave the storage-key half unguarded: an L2 could adopt the raised 3948 gas/key
	// while still charging 2400/address and everything above would stay green.
	//
	// Hold the address count fixed at smallAddrs and add storage keys, so the difference from
	// smallGas is attributable to the keys alone.
	const keysPerAddr = 4
	totalKeys := uint64(smallAddrs * keysPerAddr)
	keyedGas := sendWithAccessList(t, wallet, recipient, smallAddrs, keysPerAddr)

	require.EqualValues(txBaseGas+arsiaGasPerAddress*smallAddrs+arsiaGasPerStorageKey*int(totalKeys), keyedGas,
		"access-list tx with storage keys must match Arsia pricing (2400 gas/address + 1900 gas/key)")

	deltaKeyGas := keyedGas - smallGas
	require.Zero(deltaKeyGas%totalKeys, "marginal gas should be a whole number of gas per storage key")
	require.EqualValues(arsiaGasPerStorageKey, deltaKeyGas/totalKeys,
		"L2 must price access-list storage keys at the Arsia rate (%d gas/key), not EIP-7981's "+
			"raised %d gas/key", arsiaGasPerStorageKey, eip7981GasPerStorageKey)

	t.Log("Access-list gas stays on Arsia pricing while L1 runs Glamsterdam",
		"smallGas", smallGas,
		"largeGas", largeGas,
		"marginalPerAddress", marginal,
		"arsiaExpected", arsiaGasPerAddress,
		"eip7981Rejected", eip7981GasPerAddress)
}

// sendWithAccessList sends a value-less L2 tx to recipient carrying an access list of n
// distinct addresses, each with keysPerAddr distinct storage keys, and returns the receipt's
// GasUsed. The listed addresses and keys are deterministic and unrelated to the sender or
// recipient, so no opcode ever touches them and their only effect is the flat per-address and
// per-storage-key access-list charge.
//
// The tx itself is the wallet's default DynamicFee (EIP-1559, type 0x02) — NOT type 0x01. The
// access list rides on it either way, and EIP-7981 modifies the shared IntrinsicGas
// access-list branch (state_transition.go:132-159) regardless of tx type, so the pricing under
// test is the same. (An earlier version of this comment claimed a type-0x01 tx, which did not
// match what the code sends.)
func sendWithAccessList(t devtest.T, wallet *dsl.EOA, recipient common.Address, n, keysPerAddr int) uint64 {
	al := make(types.AccessList, n)
	for i := 0; i < n; i++ {
		keys := make([]common.Hash, keysPerAddr)
		for k := range keys {
			// 0x5107..<addr><key> — distinct per (address, key), never touched by execution.
			keys[k] = common.HexToHash(fmt.Sprintf("0x5107%060d", (i+1)*1000+k+1))
		}
		al[i] = types.AccessTuple{
			// 0xAC..0001, 0xAC..0002, ... — distinct, never touched by execution.
			Address:     common.HexToAddress(fmt.Sprintf("0xAC00000000000000000000000000000000%06d", i+1)),
			StorageKeys: keys,
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
