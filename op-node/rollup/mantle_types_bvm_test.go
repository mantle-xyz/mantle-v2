package rollup

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMantleHoodiBVMUpgradeTime(t *testing.T) {
	var cfg Config
	require.False(t, cfg.IsMantleHoodiBVMUpgrade(1<<62), "unset fork is never active")

	ts := uint64(1_000)
	cfg.MantleHoodiBVMUpgradeTime = &ts
	require.False(t, cfg.IsMantleHoodiBVMUpgrade(999))
	require.True(t, cfg.IsMantleHoodiBVMUpgrade(1_000))
	require.True(t, cfg.IsMantleHoodiBVMUpgrade(1_001))
}

// The field must round-trip through the strict rollup.json decoder (DisallowUnknownFields), so a
// Hoodi rollup.json carrying it still loads.
func TestMantleHoodiBVMUpgradeTimeJSON(t *testing.T) {
	var cfg Config
	err := cfg.ParseRollupConfig(bytes.NewBufferString(`{"mantle_hoodi_bvm_upgrade_time": 1234}`))
	require.NoError(t, err)
	require.NotNil(t, cfg.MantleHoodiBVMUpgradeTime)
	require.Equal(t, uint64(1234), *cfg.MantleHoodiBVMUpgradeTime)
}
