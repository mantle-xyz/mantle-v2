package blobsubmit

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

// TestBatcher_BlobSubmission asserts the Mantle L2 batcher, pinned to the BLOB data-availability
// path, still lands its EIP-4844 blob batches on a Glamsterdam (Amsterdam EL) L1. The Mantle L2
// stays on Arsia while the L1 runs Glamsterdam; blobs are the current production DA path, so this
// is the blob counterpart of the calldata-submission test.
//
// FLOW / DISCRIMINATION.
//  1. Drive the L1 across the Amsterdam boundary and wait for the L2 sequencer's L1 origin to
//     itself cross Amsterdam, so the batch below is posted to a post-Amsterdam L1 block.
//  2. Submit an L2 value transfer and wait for its EXACT block (number AND hash) to reach the SAFE
//     head via ReachedRef — op-node re-derived the byte-identical block only by fetching the
//     batcher's blob from the Glamsterdam L1, so the blob submission provably landed.
//  3. Locate the batcher's batch-inbox tx on a post-Amsterdam L1 block and assert it is a genuine
//     EIP-4844 BLOB tx (type-3, carries blob versioned hashes) — the blob DA path, not calldata.
//  4. Fetch that L1 tx's receipt and assert receipt.Status == Successful.
//
// Flips red if: the batcher can't get its blob batch onto a Glamsterdam L1 so the L2 tx never
// reaches safe (ReachedRef by hash times out); the inbox tx is not a blob tx (wrong DA path); or
// its L1 receipt reverted/failed.
func TestBatcher_BlobSubmission(gt *testing.T) {
	t := devtest.SerialT(gt)
	sys := presets.NewMantleMinimal(t)
	require := t.Require()
	ctx := t.Ctx()

	rollupCfg := sys.L2Chain.Escape().RollupConfig()
	l1Config := sys.L1Network.Escape().ChainConfig()
	require.True(sys.L2Chain.IsMantleForkActive(opforks.MantleElysium), "L2 must run with Mantle Elysium active")
	require.NotNil(l1Config.AmsterdamTime, "L1 AmsterdamTime must be configured")

	// 1) Drive the L1 across the Glamsterdam (Amsterdam) boundary and wait for the L2 origin to
	//    cross it too, so the batch carrying our tx is posted to a post-Amsterdam L1 block.
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
	t.Log("L2 tx block reached safe head — batcher landed its blob batch on the Glamsterdam L1")

	// 3) Locate the batcher's batch-inbox BLOB tx on a post-Amsterdam L1 block. The batch that
	//    made our L2 tx safe is such a tx, so one is guaranteed to be present.
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
				// This must be the BLOB DA path: a type-3 EIP-4844 tx carrying blob hashes.
				require.Equalf(uint8(gethtypes.BlobTxType), tx.Type(),
					"batcher tx to inbox on L1 #%d must be an EIP-4844 blob tx (type-3)", info.NumberU64())
				require.NotEmptyf(tx.BlobHashes(),
					"batcher blob tx on L1 #%d must carry blob versioned hashes", info.NumberU64())
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
	require.True(found, "expected a batcher EIP-4844 blob tx to the batch-inbox on a post-Amsterdam L1 block")
	t.Log("found post-Amsterdam batcher blob tx to batch-inbox",
		"l1Block", batchL1Block, "batchInbox", batchInbox, "txHash", batchTx.Hash(), "blobs", len(batchTx.BlobHashes()))

	// 4) The blob submission must have SUCCEEDED on-chain.
	batchReceipt, err := l1Eth.TransactionReceipt(ctx, batchTx.Hash())
	require.NoError(err, "batcher blob inbox tx must have an L1 receipt")
	require.Equalf(gethtypes.ReceiptStatusSuccessful, batchReceipt.Status,
		"batcher BLOB inbox tx on L1 #%d must be mined successfully on the Glamsterdam L1", batchL1Block)
	t.Log("batcher blob batch mined on the Glamsterdam L1", "l1Block", batchL1Block, "blobs", len(batchTx.BlobHashes()))
}
