package reorg

import (
	"testing"

	"github.com/ethereum-optimism/optimism/op-devstack/compat"
	"github.com/ethereum-optimism/optimism/op-devstack/presets"
)

func TestMain(m *testing.M) {
	presets.DoMain(m,
		// Test-sequencer topology (so we can drive a real reorg) with the
		// Mantle preconf subsystem enabled on the L2 EL. Only op-reth honours
		// preconf, so these tests require DEVSTACK_L2EL_KIND=op-reth and skip
		// otherwise.
		presets.WithNewMantleSingleChainMultiNodeWithTestSeqPreconf(),
		presets.WithCompatibleTypes(compat.SysGo),
		presets.WithNoDiscovery(),
	)
}
