package reorgorderingnojournal

import (
	"testing"

	"github.com/ethereum-optimism/optimism/op-devstack/compat"
	"github.com/ethereum-optimism/optimism/op-devstack/presets"
)

func TestMain(m *testing.M) {
	presets.DoMain(m,
		// Restricted-whitelist test-sequencer topology (so a tip-ordered pool tx
		// exists) with the preconf commitment journal DISABLED — the TC-RG4
		// degraded path. Journal is a launch flag, not runtime toggleable, so the
		// journal-off ordering characterization needs its own orchestrator / package.
		// Only op-reth honours preconf, so these tests require
		// DEVSTACK_L2EL_KIND=op-reth and skip otherwise.
		presets.WithNewMantleSingleChainMultiNodeWithTestSeqPreconfWhitelistNoJournal(),
		presets.WithCompatibleTypes(compat.SysGo),
		presets.WithNoDiscovery(),
	)
}
