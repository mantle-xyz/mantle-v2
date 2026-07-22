package preactivation

import (
	"github.com/ethereum-optimism/optimism/op-acceptance-tests/mantle-tests/elysium/devstackenv"
	"testing"

	opforks "github.com/ethereum-optimism/optimism/op-core/forks"
	"github.com/ethereum-optimism/optimism/op-devstack/presets"
	"github.com/ethereum-optimism/optimism/op-devstack/stack"
	"github.com/ethereum-optimism/optimism/op-devstack/sysgo"
	"github.com/ethereum/go-ethereum/params/forks"
)

func TestMain(m *testing.M) {
	resetEnvVars := devstackenv.Configure()
	defer resetEnvVars()

	// Amsterdam (Glamsterdam EL) activates this many SECONDS after L1 genesis (the
	// offset unit is seconds, NOT blocks). The activation L1 block height is therefore
	// not assumable and is discovered dynamically by the test as the first IsAmsterdam
	// block. An offset of 30 lands the activation well above genesis, so a real
	// pre-Amsterdam parent block (legacy header, no BAL/SlotNumber) exists — the exact
	// block this test inspects. A tiny offset (e.g. activation at block 1) would leave
	// no genuine pre-fork block and is deliberately avoided.
	amsterdamOffset := uint64(30)

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
