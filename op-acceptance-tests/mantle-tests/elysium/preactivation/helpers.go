package preactivation

import (
	"os"

	"github.com/ethereum-optimism/optimism/op-devstack/sysgo"
)

// configureDevstackEnvVars runs the L1 EL as an external Glamsterdam (Amsterdam)
// geth subprocess (DEVSTACK_L1EL_KIND=geth) so the L1 produces real Amsterdam
// block headers (EIP-7928 BlockAccessListHash, EIP-7843 SlotNumber) for the L2 to
// derive from — and, just as importantly for this test, genuine pre-Amsterdam
// (legacy) headers that lack those fields. The runner supplies the geth binary via
// SYSGO_GETH_EXEC_PATH.
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
