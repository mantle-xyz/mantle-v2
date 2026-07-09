package beaconspec

import (
	"testing"

	bss "github.com/ethereum-optimism/optimism/op-batcher/batcher"
	batcherFlags "github.com/ethereum-optimism/optimism/op-batcher/flags"
	opforks "github.com/ethereum-optimism/optimism/op-core/forks"
	"github.com/ethereum-optimism/optimism/op-devstack/presets"
	"github.com/ethereum-optimism/optimism/op-devstack/stack"
	"github.com/ethereum-optimism/optimism/op-devstack/sysgo"
	"github.com/ethereum/go-ethereum/params/forks"
)

// amsterdamOffset activates Amsterdam on the L1 a few blocks after genesis, so
// the L2 spends part of its life consuming a Glamsterdam L1.
const amsterdamOffset = uint64(6)

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
		// Post EIP-4844 blob batches to L1 (default is calldata) so the L1 has genuine
		// blob-carrying blocks for TestL1Beacon_BlobRoundTrip_PostGlamsterdam to fetch.
		sysgo.WithBatcherOption(func(_ stack.L2BatcherID, cfg *bss.CLIConfig) {
			cfg.DataAvailabilityType = batcherFlags.BlobsType
		}),
		sysgo.WithDeployerPipelineOption(sysgo.WithMantleForkAtGenesis(opforks.MantleElysium)),
	)))
}
