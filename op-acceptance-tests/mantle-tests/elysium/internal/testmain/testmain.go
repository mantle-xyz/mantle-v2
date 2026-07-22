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

// DefaultAmsterdamOffset is the activation offset for cases that genuinely exercise the L1 fork
// transition -- deriving across the activation block, ingesting the first Amsterdam headers, and
// so on. It leaves a comfortable pre-Amsterdam window for a test to observe before the boundary.
const DefaultAmsterdamOffset = uint64(30)

// FastAmsterdamOffset is for cases whose assertions are L1-fork-INDEPENDENT: they check a property
// of the Arsia L2 itself (RPC schema, header fields, EVM gas rules, code-size limit) and would
// hold under any L1. Those tests still cross Amsterdam so the L2 is genuinely running against a
// Glamsterdam L1, but the pre-boundary window buys them nothing -- it is pure CI wall-clock. Every
// package on this offset waits the whole thing before its first assertion, so keep it small.
//
// Use DefaultAmsterdamOffset instead whenever a test observes anything BEFORE the boundary.
const FastAmsterdamOffset = uint64(6)

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
