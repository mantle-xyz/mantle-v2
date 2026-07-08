package afterrestart

import (
	"testing"

	opforks "github.com/ethereum-optimism/optimism/op-core/forks"
	"github.com/ethereum-optimism/optimism/op-devstack/presets"
	"github.com/ethereum-optimism/optimism/op-devstack/stack"
	"github.com/ethereum-optimism/optimism/op-devstack/sysgo"
	"github.com/ethereum/go-ethereum/params/forks"
)

// amsterdamOffset activates Amsterdam this many SECONDS after L1 genesis (the offset
// unit is seconds, not blocks). With 6s L1 blocks that is L1 block 1, so essentially
// the whole L1 chain the L2 consumes is a Glamsterdam (Amsterdam EL) chain — the L2
// derives across the boundary early, well before the restart.
const amsterdamOffset = uint64(6)

func TestMain(m *testing.M) {
	resetEnvVars := configureDevstackEnvVars()
	defer resetEnvVars()

	// Auto-FakePoS drives the L1 on the wall clock; the L2 sequencer produces on the
	// wall clock too. No time-travel: the restart-and-recover flow is timing-driven
	// via WaitFor.../Eventually, so we let both chains advance on their own clock.
	presets.DoMain(m, stack.MakeCommon(stack.Combine[*sysgo.Orchestrator](
		sysgo.DefaultMantleMinimalSystem(&sysgo.DefaultMinimalSystemIDs{}),
		sysgo.WithDeployerOptions(
			sysgo.WithDefaultBPOBlobSchedule,
			sysgo.WithForkAtL1Offset(forks.BPO3, 0),
			sysgo.WithForkAtL1Offset(forks.BPO4, 0),
			sysgo.WithForkAtL1Offset(forks.BPO5, 0),
			sysgo.WithForkAtL1Offset(forks.Amsterdam, amsterdamOffset),
		),
		sysgo.WithDeployerPipelineOption(sysgo.WithMantleForkAtGenesis(opforks.MantleElysium)),
	)))
}
