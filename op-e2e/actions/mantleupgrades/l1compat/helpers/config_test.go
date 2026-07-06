package helpers

import (
	"testing"

	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/stretchr/testify/require"
)

func TestMakeL1GlamsterdamL2ArsiaDeployParams(t *testing.T) {
	amsterdamOffset := hexutil.Uint64(24)

	dp := MakeL1GlamsterdamL2ArsiaDeployParams(t, DefaultRollupTestParams(), &amsterdamOffset)

	require.NotNil(t, dp.DeployConfig.L2GenesisMantleArsiaTimeOffset)
	require.Equal(t, hexutil.Uint64(0), *dp.DeployConfig.L2GenesisMantleArsiaTimeOffset)
	require.NotNil(t, dp.DeployConfig.L2GenesisMantleLimbTimeOffset)
	require.Equal(t, hexutil.Uint64(0), *dp.DeployConfig.L2GenesisMantleLimbTimeOffset)

	require.NotNil(t, dp.DeployConfig.L1CancunTimeOffset)
	require.Equal(t, hexutil.Uint64(0), *dp.DeployConfig.L1CancunTimeOffset)
	require.NotNil(t, dp.DeployConfig.L1PragueTimeOffset)
	require.Equal(t, hexutil.Uint64(0), *dp.DeployConfig.L1PragueTimeOffset)
	require.NotNil(t, dp.DeployConfig.L1OsakaTimeOffset)
	require.Equal(t, hexutil.Uint64(0), *dp.DeployConfig.L1OsakaTimeOffset)
	require.NotNil(t, dp.DeployConfig.L1BPO1TimeOffset)
	require.Equal(t, hexutil.Uint64(0), *dp.DeployConfig.L1BPO1TimeOffset)
	require.NotNil(t, dp.DeployConfig.L1BPO2TimeOffset)
	require.Equal(t, hexutil.Uint64(0), *dp.DeployConfig.L1BPO2TimeOffset)
	require.NotNil(t, dp.DeployConfig.L1BPO3TimeOffset)
	require.Equal(t, hexutil.Uint64(0), *dp.DeployConfig.L1BPO3TimeOffset)
	require.NotNil(t, dp.DeployConfig.L1BPO4TimeOffset)
	require.Equal(t, hexutil.Uint64(0), *dp.DeployConfig.L1BPO4TimeOffset)
	require.NotNil(t, dp.DeployConfig.L1BPO5TimeOffset)
	require.Equal(t, hexutil.Uint64(0), *dp.DeployConfig.L1BPO5TimeOffset)

	require.NotNil(t, dp.DeployConfig.L1AmsterdamTimeOffset)
	require.Equal(t, amsterdamOffset, *dp.DeployConfig.L1AmsterdamTimeOffset)

	require.NotNil(t, dp.DeployConfig.L1BlobScheduleConfig)
	require.Equal(t, 14, dp.DeployConfig.L1BlobScheduleConfig.BPO5.Target)
	require.Equal(t, 21, dp.DeployConfig.L1BlobScheduleConfig.BPO5.Max)
	require.Equal(t, uint64(11684671), dp.DeployConfig.L1BlobScheduleConfig.BPO5.UpdateFraction)
	require.Equal(t, 14, dp.DeployConfig.L1BlobScheduleConfig.Amsterdam.Target)
	require.Equal(t, 21, dp.DeployConfig.L1BlobScheduleConfig.Amsterdam.Max)
	require.Equal(t, uint64(11684671), dp.DeployConfig.L1BlobScheduleConfig.Amsterdam.UpdateFraction)
}
