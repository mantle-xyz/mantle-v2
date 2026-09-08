package genesis

import (
	"testing"

	"github.com/ethereum-optimism/optimism/op-core/forks"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/stretchr/testify/require"
)

func TestSolidityMantleForkNumberElysium(t *testing.T) {
	schedule := DefaultMantleHardforkSchedule()
	schedule.ActivateMantleForkAtGenesis(forks.MantleElysium)

	require.Equal(t, int64(6), schedule.SolidityMantleForkNumber(1))
}

func TestActivateMantleForkAtOffsetElysiumKeepsArsiaOpForks(t *testing.T) {
	schedule := &UpgradeScheduleDeployConfig{}
	elysiumOffset := uint64(12)

	schedule.ActivateMantleForkAtOffset(forks.MantleElysium, elysiumOffset)

	requireOffset(t, schedule.L2GenesisMantleArsiaTimeOffset, 0)
	requireOffset(t, schedule.L2GenesisMantleElysiumTimeOffset, elysiumOffset)
	requireOffset(t, schedule.L2GenesisCanyonTimeOffset, 0)
	requireOffset(t, schedule.L2GenesisDeltaTimeOffset, 0)
	requireOffset(t, schedule.L2GenesisEcotoneTimeOffset, 0)
	requireOffset(t, schedule.L2GenesisFjordTimeOffset, 0)
	requireOffset(t, schedule.L2GenesisGraniteTimeOffset, 0)
	requireOffset(t, schedule.L2GenesisHoloceneTimeOffset, 0)
	requireOffset(t, schedule.L2GenesisIsthmusTimeOffset, 0)
	requireOffset(t, schedule.L2GenesisJovianTimeOffset, 0)
}

func TestActivateMantleForkAtOffsetBeforeArsiaClearsOpForks(t *testing.T) {
	schedule := &UpgradeScheduleDeployConfig{}

	schedule.ActivateMantleForkAtGenesis(forks.MantleLimb)

	require.Nil(t, schedule.L2GenesisMantleArsiaTimeOffset)
	require.Nil(t, schedule.L2GenesisCanyonTimeOffset)
	require.Nil(t, schedule.L2GenesisDeltaTimeOffset)
	require.Nil(t, schedule.L2GenesisEcotoneTimeOffset)
	require.Nil(t, schedule.L2GenesisFjordTimeOffset)
	require.Nil(t, schedule.L2GenesisGraniteTimeOffset)
	require.Nil(t, schedule.L2GenesisHoloceneTimeOffset)
	require.Nil(t, schedule.L2GenesisIsthmusTimeOffset)
	require.Nil(t, schedule.L2GenesisJovianTimeOffset)
}

func requireOffset(t *testing.T, offset *hexutil.Uint64, want uint64) {
	t.Helper()
	require.NotNil(t, offset)
	require.Equal(t, want, uint64(*offset))
}
