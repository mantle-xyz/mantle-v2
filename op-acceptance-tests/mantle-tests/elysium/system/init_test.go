package system

import (
	"os"
	"testing"

	"github.com/ethereum-optimism/optimism/op-devstack/compat"
	"github.com/ethereum-optimism/optimism/op-devstack/devtest"
	"github.com/ethereum-optimism/optimism/op-devstack/presets"
)

func TestMain(m *testing.M) {
	// Every test in this suite is gated to a real-CL sysext devnet (compat.Kurtosis /
	// Persistent). Without one, WithCompatibleTypes would silently SkipNow -> os.Exit(0), so a
	// run with no devnet would masquerade as green. Force the framework's "preconditions must be
	// met" mode so a missing devnet FAILS (exit 42) instead of skipping. op-acceptor sets this
	// itself; we set it here so direct `go test` runs get the same guarantee. This does not
	// affect the ELYSIUM_HEAVY skip in longrun, which uses the stdlib testing.T.Skip.
	if os.Getenv(devtest.ExpectPreconditionsMet) == "" {
		_ = os.Setenv(devtest.ExpectPreconditionsMet, "true")
	}

	presets.DoMain(m,
		// This suite is the real-CL system integration coverage. The network must come from a
		// sysext devnet descriptor with real L1 EL/CL services.
		presets.WithCompatibleTypes(compat.Kurtosis, compat.Persistent),
		presets.WithMantleMinimal(),
	)
}
