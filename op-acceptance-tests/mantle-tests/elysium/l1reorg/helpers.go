package l1reorg

import (
	"os"

	"github.com/ethereum-optimism/optimism/op-devstack/sysgo"
)

// configureDevstackEnvVars forces the L1 EL to run as an external subprocess
// (vanilla Amsterdam geth via SYSGO_GETH_EXEC_PATH) instead of the in-process
// mantle op-geth library, which cannot produce Amsterdam L1 blocks.
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
