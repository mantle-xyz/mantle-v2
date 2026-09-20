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

// eip8037NewAccountStateGas is what EIP-8037 charges for bringing a new account into existence:
// AccountCreationSize(120) * CostPerStateByte(1530), read off the pinned L1 geth
// (params/protocol_params.go:219,222 of ethereum/go-ethereum 49e199bb, the Glamsterdam devnet
// build). Mantle op-geth defines neither constant, so on the L2 the charge is structurally absent
// today — which is exactly why it is worth pinning: nothing else stops it arriving with a rebase.
const eip8037NewAccountStateGas = uint64(120 * 1530)

// runStateCreationGasStaysArsia verifies an L2-internal Arsia property: creating an account costs
// nothing extra, while the L1 it consumes runs Glamsterdam.
//
// EIP-8037 makes account creation a priced state operation — a value transfer to an empty
// recipient is charged NEW_ACCOUNT state gas (core/state_transition.go, chargeCallRecipientEIP2780
// in the pinned L1 geth). That state gas is not a separate meter that a receipt hides: settlement
// computes gasUsedBeforeRefund = GasLimit - (RegularGas + StateGas), so it lands in
// receipt.GasUsed like any other cost.
//
// The probe is self-calibrating rather than pinned to a constant: it sends the SAME transfer to
// the SAME address twice. On the first send the recipient is empty (the account gets created); on
// the second it already exists. Under Arsia both cost the same, because creation is free. Under
// EIP-8037 the first would exceed the second by eip8037NewAccountStateGas. Comparing the two sends
// means the assertion needs no hardcoded intrinsic and survives any future change to Mantle's base
// transaction cost — only the CREATION surcharge is isolated, which is precisely what 8037 adds.
//
// It does NOT cover EIP-2780 (Glamsterdam CFI), even though the L1 build already applies it and
// the geth charge above sits on 2780's code path. 2780 lowers the transaction base cost equally
// for both sends, so it cancels out of the comparison entirely: an L2 that adopted 2780 alone
// would still pass here. Only the creation surcharge is isolated, which is 8037's contribution.
func runStateCreationGasStaysArsia(gt *testing.T) {
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

	// A recipient no other case touches. Each run builds a fresh chain, so it starts empty — but
	// assert that rather than assume it: if the account already existed, the first send would not
	// create anything and the comparison below would be vacuously equal.
	// The charge this case rules out is gated on state.Empty(to) — balance zero AND nonce zero AND
	// no code — so check all three, not just the balance. A recipient with a nonce or code would
	// not be "empty" to the L1 rule, the first send would create nothing, and the comparison below
	// would hold vacuously.
	recipient := common.HexToAddress("0x0000000000000000000000000000000000805037")
	balance, err := l2.BalanceAt(ctx, recipient, nil)
	require.NoError(err, "read the recipient's starting balance")
	require.Truef(balance.Sign() == 0, "recipient %s must start with zero balance for the first send to create it", recipient)
	nonce, err := l2.NonceAt(ctx, recipient, nil)
	require.NoError(err, "read the recipient's starting nonce")
	require.Zerof(nonce, "recipient %s must start with zero nonce to count as an empty account", recipient)
	code, err := l2.CodeAtHash(ctx, recipient, sys.L2EL.BlockRefByLabel(eth.Unsafe).Hash)
	require.NoError(err, "read the recipient's starting code")
	require.Emptyf(code, "recipient %s must start with no code to count as an empty account", recipient)

	send := func(what string) *types.Receipt {
		receipt, err := txplan.NewPlannedTx(txplan.Combine(
			wallet.Plan(),
			txplan.WithTo(&recipient),
			txplan.WithValue(eth.OneGWei),
			txplan.WithGasLimit(1_000_000),
		)).Included.Eval(ctx)
		require.NoErrorf(err, "%s transfer must be included", what)
		require.Equalf(types.ReceiptStatusSuccessful, receipt.Status, "%s transfer must succeed", what)
		return receipt
	}

	// First send creates the account; second send pays into an account that already exists.
	creating := send("account-creating")
	require.Positivef(creating.GasUsed, "creating transfer must report gas used")

	afterBalance, err := l2.BalanceAt(ctx, recipient, nil)
	require.NoError(err, "read the recipient's balance after the first send")
	require.Truef(afterBalance.Sign() > 0, "first send must leave the recipient non-empty, so the second send creates nothing")

	existing := send("already-exists")

	require.Equalf(existing.GasUsed, creating.GasUsed,
		"creating an L2 account must cost the same as paying into an existing one (Arsia): account "+
			"creation is free. Under EIP-8037 the creating transfer would cost %d more (NEW_ACCOUNT "+
			"state gas = AccountCreationSize x CostPerStateByte), so this equality is what rules it out.",
		eip8037NewAccountStateGas)

	t.Log("L2 account-creation gas stays on Arsia while L1 runs Glamsterdam",
		"creatingGasUsed", creating.GasUsed,
		"existingGasUsed", existing.GasUsed,
		"arsiaIdentityHolds", creating.GasUsed == existing.GasUsed,
		"eip8037WouldBe", existing.GasUsed+eip8037NewAccountStateGas,
		"newAccountStateGasByEip8037", eip8037NewAccountStateGas)
}
