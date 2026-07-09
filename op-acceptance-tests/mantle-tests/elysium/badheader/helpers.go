package badheader

import (
	"os"

	"github.com/ethereum-optimism/optimism/op-devstack/sysgo"
)

// configureDevstackEnvVars runs the L1 EL as an external Glamsterdam (Amsterdam) geth subprocess
// so the header op-node's L1 client validates is a genuine Glamsterdam header.
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
