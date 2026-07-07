package l1reorg

import (
	"testing"

	opforks "github.com/ethereum-optimism/optimism/op-core/forks"
	"github.com/ethereum-optimism/optimism/op-devstack/compat"
	"github.com/ethereum-optimism/optimism/op-devstack/presets"
	"github.com/ethereum-optimism/optimism/op-devstack/stack"
	"github.com/ethereum-optimism/optimism/op-devstack/sysgo"
	"github.com/ethereum/go-ethereum/params/forks"
)

// amsterdamOffset is the number of L1 blocks after genesis at which Amsterdam
// activates. The TestSequencer's fakepos builder must cross this boundary.
const amsterdamOffset = uint64(6)

func TestMain(m *testing.M) {
	resetEnvVars := configureDevstackEnvVars()
	defer resetEnvVars()

	presets.DoMain(m,
		stack.MakeCommon(stack.Combine[*sysgo.Orchestrator](
			sysgo.DefaultMantleSingleChainMultiNodeWithTestSeqSystem(&sysgo.DefaultMantleSingleChainMultiNodeWithTestSeqSystemIDs{}),
			sysgo.WithDeployerOptions(
				sysgo.WithDefaultBPOBlobSchedule,
				sysgo.WithForkAtL1Offset(forks.BPO3, 0),
				sysgo.WithForkAtL1Offset(forks.BPO4, 0),
				sysgo.WithForkAtL1Offset(forks.BPO5, 0),
				sysgo.WithForkAtL1Offset(forks.Amsterdam, amsterdamOffset),
			),
			sysgo.WithDeployerPipelineOption(sysgo.WithMantleForkAtGenesis(opforks.MantleElysium)),
		)),
		presets.WithCompatibleTypes(compat.SysGo),
		presets.WithNoDiscovery(),
	)
}
