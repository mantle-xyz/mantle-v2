package midflight

import (
	"testing"

	opforks "github.com/ethereum-optimism/optimism/op-core/forks"
	"github.com/ethereum-optimism/optimism/op-devstack/presets"
	"github.com/ethereum-optimism/optimism/op-devstack/stack"
	"github.com/ethereum-optimism/optimism/op-devstack/sysgo"
	"github.com/ethereum/go-ethereum/params/forks"
)

// amsterdamOffset activates Amsterdam on the L1 this many SECONDS after L1 genesis
// (WithForkAtL1Offset is seconds, NOT blocks; the L1 block time is 6s).
//
// This test needs a genuine PRE-Amsterdam window: it must land an L2 tx on a
// pre-Amsterdam L1 before driving the boundary. Because the L1 runs on the real
// system clock, the wall-clock spent on system bring-up (deployer, L2 nodes,
// batcher) is charged against that window. The small offsets used by the smoke /
// derivation suites (6s ~= 1 L1 block) would already be past Amsterdam by the time
// the test body starts. 60s leaves a comfortable pre-boundary window after
// bring-up while keeping the test well within the default `go test` deadline; the
// test body's step (a) asserts the L1 head is still pre-Amsterdam and tells you to
// raise this value if bring-up ever eats the whole window.
const amsterdamOffset = uint64(60)

func TestMain(m *testing.M) {
	resetEnvVars := configureDevstackEnvVars()
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
