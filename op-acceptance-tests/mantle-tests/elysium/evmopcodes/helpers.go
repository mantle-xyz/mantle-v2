package evmopcodes

import (
	"os"

	"github.com/ethereum-optimism/optimism/op-devstack/sysgo"
)

// configureDevstackEnvVars runs the L1 EL as an external Glamsterdam (Amsterdam)
// geth subprocess so these assertions hold while the L2 genuinely consumes a
// Glamsterdam L1.
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
