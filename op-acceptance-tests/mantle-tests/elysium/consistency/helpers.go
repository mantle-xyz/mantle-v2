package consistency

import (
	"os"

	"github.com/ethereum-optimism/optimism/op-chain-ops/devkeys"
	"github.com/ethereum-optimism/optimism/op-devstack/devtest"
	"github.com/ethereum-optimism/optimism/op-devstack/dsl"
	"github.com/ethereum-optimism/optimism/op-devstack/presets"
	"github.com/ethereum-optimism/optimism/op-devstack/sysgo"
	"github.com/ethereum-optimism/optimism/op-service/eth"
)

// configureDevstackEnvVars runs the L1 EL as an external Glamsterdam (Amsterdam) geth
// subprocess so the sequencer and verifier both derive a genuine Glamsterdam L1.
func configureDevstackEnvVars() func() {
	oldKind, hadKind := os.LookupEnv(sysgo.DevstackL1ELKindEnvVar)

	if !hadKind {
		_ = os.Setenv(sysgo.DevstackL1ELKindEnvVar, "geth")
	}

	return func() {
		if hadKind {
			_ = os.Setenv(sysgo.DevstackL1ELKindEnvVar, oldKind)
		} else {
			_ = os.Unsetenv(sysgo.DevstackL1ELKindEnvVar)
		}
	}
}

// fundL1MNT credits the given L1 EOA with `amount` of the L1 MNT ERC20, using the
// deployment's L1MNTFaucet holder. MNT is the Mantle L2 native gas token, so this is
// the L1-side balance a depositor must own before bridging MNT to L2. Mirrors the proven
// flow in mantle-tests/base/deposit/helper.go's fundMNT.
func fundL1MNT(t devtest.T, sys *presets.MantleMinimal, to *dsl.EOA, amount eth.ETH) {
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
