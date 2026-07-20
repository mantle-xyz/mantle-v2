package rollup

import (
	"fmt"

	"github.com/ethereum-optimism/optimism/op-core/forks"
	"github.com/ethereum/go-ethereum/params"
)

// IsMantleBaseFee returns true if the MantleBaseFee hardfork is active at or past the given timestamp.
func (c *Config) IsMantleBaseFee(timestamp uint64) bool {
	return c.IsMantleForkActive(forks.MantleBaseFee, timestamp)
}

// IsMantleEverest returns true if the MantleEverest hardfork is active at or past the given timestamp.
func (c *Config) IsMantleEverest(timestamp uint64) bool {
	return c.IsMantleForkActive(forks.MantleEverest, timestamp)
}

// IsMantleEuboea returns true if the MantleEuboea hardfork is active at or past the given timestamp.
func (c *Config) IsMantleEuboea(timestamp uint64) bool {
	return c.IsMantleForkActive(forks.MantleEuboea, timestamp)
}

// IsMantleSkadi returns true if the MantleSkadi hardfork is active at or past the given timestamp.
func (c *Config) IsMantleSkadi(timestamp uint64) bool {
	return c.IsMantleForkActive(forks.MantleSkadi, timestamp)
}

// IsMantleLimb returns true if the MantleLimb hardfork is active at or past the given timestamp.
func (c *Config) IsMantleLimb(timestamp uint64) bool {
	return c.IsMantleForkActive(forks.MantleLimb, timestamp)
}

// IsMantleArsia returns true if the MantleArsia hardfork is active at or past the given timestamp.
func (c *Config) IsMantleArsia(timestamp uint64) bool {
	return c.IsMantleForkActive(forks.MantleArsia, timestamp)
}

// IsMantleElysium returns true if the MantleElysium hardfork is active at or past the given timestamp.
func (c *Config) IsMantleElysium(timestamp uint64) bool {
	return c.IsMantleForkActive(forks.MantleElysium, timestamp)
}

// IsMantleBaseFeeActivationBlock returns whether the specified block is the first block subject to the
// MantleBaseFee upgrade.
func (c *Config) IsMantleBaseFeeActivationBlock(l2BlockTime uint64) bool {
	return c.IsMantleBaseFee(l2BlockTime) &&
		l2BlockTime >= c.BlockTime &&
		!c.IsMantleBaseFee(l2BlockTime-c.BlockTime)
}

// IsMantleEverestActivationBlock returns whether the specified block is the first block subject to the
// MantleEverest upgrade.
func (c *Config) IsMantleEverestActivationBlock(l2BlockTime uint64) bool {
	return c.IsMantleEverest(l2BlockTime) &&
		l2BlockTime >= c.BlockTime &&
		!c.IsMantleEverest(l2BlockTime-c.BlockTime)
}

// IsMantleEuboeaActivationBlock returns whether the specified block is the first block subject to the
// MantleEuboea upgrade.
func (c *Config) IsMantleEuboeaActivationBlock(l2BlockTime uint64) bool {
	return c.IsMantleEuboea(l2BlockTime) &&
		l2BlockTime >= c.BlockTime &&
		!c.IsMantleEuboea(l2BlockTime-c.BlockTime)
}

// IsMantleSkadiActivationBlock returns whether the specified block is the first block subject to the
// MantleSkadi upgrade.
func (c *Config) IsMantleSkadiActivationBlock(l2BlockTime uint64) bool {
	return c.IsMantleSkadi(l2BlockTime) &&
		l2BlockTime >= c.BlockTime &&
		!c.IsMantleSkadi(l2BlockTime-c.BlockTime)
}

// IsMantleLimbActivationBlock returns whether the specified block is the first block subject to the
// MantleLimb upgrade.
func (c *Config) IsMantleLimbActivationBlock(l2BlockTime uint64) bool {
	return c.IsMantleLimb(l2BlockTime) &&
		l2BlockTime >= c.BlockTime &&
		!c.IsMantleLimb(l2BlockTime-c.BlockTime)
}

// IsMantleArsiaActivationBlock returns whether the specified block is the first block subject to the
// MantleArsia upgrade.
func (c *Config) IsMantleArsiaActivationBlock(l2BlockTime uint64) bool {
	return c.IsMantleArsia(l2BlockTime) &&
		l2BlockTime >= c.BlockTime &&
		!c.IsMantleArsia(l2BlockTime-c.BlockTime)
}

// IsMantleElysiumActivationBlock returns whether the specified block is the first block subject to the
// MantleElysium upgrade.
func (c *Config) IsMantleElysiumActivationBlock(l2BlockTime uint64) bool {
	return c.IsMantleElysium(l2BlockTime) &&
		l2BlockTime >= c.BlockTime &&
		!c.IsMantleElysium(l2BlockTime-c.BlockTime)
}

func (c *Config) MantleActivationTime(fork MantleForkName) *uint64 {
	switch fork {
	case forks.MantleElysium:
		return c.MantleElysiumTime
	case forks.MantleArsia:
		return c.MantleArsiaTime
	case forks.MantleLimb:
		return c.MantleLimbTime
	case forks.MantleSkadi:
		return c.MantleSkadiTime
	case forks.MantleEuboea:
		return c.MantleEuboeaTime
	case forks.MantleEverest:
		return c.MantleEverestTime
	case forks.MantleBaseFee:
		return c.MantleBaseFeeTime
	case forks.MantleNoSupport:
		return nil
	default:
		panic(fmt.Sprintf("unknown fork: %v", fork))
	}
}

func (c *Config) SetMantleActivationTime(fork MantleForkName, timestamp *uint64) {
	switch fork {
	case forks.MantleElysium:
		c.MantleElysiumTime = timestamp
	case forks.MantleArsia:
		c.MantleArsiaTime = timestamp
	case forks.MantleLimb:
		c.MantleLimbTime = timestamp
	case forks.MantleSkadi:
		c.MantleSkadiTime = timestamp
	case forks.MantleEuboea:
		c.MantleEuboeaTime = timestamp
	case forks.MantleEverest:
		c.MantleEverestTime = timestamp
	case forks.MantleBaseFee:
		c.MantleBaseFeeTime = timestamp
	default:
		panic(fmt.Sprintf("unknown mantle fork: %v", fork))
	}
}

// IsActivationBlock returns the fork which activates at the block with time newTime if the previous
// block's time is oldTime. It returns an empty ForkName if no fork activation takes place between
// those timestamps. It can be used for both, L1 and L2 blocks.
func (c *Config) IsMantleActivationBlock(oldTime, newTime uint64) MantleForkName {
	for _, fork := range scheduleableMantleForks {
		if c.IsMantleForkActive(fork, newTime) && !c.IsMantleForkActive(fork, oldTime) {
			return fork
		}
	}
	return forks.MantleNone
}

type MantleForkName = forks.MantleForkName

var scheduleableMantleForks = forks.MantleForksFrom(forks.MantleBaseFee)

func (c *Config) MantleActivateAtGenesis(hardfork MantleForkName) {
	c.MantleActivateAt(hardfork, 0)
}

func (c *Config) MantleActivateAt(fork MantleForkName, timestamp uint64) {
	if !forks.IsValidMantleFork(fork) {
		panic(fmt.Sprintf("invalid mantle fork: %s", fork))
	}
	ts := new(uint64)
	for i, f := range scheduleableMantleForks {
		if f == fork {
			c.SetMantleActivationTime(fork, &timestamp)
			ts = nil
		} else {
			c.SetMantleActivationTime(scheduleableMantleForks[i], ts)
		}
	}
}

// AlignOpWithMantle checks mantle forks and aligns Optimism forks with them.
// It means Optimism forks activation times will be set to the corresponding Mantle forks activation times.
// It also sets the default EIP1559 parameters if not set. That parameters are required by mantle arsia.
// Note: EIP1559DenominatorCanyon is forced to be the same as EIP1559Denominator.
func (c *Config) AlignOpWithMantle() error {
	// Map Optimism forks to Mantle forks
	c.CanyonTime = c.MantleArsiaTime
	c.DeltaTime = c.MantleArsiaTime
	c.EcotoneTime = c.MantleArsiaTime
	c.FjordTime = c.MantleArsiaTime
	c.GraniteTime = c.MantleArsiaTime
	c.HoloceneTime = c.MantleArsiaTime
	c.IsthmusTime = c.MantleArsiaTime
	c.JovianTime = c.MantleArsiaTime

	if c.ChainOpConfig == nil {
		dCanyon := uint64(8)
		c.ChainOpConfig = &params.OptimismConfig{
			EIP1559Elasticity:  2,
			EIP1559Denominator: 8,
			// EIP1559DenominatorCanyon is used to translate a zero post-arsia 1559 param
			EIP1559DenominatorCanyon: &dCanyon,
		}
	} else {
		// Mantle don't have a historical change of the denominator, so we use the same as the denominator
		dCanyon := c.ChainOpConfig.EIP1559Denominator
		c.ChainOpConfig.EIP1559DenominatorCanyon = &dCanyon
	}

	return c.CheckMantleForks()
}

func (cfg *Config) CheckMantleForks() error {
	if err := checkMantleFork(cfg.MantleBaseFeeTime, cfg.MantleEverestTime, forks.MantleBaseFee, forks.MantleEverest); err != nil {
		return err
	}
	if err := checkMantleFork(cfg.MantleEverestTime, cfg.MantleEuboeaTime, forks.MantleEverest, forks.MantleEuboea); err != nil {
		return err
	}
	if err := checkMantleFork(cfg.MantleEuboeaTime, cfg.MantleSkadiTime, forks.MantleEuboea, forks.MantleSkadi); err != nil {
		return err
	}
	if err := checkMantleFork(cfg.MantleSkadiTime, cfg.MantleLimbTime, forks.MantleSkadi, forks.MantleLimb); err != nil {
		return err
	}
	if err := checkMantleFork(cfg.MantleLimbTime, cfg.MantleArsiaTime, forks.MantleLimb, forks.MantleArsia); err != nil {
		return err
	}
	if err := checkMantleFork(cfg.MantleArsiaTime, cfg.MantleElysiumTime, forks.MantleArsia, forks.MantleElysium); err != nil {
		return err
	}
	return nil
}

// checkFork checks that fork A is before or at the same time as fork B
func checkMantleFork(a, b *uint64, aName, bName MantleForkName) error {
	if a == nil && b == nil {
		return nil
	}
	if a == nil && b != nil {
		return fmt.Errorf("mantle fork %s set (to %d), but prior fork %s missing", bName, *b, aName)
	}
	if a != nil && b == nil {
		return nil
	}
	if *a > *b {
		return fmt.Errorf("mantle fork %s set to %d, but prior fork %s has higher offset %d", bName, *b, aName, *a)
	}
	return nil
}
