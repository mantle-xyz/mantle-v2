package testhelpers

import (
	"github.com/ethereum-optimism/optimism/op-chain-ops/devkeys"
	"github.com/ethereum-optimism/optimism/op-devstack/devtest"
	"github.com/ethereum-optimism/optimism/op-devstack/dsl"
	"github.com/ethereum-optimism/optimism/op-devstack/presets"
	"github.com/ethereum-optimism/optimism/op-service/eth"
)

// FundL1MNT credits an L1 EOA with Mantle's L1 MNT token before an L1->L2 bridge flow.
func FundL1MNT(t devtest.T, sys *presets.MantleMinimal, to *dsl.EOA, amount eth.ETH) {
	if amount == eth.ZeroWei {
		return
	}
	holderPriv := sys.L2Chain.Escape().Keys().Secret(devkeys.L1MNTFaucet)
	holder := dsl.NewKey(t, holderPriv).User(sys.L1EL)
	sys.FunderL1.FundAtLeast(holder, eth.OneTenthEther)

	l1MNTAddr := sys.L2Chain.Escape().Deployment().L1MNTAddr()
	mntFunder := dsl.NewMNTFunder(t, l1MNTAddr, holder)
	mntFunder.FundAtLeast(to, amount)
	to.WaitForTokenBalance(l1MNTAddr, amount)
}

// WaitForGlamsterdamL1 waits for the L1 to cross AmsterdamTime and then requires the block it
// crossed on to actually BE a Glamsterdam block.
//
// Every suite here is premised on the L2 consuming a real Glamsterdam L1, and almost every one
// of them starts by waiting for that crossing — so the check belongs in the wait rather than in
// each test, where it can be, and mostly was, forgotten.
//
// What it defends against is a silent EL downgrade. The L1 EL is selected by an environment
// variable read at option-construction time (sysgo.WithL1Nodes); anything that builds the system
// option before that variable is set falls back to the in-process op-geth, which does not enforce
// the Amsterdam header rules. Suites whose assertions happen to need valid Amsterdam blocks then
// fail somewhere deep in block production, while every other suite stays green having never
// consumed a Glamsterdam L1 at all. Asserting the crossing block's shape turns that into an
// immediate, uniform failure with a message that names the cause.
func WaitForGlamsterdamL1(t devtest.T, l1EL *dsl.L1ELNode, amsterdamTime uint64) eth.BlockRef {
	l1Ref := l1EL.WaitForTime(amsterdamTime)
	RequireGlamsterdamL1Control(t, l1EL, l1Ref)
	return l1Ref
}

// RequireGlamsterdamL1Control requires an at-or-after-AmsterdamTime L1 block to carry the
// Amsterdam header fields, so that L2-side nil-checks on the same fields mean something.
func RequireGlamsterdamL1Control(t devtest.T, l1EL *dsl.L1ELNode, l1Ref eth.BlockRef) {
	require := t.Require()

	l1Info, err := l1EL.Escape().EthClient().InfoByHash(t.Ctx(), l1Ref.Hash)
	require.NoErrorf(err, "read the L1 block %d that crossed AmsterdamTime", l1Ref.Number)
	l1Header := l1Info.Header()

	require.NotNilf(l1Header.BlockAccessListHash,
		"L1 block %d is at/after AmsterdamTime but carries no BlockAccessListHash: the L1 EL is not enforcing Amsterdam rules (a silent fall back to the in-process op-geth does this), so this suite is not consuming a Glamsterdam L1 and its L2 nil-checks would be vacuous",
		l1Ref.Number)
	require.NotNilf(l1Header.SlotNumber,
		"L1 block %d is at/after AmsterdamTime but carries no SlotNumber: the L1 EL is not enforcing Amsterdam rules, so this suite is not consuming a Glamsterdam L1",
		l1Ref.Number)
}
