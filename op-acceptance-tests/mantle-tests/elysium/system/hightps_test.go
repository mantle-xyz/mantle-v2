package system

import (
	"fmt"
	"math/big"
	"os"
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

// TestL1Glamsterdam_HighTPS is a derivation-stability-under-load test: it drives ~1000
// concurrent L2 transactions (≈50 blocks × 20 tx) while the L1 runs Glamsterdam and asserts
// the loaded chain stays CONSISTENT, not just alive. Every tx must be mined successfully, and
// the busiest block must reach the cross-safe head MATCHED BY HASH (ReachedRef): op-node
// pulled the full batches back out of the Glamsterdam L1 and re-derived the loaded blocks
// byte-identically. Unlike the 1-2-tx smoke/midflight cases, the blocks here are produced
// under sustained load, so their batches exercise the fat end of the batcher -> Glamsterdam
// L1 -> derivation path (blob-sized payloads, post-fork L1 inclusion) where a size- or
// pricing-dependent regression would surface.
//
// Honest scope: per-tx success is L1-fork-independent; the Glamsterdam-specific property is
// the byte-identical RE-DERIVATION of loaded blocks from a post-Amsterdam L1.
//
// It runs the full ~1000-tx load and is therefore kept out of the light path: it skips unless
// ELYSIUM_HEAVY is set, and is meant to be run on demand / scheduled.
func runL1GlamsterdamHighTPS(gt *testing.T) {
	if os.Getenv("ELYSIUM_HEAVY") == "" {
		gt.Skip("heavy load test; run with ELYSIUM_HEAVY=1")
	}
	t := devtest.SerialT(gt)
	sys := presets.NewMantleMinimal(t)
	require := t.Require()
	ctx := t.Ctx()

	l1Config := sys.L1Network.Escape().ChainConfig()
	require.True(sys.L2Chain.IsMantleForkActive(opforks.MantleElysium), "L2 must run with Mantle Elysium active")
	require.NotNil(l1Config.AmsterdamTime, "L1 AmsterdamTime must be configured")

	// Cross Amsterdam and wait for the L2 origin to cross it too, so the loaded blocks are
	// batched to and re-derived from a genuinely post-Amsterdam L1.
	sys.L1EL.WaitForTime(*l1Config.AmsterdamTime)
	require.Eventually(func() bool {
		o := sys.L2EL.BlockRefByLabel(eth.Unsafe).L1Origin.Number
		r := sys.L1EL.BlockRefByNumber(o)
		return l1Config.IsAmsterdam(new(big.Int).SetUint64(o), r.Time)
	}, 120*time.Second, time.Second, "L2 unsafe origin must cross Amsterdam before the load")

	// Full load: ~1000 txs (≈50 blocks × 20 tx) spread across concurrent senders.
	const wallets = 20
	const perWallet = 50
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
	t.Log("submitted high-TPS load", "txs", wallets*perWallet, "topBlock", maxBlock)

	// The busiest block must reach the safe head matched by hash — op-node re-derived the loaded
	// blocks byte-identically from the Glamsterdam L1, so derivation kept up under load without
	// stalling or diverging.
	sys.L2CL.ReachedRef(suptypes.CrossSafe, eth.BlockID{Number: maxBlock, Hash: maxHash}, 180)
}
