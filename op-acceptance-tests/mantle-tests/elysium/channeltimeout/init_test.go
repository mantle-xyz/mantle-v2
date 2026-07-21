package channeltimeout

import (
	"testing"

	opforks "github.com/ethereum-optimism/optimism/op-core/forks"
	"github.com/ethereum-optimism/optimism/op-devstack/compat"
	"github.com/ethereum-optimism/optimism/op-devstack/presets"
	"github.com/ethereum-optimism/optimism/op-devstack/stack"
	"github.com/ethereum-optimism/optimism/op-devstack/sysgo"
	"github.com/ethereum/go-ethereum/params/forks"
)

// amsterdamOffset activates Amsterdam this many SECONDS after L1 genesis (the offset unit
// is seconds, not blocks). With 6s L1 blocks that is L1 block amsterdamOffset/6 = block 20,
// which lands INSIDE the first channel's timeout window: that channel opens on a
// pre-Amsterdam L1 block and must still be accepted at its post-Amsterdam deadline, so the
// deadline is proven to span the upgrade rather than being reset by it.
const (
	amsterdamOffset = uint64(120)
	l1BlockTime     = uint64(6)
)

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
		// L1 is produced manually via the TestSequencer, one block at a time, so the
		// channel's frames can be placed in exact L1 blocks. Time travel lets the L2
		// keep sequencing without waiting on wall-clock block time.
		presets.WithTimeTravel(),
	)
}
