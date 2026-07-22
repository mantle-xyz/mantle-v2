package system

import (
	"math/big"
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

// TestL1Glamsterdam_BatcherSubmissionE2E asserts the batcher still lands batches
// on a Glamsterdam L1 while the Mantle L2 stays on Arsia.
//
// The real-CL devnet chooses the DA mode, so the test validates the path that was
// actually used: blob inbox txs must carry blob hashes, non-blob inbox txs must
// carry calldata, and the L2 tx block must reach safe by exact hash.
func runBatcherSubmission(gt *testing.T) {
	t := devtest.SerialT(gt)
	sys := presets.NewMantleMinimal(t)
	require := t.Require()
	ctx := t.Ctx()

	rollupCfg := sys.L2Chain.Escape().RollupConfig()
	l1Config := sys.L1Network.Escape().ChainConfig()
	require.True(sys.L2Chain.IsMantleForkActive(opforks.MantleElysium), "L2 must run with Mantle Elysium active")
	require.NotNil(l1Config.AmsterdamTime, "L1 AmsterdamTime must be configured")

	// 1) Cross Amsterdam and wait until the L2 origin is post-Amsterdam too.
	sys.L1EL.WaitForTime(*l1Config.AmsterdamTime)
	t.Log("L1 Amsterdam activated")
	require.Eventually(func() bool {
		originNum := sys.L2EL.BlockRefByLabel(eth.Unsafe).L1Origin.Number
		originRef := sys.L1EL.BlockRefByNumber(originNum)
		return l1Config.IsAmsterdam(new(big.Int).SetUint64(originNum), originRef.Time)
	}, 120*time.Second, time.Second, "L2 unsafe origin must cross Amsterdam before the tx is submitted")

	// 2) Submit an L2 tx after the upgrade and require its exact block to reach the safe head.
	wallet := sys.FunderL2.NewFundedEOA(eth.OneEther)
	recipient := common.HexToAddress("0x00000000000000000000000000000000B10B5AFE")
	receipt, err := txplan.NewPlannedTx(txplan.Combine(
		wallet.Plan(),
		txplan.WithTo(&recipient),
		txplan.WithValue(eth.HalfEther),
		txplan.WithGasLimit(1_000_000),
	)).Included.Eval(ctx)
	require.NoError(err)
	require.Equal(gethtypes.ReceiptStatusSuccessful, receipt.Status, "L2 value transfer must be included")
	l2TxBlock := receipt.BlockNumber.Uint64()
	t.Log("submitted L2 tx", "block", l2TxBlock, "hash", receipt.TxHash)

	sys.L2CL.ReachedRef(suptypes.CrossSafe, eth.BlockID{Number: l2TxBlock, Hash: receipt.BlockHash}, 60)
	t.Log("L2 tx block reached safe head - batcher landed its batch on the Glamsterdam L1")

	// 3) Locate a batch-inbox tx on a post-Amsterdam L1 block.
	batchInbox := rollupCfg.BatchInboxAddress
	l1Eth := sys.L1EL.EthClient()
	var (
		found        bool
		batchTx      *gethtypes.Transaction
		batchL1Block uint64
	)
	deadline := time.Now().Add(90 * time.Second)
	for !found && time.Now().Before(deadline) {
		head := sys.L1EL.BlockRefByLabel(eth.Unsafe)
		floor := uint64(1)
		if head.Number > 64 {
			floor = head.Number - 64
		}
		for n := head.Number; n >= floor && !found; n-- {
			info, txs, err := l1Eth.InfoAndTxsByNumber(ctx, n)
			require.NoError(err, "read L1 block %d", n)
			// Only trust batch-inbox txs seen on a genuinely post-Amsterdam L1 block.
			if !l1Config.IsAmsterdam(new(big.Int).SetUint64(info.NumberU64()), info.Time()) {
				continue
			}
			for _, tx := range txs {
				if tx.To() == nil || *tx.To() != batchInbox {
					continue
				}
				// Assert the invariants of whichever DA path the devnet's batcher uses: a type-3
				// EIP-4844 tx must carry blob versioned hashes, anything else must carry calldata.
				// An inbox tx with neither carries no batch at all.
				if tx.Type() == gethtypes.BlobTxType {
					require.NotEmptyf(tx.BlobHashes(),
						"batcher blob tx on L1 #%d must carry blob versioned hashes", info.NumberU64())
				} else {
					require.NotEmptyf(tx.Data(),
						"batcher calldata tx (type %d) on L1 #%d must carry batch calldata",
						tx.Type(), info.NumberU64())
				}
				batchTx = tx
				batchL1Block = info.NumberU64()
				found = true
				break
			}
		}
		if !found {
			time.Sleep(time.Second)
		}
	}
	require.True(found, "expected a batcher tx to the batch-inbox on a post-Amsterdam L1 block")
	daPath := "calldata"
	if batchTx.Type() == gethtypes.BlobTxType {
		daPath = "blob"
	}
	t.Log("found post-Amsterdam batcher tx to batch-inbox",
		"l1Block", batchL1Block, "batchInbox", batchInbox, "txHash", batchTx.Hash(),
		"daPath", daPath, "txType", batchTx.Type(), "blobs", len(batchTx.BlobHashes()), "calldata", len(batchTx.Data()))

	// 4) The submission must have SUCCEEDED on-chain.
	batchReceipt, err := l1Eth.TransactionReceipt(ctx, batchTx.Hash())
	require.NoError(err, "batcher inbox tx must have an L1 receipt")
	require.Equalf(gethtypes.ReceiptStatusSuccessful, batchReceipt.Status,
		"batcher %s inbox tx on L1 #%d must be mined successfully on the Glamsterdam L1", daPath, batchL1Block)
	t.Log("batcher batch mined on the Glamsterdam L1", "l1Block", batchL1Block, "daPath", daPath)
}
