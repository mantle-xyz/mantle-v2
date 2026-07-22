package testmain

import (
	"os"
	"testing"

	"github.com/ethereum-optimism/optimism/op-acceptance-tests/mantle-tests/elysium/internal/devstackenv"
	bss "github.com/ethereum-optimism/optimism/op-batcher/batcher"
	batcherFlags "github.com/ethereum-optimism/optimism/op-batcher/flags"
	opforks "github.com/ethereum-optimism/optimism/op-core/forks"
	"github.com/ethereum-optimism/optimism/op-devstack/compat"
	"github.com/ethereum-optimism/optimism/op-devstack/devtest"
	"github.com/ethereum-optimism/optimism/op-devstack/presets"
	"github.com/ethereum-optimism/optimism/op-devstack/stack"
	"github.com/ethereum-optimism/optimism/op-devstack/sysgo"
	"github.com/ethereum/go-ethereum/params/forks"
)

const DefaultAmsterdamOffset = uint64(30)

type Option func(*config)

type config struct {
	deployer []sysgo.DeployerOption
	batcher  []sysgo.BatcherOption
}

func WithCalldataBatches() Option {
	return func(cfg *config) {
		cfg.batcher = append(cfg.batcher, func(_ stack.L2BatcherID, batcherCfg *bss.CLIConfig) {
			batcherCfg.DataAvailabilityType = batcherFlags.CalldataType
		})
	}
}

func WithBlobBatches() Option {
	return func(cfg *config) {
		cfg.batcher = append(cfg.batcher, func(_ stack.L2BatcherID, batcherCfg *bss.CLIConfig) {
			batcherCfg.DataAvailabilityType = batcherFlags.BlobsType
		})
	}
}

func WithCellProofBatches() Option {
	return func(cfg *config) {
		cfg.batcher = append(cfg.batcher, func(_ stack.L2BatcherID, batcherCfg *bss.CLIConfig) {
			batcherCfg.DataAvailabilityType = batcherFlags.BlobsType
			batcherCfg.TxMgrConfig.CellProofTime = 0
		})
	}
}

func WithSequencingWindow(size uint64) Option {
	return func(cfg *config) {
		cfg.deployer = append(cfg.deployer, sysgo.WithSequencingWindow(size))
	}
}

func RunMinimal(m *testing.M, amsterdamOffset uint64, opts ...Option) {
	runSysGo(m, mantleElysiumOption(
		sysgo.DefaultMantleMinimalSystem(&sysgo.DefaultMinimalSystemIDs{}),
		amsterdamOffset,
		opts...,
	))
}

func RunMultiNode(m *testing.M, amsterdamOffset uint64, opts ...Option) {
	runSysGo(m, mantleElysiumOption(
		sysgo.DefaultMantleSingleChainMultiNodeSystem(&sysgo.DefaultMantleSingleChainMultiNodeSystemIDs{}),
		amsterdamOffset,
		opts...,
	), presets.WithCompatibleTypes(compat.SysGo), presets.WithNoDiscovery())
}

func RunTestSeq(m *testing.M, amsterdamOffset uint64, opts ...Option) {
	runTestSeq(m, amsterdamOffset, true, opts...)
}

func RunTestSeqWallClock(m *testing.M, amsterdamOffset uint64, opts ...Option) {
	runTestSeq(m, amsterdamOffset, false, opts...)
}

func RunSystem(m *testing.M) {
	if os.Getenv(devtest.ExpectPreconditionsMet) == "" {
		_ = os.Setenv(devtest.ExpectPreconditionsMet, "true")
	}

	presets.DoMain(m,
		presets.WithCompatibleTypes(compat.Kurtosis, compat.Persistent),
		presets.WithMantleMinimal(),
	)
}

func runTestSeq(m *testing.M, amsterdamOffset uint64, timeTravel bool, opts ...Option) {
	common := []stack.CommonOption{
		presets.WithCompatibleTypes(compat.SysGo),
		presets.WithNoDiscovery(),
	}
	if timeTravel {
		common = append(common, presets.WithTimeTravel())
	}
	runSysGo(m, mantleElysiumOption(
		sysgo.DefaultMantleSingleChainMultiNodeWithTestSeqSystem(&sysgo.DefaultMantleSingleChainMultiNodeWithTestSeqSystemIDs{}),
		amsterdamOffset,
		opts...,
	), common...)
}

func runSysGo(m *testing.M, sysGoOpt stack.Option[*sysgo.Orchestrator], common ...stack.CommonOption) {
	resetEnvVars := devstackenv.Configure()
	defer resetEnvVars()

	opts := append([]stack.CommonOption{stack.MakeCommon(sysGoOpt)}, common...)
	presets.DoMain(m, opts...)
}

func mantleElysiumOption(system stack.Option[*sysgo.Orchestrator], amsterdamOffset uint64, opts ...Option) stack.Option[*sysgo.Orchestrator] {
	cfg := config{}
	for _, opt := range opts {
		opt(&cfg)
	}

	deployerOpts := []sysgo.DeployerOption{
		sysgo.WithDefaultBPOBlobSchedule,
		sysgo.WithForkAtL1Offset(forks.BPO3, 0),
		sysgo.WithForkAtL1Offset(forks.BPO4, 0),
		sysgo.WithForkAtL1Offset(forks.BPO5, 0),
		sysgo.WithForkAtL1Offset(forks.Amsterdam, amsterdamOffset),
	}
	deployerOpts = append(deployerOpts, cfg.deployer...)

	sysGoOpts := []stack.Option[*sysgo.Orchestrator]{
		system,
		sysgo.WithDeployerOptions(deployerOpts...),
	}
	for _, batcherOpt := range cfg.batcher {
		sysGoOpts = append(sysGoOpts, sysgo.WithBatcherOption(batcherOpt))
	}
	sysGoOpts = append(sysGoOpts, sysgo.WithDeployerPipelineOption(sysgo.WithMantleForkAtGenesis(opforks.MantleElysium)))

	return stack.Combine(sysGoOpts...)
}
