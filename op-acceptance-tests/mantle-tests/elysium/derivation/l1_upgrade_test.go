package derivation

import (
	"errors"
	"math/big"
	"testing"
	"time"

	opforks "github.com/ethereum-optimism/optimism/op-core/forks"
	"github.com/ethereum-optimism/optimism/op-devstack/devtest"
	"github.com/ethereum-optimism/optimism/op-devstack/presets"
	"github.com/ethereum/go-ethereum"
)

func TestDerivation_AcrossL1Upgrade(gt *testing.T) {
	t := devtest.SerialT(gt)
	sys := presets.NewMantleMinimal(t)
	rollupCfg := sys.L2Chain.Escape().RollupConfig()
	l1Config := sys.L1Network.Escape().ChainConfig()

	t.Require().True(sys.L2Chain.IsMantleForkActive(opforks.MantleElysium), "L2 must run with Mantle Elysium active")
	t.Require().NotNil(rollupCfg.MantleElysiumTime, "MantleElysiumTime must be configured")
	t.Require().NotNil(l1Config.AmsterdamTime, "L1 AmsterdamTime must be configured")

	t.Log("Waiting for L1 Amsterdam to activate")
	sys.L1EL.WaitForTime(*l1Config.AmsterdamTime)
	t.Log("L1 Amsterdam activated")

	l2BlockTime := time.Duration(rollupCfg.BlockTime) * time.Second
	for {
		l2SafeRef := sys.L2CL.SyncStatus().SafeL2
		l1Info, _, err := sys.L1EL.EthClient().InfoAndTxsByHash(t.Ctx(), l2SafeRef.L1Origin.Hash)
		if errors.Is(err, ethereum.NotFound) {
			t.Log("L2 safe head references an L1 origin not found by L1 EL yet, waiting...", "origin", l2SafeRef.L1Origin)
			select {
			case <-time.After(l2BlockTime):
				continue
			case <-t.Ctx().Done():
				t.Require().Fail("Never found L2 safe head L1 origin on L1 EL")
			}
		}
		t.Require().NoError(err)

		if l1Config.IsAmsterdam(new(big.Int).SetUint64(l1Info.NumberU64()), l1Info.Time()) {
			header := l1Info.Header()
			t.Require().NotNil(header.BlockAccessListHash, "post-Amsterdam L1 origin must carry BlockAccessListHash")
			t.Require().NotNil(header.SlotNumber, "post-Amsterdam L1 origin must carry SlotNumber")
			return
		}

		t.Log("L2 safe head still references pre-Amsterdam L1 origin, waiting for derivation to advance...")
		select {
		case <-time.After(l2BlockTime):
		case <-t.Ctx().Done():
			t.Require().Fail("Never found a safe L2 block with post-Amsterdam L1 origin")
		}
	}
}
