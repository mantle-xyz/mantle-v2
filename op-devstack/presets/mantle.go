package presets

import (
	bss "github.com/ethereum-optimism/optimism/op-batcher/batcher"
	"github.com/ethereum-optimism/optimism/op-core/forks"
	"github.com/ethereum-optimism/optimism/op-devstack/stack"
	"github.com/ethereum-optimism/optimism/op-devstack/sysgo"
	"github.com/ethereum-optimism/optimism/op-node/rollup/derive"
)

// WithMantleLegacyBatcher configures the pre-span-batch batcher (singular batches + zlib), as used
// by the pre-Arsia forks. The Skadi/Limb genesis presets that composed it were removed along with
// their suites (their 2^50 gas limit exceeds SystemConfig.MAX_GAS_LIMIT, so they could never run
// under sysgo); sync_tester_hfs still uses this batcher option directly.
func WithMantleLegacyBatcher() stack.CommonOption {
	return stack.MakeCommon(sysgo.WithBatcherOption(func(_ stack.L2BatcherID, cfg *bss.CLIConfig) {
		cfg.BatchType = derive.SingularBatchType
		cfg.CompressionAlgo = derive.Zlib
	}))
}

func WithMantleArsiaAtGenesis() stack.CommonOption {
	return stack.MakeCommon(sysgo.WithDeployerPipelineOption(sysgo.WithMantleForkAtGenesis(forks.MantleArsia)))
}
