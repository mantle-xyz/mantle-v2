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

// RequireGlamsterdamL1Control proves the current run really exposes Amsterdam L1 header fields.
func RequireGlamsterdamL1Control(t devtest.T, sys *presets.MantleMinimal, l1Ref eth.BlockRef) {
	require := t.Require()

	l1Info, err := sys.L1EL.Escape().EthClient().InfoByHash(t.Ctx(), l1Ref.Hash)
	require.NoErrorf(err, "read the L1 block %d that crossed AmsterdamTime", l1Ref.Number)
	l1Header := l1Info.Header()

	require.NotNilf(l1Header.BlockAccessListHash,
		"L1 block %d is at/after AmsterdamTime but carries no BlockAccessListHash; L2 nil-checks would be vacuous",
		l1Ref.Number)
	require.NotNilf(l1Header.SlotNumber,
		"L1 block %d is at/after AmsterdamTime but carries no SlotNumber; L2 nil-checks would be vacuous",
		l1Ref.Number)
}
