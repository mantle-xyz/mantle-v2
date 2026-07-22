package calldata

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
	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core"
	gethtypes "github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/params"
)

// TestBatcher_CalldataCost_After7976 verifies the calldata DA path across the Amsterdam
// calldata-floor increase. The batcher is pinned to calldata, the L1 crosses Glamsterdam, and a
// post-upgrade L2 block must become safe by hash, proving the batch landed and was re-derived.
//
// The test then checks the mined inbox tx is calldata, succeeded on L1, and had a gas limit at
// least as high as the Glamsterdam L1's own estimate for the same submission. It deliberately does
// not bound over-reservation: the batcher biases high by design, and senders pay gas used rather
// than the limit.
func TestBatcher_CalldataCost_After7976(gt *testing.T) {
	t := devtest.SerialT(gt)
	sys := presets.NewMantleMinimal(t)
	require := t.Require()
	ctx := t.Ctx()

	rollupCfg := sys.L2Chain.Escape().RollupConfig()
	l1Config := sys.L1Network.Escape().ChainConfig()

	require.True(sys.L2Chain.IsMantleForkActive(opforks.MantleElysium), "L2 must run with Mantle Elysium active")
	require.NotNil(l1Config.AmsterdamTime, "L1 AmsterdamTime must be configured")

	// 1) Drive the L1 across the Amsterdam boundary.
	t.Log("Waiting for L1 Amsterdam to activate")
	sys.L1EL.WaitForTime(*l1Config.AmsterdamTime)
	t.Log("L1 Amsterdam activated")

	// Wait until L2's lagging L1 origin has also crossed Amsterdam.
	require.Eventually(func() bool {
		originNum := sys.L2EL.BlockRefByLabel(eth.Unsafe).L1Origin.Number
		originRef := sys.L1EL.BlockRefByNumber(originNum)
		return l1Config.IsAmsterdam(new(big.Int).SetUint64(originNum), originRef.Time)
	}, 120*time.Second, time.Second, "L2 unsafe origin must cross Amsterdam before the tx is submitted")

	// 2) Submit a post-upgrade plain L2 transfer.
	wallet := sys.FunderL2.NewFundedEOA(eth.OneEther)
	recipient := common.HexToAddress("0x00000000000000000000000000000000CA11DA7A")
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

	// Wait for the exact L2 tx block, by number and hash, to become safe.
	sys.L2CL.ReachedRef(suptypes.CrossSafe, eth.BlockID{Number: l2TxBlock, Hash: receipt.BlockHash}, 60)
	t.Log("L2 tx block reached safe head — batcher landed its calldata batch on the Glamsterdam L1")

	// 3) Locate the batcher's calldata inbox tx on a post-Amsterdam L1 block.
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
				// A normal calldata tx distinguishes calldata DA from blob DA.
				require.NotEqualf(uint8(gethtypes.BlobTxType), tx.Type(),
					"batcher tx to inbox on L1 #%d must be CALLDATA, not an EIP-4844 blob tx", info.NumberU64())
				require.Emptyf(tx.BlobHashes(),
					"batcher calldata tx on L1 #%d must carry no blob versioned hashes", info.NumberU64())
				require.NotEmptyf(tx.Data(),
					"batcher calldata tx on L1 #%d must carry the batch data in calldata", info.NumberU64())
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
	require.True(found, "expected a batcher CALLDATA tx to the batch-inbox on a post-Amsterdam L1 block")
	t.Log("found post-Amsterdam batcher calldata tx to batch-inbox",
		"l1Block", batchL1Block, "batchInbox", batchInbox, "txHash", batchTx.Hash(), "dataLen", len(batchTx.Data()))

	// 4) The inbox tx must have succeeded on L1.
	batchReceipt, err := l1Eth.TransactionReceipt(ctx, batchTx.Hash())
	require.NoError(err, "batcher inbox tx must have an L1 receipt")
	require.Equalf(gethtypes.ReceiptStatusSuccessful, batchReceipt.Status,
		"batcher CALLDATA inbox tx on L1 #%d must be mined successfully under the raised EIP-7976 floor", batchL1Block)

	// 5) The batcher's gas limit must cover the live L1 estimate for the same call.
	sender, err := gethtypes.Sender(gethtypes.LatestSignerForChainID(batchTx.ChainId()), batchTx)
	require.NoError(err, "recover the batcher tx sender")
	l1Estimate, err := l1Eth.EstimateGas(ctx, ethereum.CallMsg{
		From:  sender,
		To:    batchTx.To(),
		Data:  batchTx.Data(),
		Value: batchTx.Value(),
	})
	require.NoError(err, "the Glamsterdam L1 must estimate the batcher's own calldata submission")
	require.GreaterOrEqualf(batchTx.Gas(), l1Estimate,
		"batcher committed gas limit %d must cover the Glamsterdam L1's own estimate %d for the same %d-byte calldata submission; under-committing is rejected with ErrFloorDataGas and stalls the batch",
		batchTx.Gas(), l1Estimate, len(batchTx.Data()))
	t.Log("batcher gas commitment covers the L1's own estimate",
		"committedGasLimit", batchTx.Gas(), "l1Estimate", l1Estimate, "gasUsed", batchReceipt.GasUsed)

	// 6) Recompute local floor values for logging only; the live L1 estimate above is asserted.
	floorAmsterdam, err := core.FloorDataGas(params.Rules{IsAmsterdam: true}, batchTx.Data(), batchTx.AccessList())
	require.NoError(err, "compute Amsterdam (EIP-7976) floor for the batcher tx")
	floorPre, err := core.FloorDataGas(params.Rules{}, batchTx.Data(), batchTx.AccessList())
	require.NoError(err, "compute pre-Amsterdam (EIP-7623) floor for the batcher tx")

	// The local floor values are diagnostic; vanilla-geth L1 charging may differ.
	t.Log("batcher calldata batch mined on the Glamsterdam L1",
		"l1Block", batchL1Block,
		"dataLen", len(batchTx.Data()),
		"gasUsed", batchReceipt.GasUsed,
		"floorAmsterdam_7976", floorAmsterdam,
		"floorPre_7623", floorPre)
}
