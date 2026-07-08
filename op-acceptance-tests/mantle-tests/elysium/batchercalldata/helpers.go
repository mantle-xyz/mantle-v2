package batchercalldata

import (
	"os"

	"github.com/ethereum-optimism/optimism/op-devstack/sysgo"
)

// configureDevstackEnvVars runs the L1 EL as an external Glamsterdam (Amsterdam)
// geth subprocess so the batcher genuinely submits its CALLDATA batches to a
// Glamsterdam L1 that prices calldata under EIP-7976's raised floor.
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
