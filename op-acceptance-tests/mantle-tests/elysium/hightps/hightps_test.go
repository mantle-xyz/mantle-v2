package hightps

import (
	"fmt"
	"math/big"
	"sync"
	"testing"
	"time"

	opforks "github.com/ethereum-optimism/optimism/op-core/forks"
	"github.com/ethereum-optimism/optimism/op-devstack/devtest"
	"github.com/ethereum-optimism/optimism/op-devstack/presets"
	"github.com/ethereum-optimism/optimism/op-service/eth"
	"github.com/ethereum-optimism/optimism/op-service/txplan"
	suptypes "github.com/ethereum-optimism/optimism/op-supervisor/supervisor/types"
	"github.com/ethereum/go-ethereum/common"
	gethtypes "github.com/ethereum/go-ethereum/core/types"
)

// TestSystem_HighTPS_AcrossUpgrade drives a burst of concurrent L2 transactions after the L1 has
// upgraded to Glamsterdam and asserts the L2 stays healthy under load: every tx is mined, the
// busiest block reaches the safe head matched by hash (op-node kept deriving under load, no stall
// or divergence), and the loaded blocks stay Arsia.
//
// The load itself is fork-independent, so this is deliberately a robustness/soak check on top of
// the Glamsterdam L1 rather than a discriminating fork assertion: it flips red if the L2 drops or
// reverts txs under load, fails to consolidate the loaded blocks to safe, or leaks an Amsterdam
// header field while busy.
func TestSystem_HighTPS_AcrossUpgrade(gt *testing.T) {
	t := devtest.SerialT(gt)
	sys := presets.NewMantleMinimal(t)
	require := t.Require()
	ctx := t.Ctx()

	l1Config := sys.L1Network.Escape().ChainConfig()
	require.True(sys.L2Chain.IsMantleForkActive(opforks.MantleElysium), "L2 must run with Mantle Elysium active")
	require.NotNil(l1Config.AmsterdamTime, "L1 AmsterdamTime must be configured")

	// Cross Amsterdam and wait for the L2 origin to cross it too.
	sys.L1EL.WaitForTime(*l1Config.AmsterdamTime)
	require.Eventually(func() bool {
		o := sys.L2EL.BlockRefByLabel(eth.Unsafe).L1Origin.Number
		r := sys.L1EL.BlockRefByNumber(o)
		return l1Config.IsAmsterdam(new(big.Int).SetUint64(o), r.Time)
	}, 120*time.Second, time.Second, "L2 unsafe origin must cross Amsterdam before the load")

	const wallets = 8
	const perWallet = 15
	recipient := common.HexToAddress("0x00000000000000000000000000000000ABCDEF01")

	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		funderMu sync.Mutex
		maxBlock uint64
		maxHash  common.Hash
		errCh    = make(chan error, wallets)
	)
	for i := 0; i < wallets; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// NewFundedEOA submits a funding tx off the shared funder nonce, so serialize creation.
			funderMu.Lock()
			eoa := sys.FunderL2.NewFundedEOA(eth.OneEther)
			funderMu.Unlock()

			for j := 0; j < perWallet; j++ {
				rcpt, err := txplan.NewPlannedTx(txplan.Combine(
					eoa.Plan(),
					txplan.WithTo(&recipient),
					txplan.WithGasLimit(100_000),
				)).Included.Eval(ctx)
				if err != nil {
					errCh <- err
					return
				}
				if rcpt.Status != gethtypes.ReceiptStatusSuccessful {
					errCh <- fmt.Errorf("load tx not successful in block %d", rcpt.BlockNumber.Uint64())
					return
				}
				mu.Lock()
				if n := rcpt.BlockNumber.Uint64(); n > maxBlock {
					maxBlock = n
					maxHash = rcpt.BlockHash
				}
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	close(errCh)
	for e := range errCh {
		require.NoError(e, "all high-TPS txs must be mined successfully")
	}
	require.Greater(maxBlock, uint64(0), "the load must have produced L2 blocks")
	t.Log("submitted high-TPS load", "txs", wallets*perWallet, "topBlock", maxBlock)

	// The busiest block must reach the safe head matched by hash — op-node re-derived the loaded
	// blocks byte-identically from L1, so derivation kept up under load without stalling/diverging.
	sys.L2CL.ReachedRef(suptypes.CrossSafe, eth.BlockID{Number: maxBlock, Hash: maxHash}, 180)

	// The loaded blocks must stay Arsia.
	safe := sys.L2CL.SyncStatus().SafeL2.Number
	for off := uint64(0); off < 3; off++ {
		if maxBlock < off {
			break
		}
		num := maxBlock - off
		if num == 0 || num > safe {
			continue
		}
		info, _, err := sys.L2EL.Escape().EthClient().InfoAndTxsByHash(ctx, sys.L2EL.BlockRefByNumber(num).Hash)
		require.NoErrorf(err, "read loaded L2 block %d", num)
		require.Nilf(info.Header().BlockAccessListHash, "loaded L2 block %d must stay Arsia (no BAL)", num)
		require.Nilf(info.Header().SlotNumber, "loaded L2 block %d must stay Arsia (no SlotNumber)", num)
	}
}
