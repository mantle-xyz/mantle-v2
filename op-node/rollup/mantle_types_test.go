package rollup

import (
	"fmt"
	"reflect"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ethereum-optimism/optimism/op-core/forks"
	"github.com/ethereum/go-ethereum/params"
)

// TestMantleActivations tests the activation condition of the various Mantle upgrades.
func TestMantleActivations(t *testing.T) {
	for _, test := range []struct {
		name           string
		setUpgradeTime func(t *uint64, c *Config)
		checkEnabled   func(t uint64, c *Config) bool
	}{
		{
			name: "MantleBaseFee",
			setUpgradeTime: func(t *uint64, c *Config) {
				c.MantleBaseFeeTime = t
			},
			checkEnabled: func(t uint64, c *Config) bool {
				return c.IsMantleBaseFee(t)
			},
		},
		{
			name: "MantleEverest",
			setUpgradeTime: func(t *uint64, c *Config) {
				c.MantleEverestTime = t
			},
			checkEnabled: func(t uint64, c *Config) bool {
				return c.IsMantleEverest(t)
			},
		},
		{
			name: "MantleEuboea",
			setUpgradeTime: func(t *uint64, c *Config) {
				c.MantleEuboeaTime = t
			},
			checkEnabled: func(t uint64, c *Config) bool {
				return c.IsMantleEuboea(t)
			},
		},
		{
			name: "MantleSkadi",
			setUpgradeTime: func(t *uint64, c *Config) {
				c.MantleSkadiTime = t
			},
			checkEnabled: func(t uint64, c *Config) bool {
				return c.IsMantleSkadi(t)
			},
		},
		{
			name: "MantleLimb",
			setUpgradeTime: func(t *uint64, c *Config) {
				c.MantleLimbTime = t
			},
			checkEnabled: func(t uint64, c *Config) bool {
				return c.IsMantleLimb(t)
			},
		},
		{
			name: "MantleArsia",
			setUpgradeTime: func(t *uint64, c *Config) {
				c.MantleArsiaTime = t
			},
			checkEnabled: func(t uint64, c *Config) bool {
				return c.IsMantleArsia(t)
			},
		},
	} {
		tt := test
		t.Run(fmt.Sprintf("TestMantleActivations_%s", tt.name), func(t *testing.T) {
			config := randConfig()
			test.setUpgradeTime(nil, config)
			require.False(t, tt.checkEnabled(0, config), "false if nil time, even if checking 0")
			require.False(t, tt.checkEnabled(123456, config), "false if nil time")

			test.setUpgradeTime(new(uint64), config)
			require.True(t, tt.checkEnabled(0, config), "true at zero")
			require.True(t, tt.checkEnabled(123456, config), "true for any")

			x := uint64(123)
			test.setUpgradeTime(&x, config)
			require.False(t, tt.checkEnabled(0, config))
			require.False(t, tt.checkEnabled(122, config))
			require.True(t, tt.checkEnabled(123, config))
			require.True(t, tt.checkEnabled(124, config))
		})
	}
}

// TestMantleActivationBlocks tests the activation block detection for Mantle forks.
func TestMantleActivationBlocks(t *testing.T) {
	tests := []struct {
		name    string
		setTime func(cfg *Config, ts uint64)
		check   func(cfg *Config, ts uint64) bool
	}{
		{
			name:    "MantleBaseFee",
			setTime: func(cfg *Config, ts uint64) { cfg.MantleBaseFeeTime = &ts },
			check:   func(cfg *Config, ts uint64) bool { return cfg.IsMantleBaseFeeActivationBlock(ts) },
		},
		{
			name:    "MantleEverest",
			setTime: func(cfg *Config, ts uint64) { cfg.MantleEverestTime = &ts },
			check:   func(cfg *Config, ts uint64) bool { return cfg.IsMantleEverestActivationBlock(ts) },
		},
		{
			name:    "MantleEuboea",
			setTime: func(cfg *Config, ts uint64) { cfg.MantleEuboeaTime = &ts },
			check:   func(cfg *Config, ts uint64) bool { return cfg.IsMantleEuboeaActivationBlock(ts) },
		},
		{
			name:    "MantleSkadi",
			setTime: func(cfg *Config, ts uint64) { cfg.MantleSkadiTime = &ts },
			check:   func(cfg *Config, ts uint64) bool { return cfg.IsMantleSkadiActivationBlock(ts) },
		},
		{
			name:    "MantleLimb",
			setTime: func(cfg *Config, ts uint64) { cfg.MantleLimbTime = &ts },
			check:   func(cfg *Config, ts uint64) bool { return cfg.IsMantleLimbActivationBlock(ts) },
		},
		{
			name:    "MantleArsia",
			setTime: func(cfg *Config, ts uint64) { cfg.MantleArsiaTime = &ts },
			check:   func(cfg *Config, ts uint64) bool { return cfg.IsMantleArsiaActivationBlock(ts) },
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &Config{BlockTime: 2}
			ts := uint64(100)
			tc.setTime(cfg, ts)

			// Should be true for the first block at/after activation
			require.True(t, tc.check(cfg, ts))
			require.True(t, tc.check(cfg, ts+cfg.BlockTime-1))

			// Should be false before activation
			require.False(t, tc.check(cfg, ts-1))
			require.False(t, tc.check(cfg, ts-cfg.BlockTime))

			// Should be false after the activation block window
			require.False(t, tc.check(cfg, ts+cfg.BlockTime))
			require.False(t, tc.check(cfg, ts+cfg.BlockTime*2))

			// Should be false if block time is less than BlockTime
			require.False(t, tc.check(cfg, cfg.BlockTime-1))
		})
	}
}

// TestConfig_MantleActivationTime tests that all getters and setters for all scheduleable Mantle forks are
// present and working.
func TestConfig_MantleActivationTime(t *testing.T) {
	for i, fork := range scheduleableMantleForks {
		t.Run(string(fork), func(t *testing.T) {
			var cfg Config
			ts := uint64((i + 1) * 1000)
			cfg.SetMantleActivationTime(fork, &ts)
			gts := cfg.MantleActivationTime(fork)
			require.NotNil(t, gts)
			require.Equal(t, ts, *gts, "activation time for fork %s", fork)

			// Reflectively call Is<MantleForkName> (e.g. MantleBaseFee -> IsMantleBaseFee).
			// Fork constants use PascalCase in op-core/forks (not snake_case mantle_base_fee).
			methodName := "Is" + string(fork)
			m := reflect.ValueOf(&cfg).MethodByName(methodName)
			require.True(t, m.IsValid(), "method %s not found", methodName)
			out := m.Call([]reflect.Value{reflect.ValueOf(ts)})
			require.Len(t, out, 1)
			require.True(t, out[0].Bool(), "%s(%d) should be true", methodName, ts)
			prev := ts - 1
			out = m.Call([]reflect.Value{reflect.ValueOf(prev)})
			require.Len(t, out, 1)
			require.False(t, out[0].Bool(), "%s(%d) should be false", methodName, prev)
		})
	}
}

// TestConfig_MantleActivationTime_UnknownFork tests that MantleActivationTime panics on unknown fork.
func TestConfig_MantleActivationTime_UnknownFork(t *testing.T) {
	var cfg Config
	require.Panics(t, func() {
		cfg.MantleActivationTime(MantleForkName("unknown_fork"))
	})
}

// TestConfig_SetMantleActivationTime_UnknownFork tests that SetMantleActivationTime panics on unknown fork.
func TestConfig_SetMantleActivationTime_UnknownFork(t *testing.T) {
	var cfg Config
	ts := uint64(100)
	require.Panics(t, func() {
		cfg.SetMantleActivationTime(MantleForkName("unknown_fork"), &ts)
	})
}

// TestConfig_MantleActivationTime_MantleNoSupport tests that MantleNoSupport returns nil.
func TestConfig_MantleActivationTime_MantleNoSupport(t *testing.T) {
	var cfg Config
	result := cfg.MantleActivationTime(forks.MantleNoSupport)
	require.Nil(t, result)
}

// TestConfig_MantleActivateAt validates MantleActivateAt behavior.
func TestConfig_MantleActivateAt(t *testing.T) {
	// Activate at MantleEverest with a non-zero timestamp
	t.Run("MantleEverest", func(t *testing.T) {
		var cfg Config
		ts := uint64(100)
		cfg.MantleActivateAt(forks.MantleEverest, ts)
		// Prior forks should be set to 0 (genesis)
		require.Equal(t, uint64(0), *cfg.MantleBaseFeeTime)
		// Target fork should be set to the timestamp
		require.Equal(t, ts, *cfg.MantleEverestTime)
		// Later forks should be nil
		require.Nil(t, cfg.MantleEuboeaTime)
		require.Nil(t, cfg.MantleSkadiTime)
		require.Nil(t, cfg.MantleLimbTime)
		require.Nil(t, cfg.MantleArsiaTime)
	})

	t.Run("MantleArsia", func(t *testing.T) {
		var cfg Config
		ts := uint64(100)
		cfg.MantleActivateAt(forks.MantleArsia, ts)
		// Forks up to and including the target are set; later forks (e.g. Elysium) stay nil.
		for _, f := range scheduleableMantleForks {
			at := cfg.MantleActivationTime(f)
			require.NotNil(t, at)
			if f == forks.MantleArsia {
				require.EqualValues(t, ts, *at) // target fork set to the timestamp
				break
			}
			require.Zero(t, *at) // prior forks set to 0 (genesis)
		}
	})

	t.Run("MantleBaseFee", func(t *testing.T) {
		var cfg Config
		ts := uint64(100)
		cfg.MantleActivateAt(forks.MantleBaseFee, ts)
		require.Equal(t, ts, *cfg.MantleBaseFeeTime)
		require.Nil(t, cfg.MantleEverestTime)
		require.Nil(t, cfg.MantleEuboeaTime)
		require.Nil(t, cfg.MantleSkadiTime)
		require.Nil(t, cfg.MantleLimbTime)
		require.Nil(t, cfg.MantleArsiaTime)
	})

	t.Run("InvalidFork", func(t *testing.T) {
		var cfg Config
		require.Panics(t, func() {
			cfg.MantleActivateAt(MantleForkName("invalid_fork"), 100)
		})
	})
}

// TestConfig_MantleActivateAtGenesis validates MantleActivateAtGenesis behavior.
func TestConfig_MantleActivateAtGenesis(t *testing.T) {
	// Activate MantleEverest at genesis
	t.Run("MantleEverest", func(t *testing.T) {
		var cfg Config
		cfg.MantleActivateAtGenesis(forks.MantleEverest)
		zero := uint64(0)
		require.Equal(t, zero, *cfg.MantleBaseFeeTime)
		require.Equal(t, zero, *cfg.MantleEverestTime)
		require.Nil(t, cfg.MantleEuboeaTime)
		require.Nil(t, cfg.MantleSkadiTime)
		require.Nil(t, cfg.MantleLimbTime)
		require.Nil(t, cfg.MantleArsiaTime)
	})

	t.Run("MantleArsia", func(t *testing.T) {
		var cfg Config
		cfg.MantleActivateAtGenesis(forks.MantleArsia)
		// Forks up to and including Arsia are activated at genesis (0); later forks stay nil.
		for _, f := range scheduleableMantleForks {
			at := cfg.MantleActivationTime(f)
			require.NotNil(t, at)
			require.Zero(t, *at)
			if f == forks.MantleArsia {
				break
			}
		}
	})
}

// TestCheckMantleForks tests the fork ordering validation.
func TestCheckMantleForks(t *testing.T) {
	tests := []struct {
		name        string
		modifier    func(cfg *Config)
		expectedErr error
	}{
		{
			name: "AllForksInOrder",
			modifier: func(cfg *Config) {
				baseFeeTime := uint64(1)
				everestTime := uint64(2)
				euboeaTime := uint64(3)
				skadiTime := uint64(4)
				limbTime := uint64(5)
				arsiaTime := uint64(6)
				cfg.MantleBaseFeeTime = &baseFeeTime
				cfg.MantleEverestTime = &everestTime
				cfg.MantleEuboeaTime = &euboeaTime
				cfg.MantleSkadiTime = &skadiTime
				cfg.MantleLimbTime = &limbTime
				cfg.MantleArsiaTime = &arsiaTime
			},
			expectedErr: nil,
		},
		{
			name: "AllForksSameTime",
			modifier: func(cfg *Config) {
				ts := uint64(100)
				cfg.MantleBaseFeeTime = &ts
				cfg.MantleEverestTime = &ts
				cfg.MantleEuboeaTime = &ts
				cfg.MantleSkadiTime = &ts
				cfg.MantleLimbTime = &ts
				cfg.MantleArsiaTime = &ts
			},
			expectedErr: nil,
		},
		{
			name: "PriorForkMissing",
			modifier: func(cfg *Config) {
				everestTime := uint64(2)
				cfg.MantleEverestTime = &everestTime
			},
			expectedErr: fmt.Errorf("mantle fork MantleEverest set (to 2), but prior fork MantleBaseFee missing"),
		},
		{
			name: "PriorForkHasHigherOffset",
			modifier: func(cfg *Config) {
				baseFeeTime := uint64(2)
				everestTime := uint64(1)
				cfg.MantleBaseFeeTime = &baseFeeTime
				cfg.MantleEverestTime = &everestTime
			},
			expectedErr: fmt.Errorf("mantle fork MantleEverest set to 1, but prior fork MantleBaseFee has higher offset 2"),
		},
		{
			name: "LaterForkMissing",
			modifier: func(cfg *Config) {
				baseFeeTime := uint64(1)
				everestTime := uint64(2)
				cfg.MantleBaseFeeTime = &baseFeeTime
				cfg.MantleEverestTime = &everestTime
				// Euboea, Skadi, Limb, Arsia are nil - should be OK
			},
			expectedErr: nil,
		},
		{
			name: "AllNil",
			modifier: func(cfg *Config) {
				// All forks nil - should be OK
			},
			expectedErr: nil,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := randConfig()
			test.modifier(cfg)
			err := cfg.CheckMantleForks()
			if test.expectedErr == nil {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
				require.Equal(t, test.expectedErr.Error(), err.Error())
			}
		})
	}
}

// TestConfig_IsMantleForkActive validates the generic IsMantleForkActive API across forks.
func TestConfig_IsMantleForkActive(t *testing.T) {
	for _, fork := range scheduleableMantleForks {
		t.Run(string(fork), func(t *testing.T) {
			var cfg Config
			// nil activation time => always inactive
			require.False(t, cfg.IsMantleForkActive(fork, 0))
			require.False(t, cfg.IsMantleForkActive(fork, 123))

			// activation at genesis (0) => active for all timestamps
			zero := uint64(0)
			cfg.SetMantleActivationTime(fork, &zero)
			require.True(t, cfg.IsMantleForkActive(fork, 0))
			require.True(t, cfg.IsMantleForkActive(fork, 999999))

			// activation at specific timestamp
			at := uint64(100)
			cfg.SetMantleActivationTime(fork, &at)
			require.False(t, cfg.IsMantleForkActive(fork, 99))
			require.True(t, cfg.IsMantleForkActive(fork, 100))
			require.True(t, cfg.IsMantleForkActive(fork, 101))
		})
	}
}

// TestAlignOpWithMantle tests the AlignOpWithMantle function.
func TestAlignOpWithMantle(t *testing.T) {
	t.Run("NoMantleForks", func(t *testing.T) {
		cfg := randConfig()
		// All Mantle fork times are nil by default
		err := cfg.AlignOpWithMantle()
		require.NoError(t, err)
		require.Nil(t, cfg.MantleBaseFeeTime)
		require.Nil(t, cfg.MantleEverestTime)
		require.Nil(t, cfg.MantleEuboeaTime)
		require.Nil(t, cfg.MantleSkadiTime)
		require.Nil(t, cfg.MantleLimbTime)
		require.Nil(t, cfg.MantleArsiaTime)
		// Optimism forks should also be nil when MantleArsiaTime is nil
		require.Nil(t, cfg.CanyonTime)
		require.Nil(t, cfg.DeltaTime)
		require.Nil(t, cfg.EcotoneTime)
		require.Nil(t, cfg.FjordTime)
		require.Nil(t, cfg.GraniteTime)
		require.Nil(t, cfg.HoloceneTime)
		require.Nil(t, cfg.IsthmusTime)
		require.Nil(t, cfg.JovianTime)
		// ChainOpConfig should be set with default values
		require.NotNil(t, cfg.ChainOpConfig)
		require.Equal(t, uint64(2), cfg.ChainOpConfig.EIP1559Elasticity)
		require.Equal(t, uint64(8), cfg.ChainOpConfig.EIP1559Denominator)
		require.NotNil(t, cfg.ChainOpConfig.EIP1559DenominatorCanyon)
		require.Equal(t, cfg.ChainOpConfig.EIP1559Denominator, *cfg.ChainOpConfig.EIP1559DenominatorCanyon)
	})

	t.Run("WithMantleForks", func(t *testing.T) {
		cfg := randConfig()
		baseFeeTime := uint64(100)
		everestTime := uint64(200)
		euboeaTime := uint64(200) // Euboea uses Everest time
		skadiTime := uint64(300)
		limbTime := uint64(400)
		arsiaTime := uint64(500)

		// Set Mantle fork times directly on the config
		cfg.MantleBaseFeeTime = &baseFeeTime
		cfg.MantleEverestTime = &everestTime
		cfg.MantleEuboeaTime = &euboeaTime
		cfg.MantleSkadiTime = &skadiTime
		cfg.MantleLimbTime = &limbTime
		cfg.MantleArsiaTime = &arsiaTime

		err := cfg.AlignOpWithMantle()
		require.NoError(t, err)

		// Verify Mantle fork times are unchanged
		require.NotNil(t, cfg.MantleBaseFeeTime)
		require.Equal(t, baseFeeTime, *cfg.MantleBaseFeeTime)
		require.NotNil(t, cfg.MantleEverestTime)
		require.Equal(t, everestTime, *cfg.MantleEverestTime)
		require.NotNil(t, cfg.MantleEuboeaTime)
		require.Equal(t, euboeaTime, *cfg.MantleEuboeaTime)
		require.NotNil(t, cfg.MantleSkadiTime)
		require.Equal(t, skadiTime, *cfg.MantleSkadiTime)
		require.NotNil(t, cfg.MantleLimbTime)
		require.Equal(t, limbTime, *cfg.MantleLimbTime)
		require.NotNil(t, cfg.MantleArsiaTime)
		require.Equal(t, arsiaTime, *cfg.MantleArsiaTime)

		// Verify Optimism forks are mapped to MantleArsiaTime
		require.NotNil(t, cfg.CanyonTime)
		require.Equal(t, arsiaTime, *cfg.CanyonTime)
		require.NotNil(t, cfg.DeltaTime)
		require.Equal(t, arsiaTime, *cfg.DeltaTime)
		require.NotNil(t, cfg.EcotoneTime)
		require.Equal(t, arsiaTime, *cfg.EcotoneTime)
		require.NotNil(t, cfg.FjordTime)
		require.Equal(t, arsiaTime, *cfg.FjordTime)
		require.NotNil(t, cfg.GraniteTime)
		require.Equal(t, arsiaTime, *cfg.GraniteTime)
		require.NotNil(t, cfg.HoloceneTime)
		require.Equal(t, arsiaTime, *cfg.HoloceneTime)
		require.NotNil(t, cfg.IsthmusTime)
		require.Equal(t, arsiaTime, *cfg.IsthmusTime)
		require.NotNil(t, cfg.JovianTime)
		require.Equal(t, arsiaTime, *cfg.JovianTime)

		// Verify that all Optimism forks are active at the same timestamp as MantleArsia
		require.True(t, cfg.IsCanyon(arsiaTime), "Canyon should be active at MantleArsia activation time")
		require.True(t, cfg.IsDelta(arsiaTime), "Delta should be active at MantleArsia activation time")
		require.True(t, cfg.IsEcotone(arsiaTime), "Ecotone should be active at MantleArsia activation time")
		require.True(t, cfg.IsFjord(arsiaTime), "Fjord should be active at MantleArsia activation time")
		require.True(t, cfg.IsGranite(arsiaTime), "Granite should be active at MantleArsia activation time")
		require.True(t, cfg.IsHolocene(arsiaTime), "Holocene should be active at MantleArsia activation time")
		require.True(t, cfg.IsIsthmus(arsiaTime), "Isthmus should be active at MantleArsia activation time")
		require.True(t, cfg.IsJovian(arsiaTime), "Jovian should be active at MantleArsia activation time")
		require.True(t, cfg.IsMantleArsia(arsiaTime), "MantleArsia should be active at its activation time")

		// Verify they're not active before MantleArsia activation
		if arsiaTime > 0 {
			beforeTime := arsiaTime - 1
			require.False(t, cfg.IsCanyon(beforeTime), "Canyon should not be active before MantleArsia")
			require.False(t, cfg.IsDelta(beforeTime), "Delta should not be active before MantleArsia")
			require.False(t, cfg.IsEcotone(beforeTime), "Ecotone should not be active before MantleArsia")
			require.False(t, cfg.IsFjord(beforeTime), "Fjord should not be active before MantleArsia")
			require.False(t, cfg.IsGranite(beforeTime), "Granite should not be active before MantleArsia")
			require.False(t, cfg.IsHolocene(beforeTime), "Holocene should not be active before MantleArsia")
			require.False(t, cfg.IsIsthmus(beforeTime), "Isthmus should not be active before MantleArsia")
			require.False(t, cfg.IsJovian(beforeTime), "Jovian should not be active before MantleArsia")
			require.False(t, cfg.IsMantleArsia(beforeTime), "MantleArsia should not be active before its activation time")
		}

		// Verify ChainOpConfig is set with default values
		require.NotNil(t, cfg.ChainOpConfig)
		require.Equal(t, uint64(2), cfg.ChainOpConfig.EIP1559Elasticity)
		require.Equal(t, uint64(8), cfg.ChainOpConfig.EIP1559Denominator)
		require.NotNil(t, cfg.ChainOpConfig.EIP1559DenominatorCanyon)
		require.Equal(t, cfg.ChainOpConfig.EIP1559Denominator, *cfg.ChainOpConfig.EIP1559DenominatorCanyon)
	})

	t.Run("ChainOpConfigAlreadySet", func(t *testing.T) {
		cfg := randConfig()
		cfg.ChainOpConfig = &params.OptimismConfig{
			EIP1559Elasticity:  8,
			EIP1559Denominator: 100,
		}

		// Set Mantle fork times
		baseFeeTime := uint64(0)
		everestTime := uint64(0)
		euboeaTime := uint64(0)
		skadiTime := uint64(0)
		limbTime := uint64(0)
		arsiaTime := uint64(0)
		cfg.MantleBaseFeeTime = &baseFeeTime
		cfg.MantleEverestTime = &everestTime
		cfg.MantleEuboeaTime = &euboeaTime
		cfg.MantleSkadiTime = &skadiTime
		cfg.MantleLimbTime = &limbTime
		cfg.MantleArsiaTime = &arsiaTime

		err := cfg.AlignOpWithMantle()
		require.NoError(t, err)
		// Should preserve existing config
		require.Equal(t, uint64(8), cfg.ChainOpConfig.EIP1559Elasticity)
		require.Equal(t, uint64(100), cfg.ChainOpConfig.EIP1559Denominator)
		require.NotNil(t, cfg.ChainOpConfig.EIP1559DenominatorCanyon)
		require.Equal(t, cfg.ChainOpConfig.EIP1559Denominator, *cfg.ChainOpConfig.EIP1559DenominatorCanyon)
	})

	t.Run("InvalidForkOrdering", func(t *testing.T) {
		cfg := randConfig()
		// Set forks in wrong order (Everest before BaseFee)
		everestTime := uint64(100)
		baseFeeTime := uint64(200)
		cfg.MantleEverestTime = &everestTime
		cfg.MantleBaseFeeTime = &baseFeeTime

		err := cfg.AlignOpWithMantle()
		require.Error(t, err)
		require.Contains(t, err.Error(), "mantle fork MantleEverest set to 100, but prior fork MantleBaseFee has higher offset 200")
	})

}
