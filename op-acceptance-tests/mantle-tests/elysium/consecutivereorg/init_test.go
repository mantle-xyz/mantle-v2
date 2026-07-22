package consecutivereorg

import (
	"github.com/ethereum-optimism/optimism/op-acceptance-tests/mantle-tests/elysium/devstackenv"
	"testing"

	opforks "github.com/ethereum-optimism/optimism/op-core/forks"
	"github.com/ethereum-optimism/optimism/op-devstack/compat"
	"github.com/ethereum-optimism/optimism/op-devstack/presets"
	"github.com/ethereum-optimism/optimism/op-devstack/stack"
	"github.com/ethereum-optimism/optimism/op-devstack/sysgo"
	"github.com/ethereum/go-ethereum/params/forks"
)

// amsterdamOffset activates Amsterdam this many SECONDS after L1 genesis (the offset unit is
// seconds, not blocks). With 6s L1 blocks that is L1 block amsterdamOffset/6. We drive WELL past
// this boundary so every reorg target across the whole test is post-Amsterdam.
const amsterdamOffset = uint64(30)

func TestMain(m *testing.M) {
	resetEnvVars := devstackenv.Configure()
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
		// L1 is produced manually via the TestSequencer, so L2 derivation must run on a
		// controllable clock rather than the wall clock.
		presets.WithTimeTravel(),
	)
}
