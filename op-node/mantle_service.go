package opnode

import (
	"github.com/ethereum/go-ethereum/params"

	"github.com/ethereum-optimism/optimism/op-node/config"
	"github.com/ethereum-optimism/optimism/op-node/flags"
	"github.com/ethereum-optimism/optimism/op-node/rollup"
	"github.com/ethereum-optimism/optimism/op-service/cliiface"
	opflags "github.com/ethereum-optimism/optimism/op-service/flags"
)

// applyMantleOverrides applies the mantle overrides to the rollup config.
// Mantle overrides come from either the hardcoded config or the CLI flags.
func applyMantleOverrides(ctx cliiface.Context, rollupConfig *rollup.Config) {
	hardCodedConfig := params.GetUpgradeConfigForMantle(rollupConfig.L2ChainID)
	if hardCodedConfig != nil {
		rollupConfig.MantleBaseFeeTime = hardCodedConfig.BaseFeeTime
		rollupConfig.MantleEverestTime = hardCodedConfig.MantleEverestTime
		// No consensus&execution update for Euboea, just use the same as Everest
		rollupConfig.MantleEuboeaTime = hardCodedConfig.MantleEverestTime
		rollupConfig.MantleSkadiTime = hardCodedConfig.MantleSkadiTime
		rollupConfig.MantleLimbTime = hardCodedConfig.MantleLimbTime
		rollupConfig.MantleArsiaTime = hardCodedConfig.MantleArsiaTime
		rollupConfig.MantleElysiumTime = hardCodedConfig.MantleElysiumTime
	}

	for _, fork := range opflags.OverridableMantleForks {
		flagName := opflags.MantleOverrideName(fork)
		if ctx.IsSet(flagName) {
			timestamp := ctx.Uint64(flagName)
			rollupConfig.SetMantleActivationTime(fork, &timestamp)
		}
	}
}

// NewL2SyncEndpointConfig returns a pointer to a L2SyncEndpointConfig.
func NewL2SyncEndpointConfig(ctx cliiface.Context) *config.L2SyncEndpointConfig {
	return &config.L2SyncEndpointConfig{
		Enabled:    ctx.Bool(flags.EnableBackupSync.Name),
		L2NodeAddr: ctx.String(flags.BackupL2UnsafeSyncRPC.Name),
		TrustRPC:   ctx.Bool(flags.BackupL2UnsafeSyncRPCTrustRPC.Name),
	}
}
