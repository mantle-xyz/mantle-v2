package helpers

import (
	actionsHelpers "github.com/ethereum-optimism/optimism/op-e2e/actions/helpers"
	mantleUpgradeHelpers "github.com/ethereum-optimism/optimism/op-e2e/actions/mantleupgrades/helpers"
	"github.com/ethereum-optimism/optimism/op-e2e/e2eutils"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/params"
	"github.com/stretchr/testify/require"
)

// glamsterdamL1BlobSchedule mirrors the blob schedule installed by
// op-devstack/sysgo.WithDefaultBPOBlobSchedule: it must include an entry for
// every L1 fork the helper activates, otherwise the chain config validator
// rejects with "missing entry for fork ... in blobSchedule". op-geth does not
// yet expose Default BlobConfigs for BPO5 / Amsterdam, so we inline reasonable
// values (matching BPO4) for those two.
var glamsterdamL1BlobSchedule = &params.BlobScheduleConfig{
	Cancun: params.DefaultCancunBlobConfig,
	Prague: params.DefaultPragueBlobConfig,
	Osaka:  params.DefaultOsakaBlobConfig,
	BPO1:   params.DefaultBPO1BlobConfig,
	BPO2:   params.DefaultBPO2BlobConfig,
	BPO3:   params.DefaultBPO3BlobConfig,
	BPO4:   params.DefaultBPO4BlobConfig,
	BPO5: &params.BlobConfig{
		Target:         14,
		Max:            21,
		UpdateFraction: 13739630,
	},
	Amsterdam: &params.BlobConfig{
		Target:         14,
		Max:            21,
		UpdateFraction: 13739630,
	},
}

func MakeL1GlamsterdamL2ArsiaDeployParams(t require.TestingT, tp *e2eutils.TestParams, amsterdamOffset *hexutil.Uint64) *e2eutils.DeployParams {
	dp := e2eutils.MakeMantleDeployParams(t, tp)
	zero := hexutil.Uint64(0)

	mantleUpgradeHelpers.ApplyArsiaTimeOffset(dp, &zero)
	dp.DeployConfig.L1CancunTimeOffset = &zero
	dp.DeployConfig.L1PragueTimeOffset = &zero
	dp.DeployConfig.L1OsakaTimeOffset = &zero
	dp.DeployConfig.L1BPO1TimeOffset = &zero
	dp.DeployConfig.L1BPO2TimeOffset = &zero
	dp.DeployConfig.L1BPO3TimeOffset = &zero
	dp.DeployConfig.L1BPO4TimeOffset = &zero
	dp.DeployConfig.L1BPO5TimeOffset = &zero
	dp.DeployConfig.L1AmsterdamTimeOffset = amsterdamOffset
	dp.DeployConfig.L1BlobScheduleConfig = glamsterdamL1BlobSchedule

	return dp
}

func DefaultRollupTestParams() *e2eutils.TestParams {
	return actionsHelpers.DefaultRollupTestParams()
}
