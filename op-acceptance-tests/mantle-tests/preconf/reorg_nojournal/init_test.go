package reorgnojournal

import (
	"testing"

	"github.com/ethereum-optimism/optimism/op-devstack/compat"
	"github.com/ethereum-optimism/optimism/op-devstack/presets"
)

func TestMain(m *testing.M) {
	presets.DoMain(m,
		// Same test-sequencer topology as the journal-on reorg package, but the
		// preconf subsystem is started with the on-disk journal DISABLED. That is
		// the whole point of TC-RG4: journal is a launch flag, not runtime
		// toggleable, so the degraded path needs its own orchestrator (hence its
		// own package / TestMain). Only op-reth honours preconf, so these tests
		// require DEVSTACK_L2EL_KIND=op-reth and skip otherwise.
		presets.WithNewMantleSingleChainMultiNodeWithTestSeqPreconfNoJournal(),
		presets.WithCompatibleTypes(compat.SysGo),
		presets.WithNoDiscovery(),
	)
}
