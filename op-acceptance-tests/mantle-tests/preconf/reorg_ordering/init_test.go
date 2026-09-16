package reorgordering

import (
	"testing"

	"github.com/ethereum-optimism/optimism/op-devstack/compat"
	"github.com/ethereum-optimism/optimism/op-devstack/presets"
)

func TestMain(m *testing.M) {
	presets.DoMain(m,
		// Same test-sequencer topology as the `reorg` package, but the preconf
		// subsystem uses a RESTRICTED (from,to) whitelist instead of --preconf.all.
		// That is what makes a non-whitelisted, tip-ordered pool tx possible, which
		// the ordering-inversion attribution test needs. Journal is ON here.
		// Only op-reth honours preconf, so these tests require
		// DEVSTACK_L2EL_KIND=op-reth and skip otherwise.
		presets.WithNewMantleSingleChainMultiNodeWithTestSeqPreconfWhitelist(),
		presets.WithCompatibleTypes(compat.SysGo),
		presets.WithNoDiscovery(),
	)
}
