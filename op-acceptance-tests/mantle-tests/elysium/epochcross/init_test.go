package epochcross

import (
	"github.com/ethereum-optimism/optimism/op-acceptance-tests/mantle-tests/elysium/devstackenv"
	"testing"

	opforks "github.com/ethereum-optimism/optimism/op-core/forks"
	"github.com/ethereum-optimism/optimism/op-devstack/presets"
	"github.com/ethereum-optimism/optimism/op-devstack/stack"
	"github.com/ethereum-optimism/optimism/op-devstack/sysgo"
	"github.com/ethereum/go-ethereum/params/forks"
)

// amsterdamOffset activates Amsterdam SECONDS after L1 genesis. It is chosen so that with 6s L1
// blocks the activation lands on L1 block 32 — an L1 (beacon) epoch boundary (32 slots): 32*6=192.
const amsterdamOffset = uint64(192)

func TestMain(m *testing.M) {
	resetEnvVars := devstackenv.Configure()
	defer resetEnvVars()

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
