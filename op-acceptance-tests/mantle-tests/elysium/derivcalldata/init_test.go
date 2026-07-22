package derivcalldata

import (
	"github.com/ethereum-optimism/optimism/op-acceptance-tests/mantle-tests/elysium/devstackenv"
	"testing"

	bss "github.com/ethereum-optimism/optimism/op-batcher/batcher"
	batcherFlags "github.com/ethereum-optimism/optimism/op-batcher/flags"
	opforks "github.com/ethereum-optimism/optimism/op-core/forks"
	"github.com/ethereum-optimism/optimism/op-devstack/presets"
	"github.com/ethereum-optimism/optimism/op-devstack/stack"
	"github.com/ethereum-optimism/optimism/op-devstack/sysgo"
	"github.com/ethereum/go-ethereum/params/forks"
)

// amsterdamOffset activates Amsterdam on the L1 a few blocks after genesis, so the
// L2 spends part of its life deriving batches from a Glamsterdam L1.
const amsterdamOffset = uint64(30)

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
		// Submit batches as ordinary CALLDATA transactions (the batcher default, set
		// here explicitly) so derivation must travel the calldata DA path off a
		// Glamsterdam L1 — the counterpart to derivblob's EIP-4844 blob path.
		sysgo.WithBatcherOption(func(_ stack.L2BatcherID, cfg *bss.CLIConfig) {
			cfg.DataAvailabilityType = batcherFlags.CalldataType
		}),
		sysgo.WithDeployerPipelineOption(sysgo.WithMantleForkAtGenesis(opforks.MantleElysium)),
	)))
}
