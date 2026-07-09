package deposit

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/ethereum-optimism/optimism/op-chain-ops/devkeys"
	"github.com/ethereum-optimism/optimism/op-devstack/devtest"
	"github.com/ethereum-optimism/optimism/op-devstack/dsl"
	"github.com/ethereum-optimism/optimism/op-devstack/presets"
	"github.com/ethereum-optimism/optimism/op-devstack/sysgo"
	"github.com/ethereum-optimism/optimism/op-service/eth"
)

// configureDevstackEnvVars runs the L1 EL as an external Amsterdam-capable geth
// subprocess so these assertions hold while the L2 genuinely consumes a Glamsterdam
// L1. It auto-locates a mise-installed geth (mirroring the fusaka suite) so these
// tests are self-sufficient and do NOT require SYSGO_GETH_EXEC_PATH to be exported by
// the runner. No-op if DevstackL1ELKindEnvVar or GethExecPathEnvVar is already set.
func configureDevstackEnvVars() func() {
	if _, ok := os.LookupEnv(sysgo.DevstackL1ELKindEnvVar); ok {
		return func() {}
	}
	if _, ok := os.LookupEnv(sysgo.GethExecPathEnvVar); ok {
		return func() {}
	}

	cmd := exec.Command("mise", "which", "geth")
	buf := bytes.NewBuffer([]byte{})
	cmd.Stdout = buf
	if err := cmd.Run(); err != nil {
		fmt.Printf("Failed to find mise-installed geth: %v\n", err)
		return func() {}
	}
	execPath := strings.TrimSpace(buf.String())
	fmt.Println("Found mise-installed geth:", execPath)

	_ = os.Setenv(sysgo.GethExecPathEnvVar, execPath)
	_ = os.Setenv(sysgo.DevstackL1ELKindEnvVar, "geth")
	return func() {
		_ = os.Unsetenv(sysgo.GethExecPathEnvVar)
		_ = os.Unsetenv(sysgo.DevstackL1ELKindEnvVar)
	}
}

// fundL1MNT credits the given L1 EOA with `amount` of the L1 MNT ERC20, using the
// deployment's L1MNTFaucet holder. MNT is the Mantle L2 native gas token, so this
// is the L1-side balance a depositor must own before bridging MNT to L2. Mirrors
// the proven flow in mantle-tests/base/deposit/helper.go's fundMNT.
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
