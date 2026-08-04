package arsia

import (
	"context"
	"crypto/rand"
	"errors"
	"math/big"
	"sync"
	"testing"
	"time"

	"github.com/ethereum-optimism/optimism/op-acceptance-tests/tests/interop/loadtest"
	"github.com/ethereum-optimism/optimism/op-core/predeploys"
	"github.com/ethereum-optimism/optimism/op-devstack/devtest"
	"github.com/ethereum-optimism/optimism/op-devstack/presets"
	"github.com/ethereum-optimism/optimism/op-node/rollup/derive"
	"github.com/ethereum-optimism/optimism/op-service/eth"
	"github.com/ethereum-optimism/optimism/op-service/txinclude"
	"github.com/ethereum-optimism/optimism/op-service/txintent/bindings"
	"github.com/ethereum-optimism/optimism/op-service/txintent/contractio"
	"github.com/ethereum-optimism/optimism/op-service/txplan"
	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/params"
)

// TestBlobBaseFeePlumbing checks that the L1 blob base fee is correctly surfaced into L2 system
// data: the L1-info system-deposit tx (derive.L1BlockInfoFromBytes) and the L1Block predeploy
// (BlobBaseFee()). It is fork-agnostic L1->L2 plumbing and needs only a post-Osaka L1 with a blob
// schedule, which the Arsia baseline (BPO2-at-genesis) provides.
//
// It was relocated here from the fusaka suite: fusaka runs in the non-blocking mantle-orphan soak,
// yet this was the ONLY CI assertion of the blob-base-fee value reaching L2 (arsia/check_scripts
// only reads BlobBaseFeeScalar; elysium/base/headernopollut round-trips the regular base fee, not
// the blob one). Moving it into arsia makes that unique coverage a blocking per-PR check. The
// BPO-crossing variant (TestBlobBaseFeeIsCorrectAfterBPOFork) stays in fusaka, since only fusaka's
// L1 crosses a BPO blob-parameter fork mid-run.
func TestBlobBaseFeePlumbing(gt *testing.T) {
	t := devtest.SerialT(gt)
	sys := presets.NewMantleMinimal(t)

	spamBlobs(t, sys)

	t.Log("Waiting for a block with blob base fee")
	l2UnsafeHash, l1BlobBaseFee := waitForL1BlobBaseFee(t, sys)
	t.Log("Found block with blob base fee")

	l2Info, l2Txs, err := sys.L2EL.Escape().EthClient().InfoAndTxsByHash(t.Ctx(), l2UnsafeHash)
	t.Require().NoError(err)

	t.Log("Checking the L1 blob base fee in the system deposit tx")
	blockInfo, err := derive.L1BlockInfoFromBytes(sys.L2Chain.Escape().RollupConfig(), l2Info.Time(), l2Txs[0].Data())
	t.Require().NoError(err)
	l2BlobBaseFee := blockInfo.BlobBaseFee
	t.Require().Equal(l1BlobBaseFee, l2BlobBaseFee)

	t.Log("Checking the L1 blob base fee in the L1Block contract")
	l1Block := bindings.NewL1Block(bindings.WithClient(sys.L2EL.Escape().EthClient()), bindings.WithTo(predeploys.L1BlockAddr))
	l2BlobBaseFee, err = contractio.Read(l1Block.BlobBaseFee(), t.Ctx(), func(tx *txplan.PlannedTx) {
		tx.AgainstBlock.Set(l2Info)
	})
	t.Require().NoError(err)
	t.Require().Equal(l1BlobBaseFee, l2BlobBaseFee)
}

func waitForL1BlobBaseFee(t devtest.T, sys *presets.MantleMinimal) (common.Hash, *big.Int) {
	l1ChainConfig := sys.L1Network.Escape().ChainConfig()
	l1BlockTime := estimateL1BlockTimeSafe(t, sys)

	for {
		l2UnsafeRef := sys.L2CL.SyncStatus().UnsafeL2
		l1Info, _, err := sys.L1EL.EthClient().InfoAndTxsByHash(t.Ctx(), l2UnsafeRef.L1Origin.Hash)
		if errors.Is(err, ethereum.NotFound) {
			continue
		}
		t.Require().NoError(err)

		l1BlobBaseFee := l1Info.BlobBaseFee(l1ChainConfig)
		if l1BlobBaseFee != nil {
			return l2UnsafeRef.Hash, l1BlobBaseFee
		}

		select {
		case <-t.Ctx().Done():
			t.Require().Fail("context canceled before finding a block with blob base fee")
		case <-time.After(l1BlockTime):
		}
	}
}

func spamBlobs(t devtest.T, sys *presets.MantleMinimal) {
	l1BlockTime := estimateL1BlockTimeSafe(t, sys)
	l1ChainConfig := sys.L1Network.Escape().ChainConfig()

	eoa := sys.FunderL1.NewFundedEOA(eth.OneTenthEther)
	signer := txinclude.NewPkSigner(eoa.Key().Priv(), sys.L1Network.ChainID().ToBig())
	l1ETHClient := sys.L1EL.EthClient()
	syncEOA := loadtest.NewSyncEOA(txinclude.NewPersistent(signer, struct {
		*txinclude.Monitor
		*txinclude.Resubmitter
	}{
		txinclude.NewMonitor(l1ETHClient, l1BlockTime),
		txinclude.NewResubmitter(l1ETHClient, l1BlockTime),
	}), eoa.Plan())

	var blob eth.Blob
	_, err := rand.Read(blob[:])
	t.Require().NoError(err)
	// get the field-elements into a valid range
	for i := range 4096 {
		blob[32*i] &= 0b0011_1111
	}

	const maxBlobTxsPerAccountInMempool = 16 // Private policy param in geth.
	spammer := loadtest.SpammerFunc(func(t devtest.T) error {
		_, err := syncEOA.Include(t, txplan.WithBlobs([]*eth.Blob{&blob}, l1ChainConfig), txplan.WithTo(&common.Address{}))
		return err
	})
	txsPerSlot := calcBlobTxsPerSlot(t, l1ChainConfig, maxBlobTxsPerAccountInMempool)
	schedule := loadtest.NewConstant(l1BlockTime, loadtest.WithBaseRPS(uint64(txsPerSlot)))

	ctx, cancel := context.WithCancel(t.Ctx())
	var wg sync.WaitGroup
	t.Cleanup(func() {
		cancel()
		wg.Wait()
	})
	wg.Add(1)
	go func() {
		defer wg.Done()
		schedule.Run(t.WithCtx(ctx), spammer)
	}()
}

func calcBlobTxsPerSlot(t devtest.T, l1ChainConfig *params.ChainConfig, maxBlobTxsPerAccountInMempool int) int {
	if l1ChainConfig.BlobScheduleConfig == nil || l1ChainConfig.BlobScheduleConfig.BPO1 == nil {
		t.Log("BPO1 blob schedule not configured; using minimal blob tx rate")
		return 1
	}
	return min(l1ChainConfig.BlobScheduleConfig.BPO1.Max*3/4, maxBlobTxsPerAccountInMempool)
}

func estimateL1BlockTimeSafe(t devtest.T, sys *presets.MantleMinimal) time.Duration {
	client := sys.L1EL.EthClient()
	for {
		latest, err := client.BlockRefByLabel(t.Ctx(), eth.Unsafe)
		t.Require().NoError(err)
		if latest.Number > 1 {
			lowerNum := latest.Number - 1
			if latest.Number > 1000 {
				lowerNum = latest.Number - 1000
			}
			if lowerNum == 0 {
				lowerNum = 1
			}
			lowerBlock, err := client.BlockRefByNumber(t.Ctx(), lowerNum)
			t.Require().NoError(err)
			deltaTime := latest.Time - lowerBlock.Time
			deltaNum := latest.Number - lowerBlock.Number
			if deltaNum == 0 {
				return 2 * time.Second
			}
			return time.Duration(deltaTime) * time.Second / time.Duration(deltaNum)
		}
		select {
		case <-time.After(time.Millisecond * 500):
		case <-t.Ctx().Done():
			t.Require().Fail("context was canceled before L1 block time could be estimated")
		}
	}
}
