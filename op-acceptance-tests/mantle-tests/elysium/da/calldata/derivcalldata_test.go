package calldata

import (
	"math/big"
	"testing"
	"time"

	"github.com/ethereum-optimism/optimism/op-acceptance-tests/mantle-tests/elysium/internal/testhelpers"
	opforks "github.com/ethereum-optimism/optimism/op-core/forks"
	"github.com/ethereum-optimism/optimism/op-devstack/devtest"
	"github.com/ethereum-optimism/optimism/op-devstack/presets"
	"github.com/ethereum-optimism/optimism/op-service/eth"
	"github.com/ethereum-optimism/optimism/op-service/txplan"
	suptypes "github.com/ethereum-optimism/optimism/op-supervisor/supervisor/types"
	"github.com/ethereum/go-ethereum/common"
	gethtypes "github.com/ethereum/go-ethereum/core/types"
)

// TestDerivation_CalldataPathIntact verifies the calldata DA path while the L1 runs
// Glamsterdam. The batcher is pinned to calldata, a post-upgrade L2 transfer must become safe by
// number and hash, and the matching L1 inbox tx must be a non-blob calldata submission.
//
// This is the focused derivation counterpart to batchercalldata, which additionally checks L1
// receipt success and gas coverage under the EIP-7976 calldata floor.
func TestDerivation_CalldataPathIntact(gt *testing.T) {
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
	testhelpers.WaitForGlamsterdamL1(t, sys.L1EL, *l1Config.AmsterdamTime)
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

	// 3) Wait for the exact L2 tx block, by number and hash, to become safe.
	sys.L2CL.ReachedRef(suptypes.CrossSafe, eth.BlockID{Number: l2TxBlock, Hash: receipt.BlockHash}, 60)

	// 4) The safe L2 tx block must have a post-Amsterdam L1 origin.
	l2Ref := sys.L2EL.BlockRefByNumber(l2TxBlock)
	originInfo, _, err := sys.L1EL.EthClient().InfoAndTxsByHash(ctx, l2Ref.L1Origin.Hash)
	require.NoError(err, "L1 origin of the safe L2 tx block must exist on L1")
	require.Truef(
		l1Config.IsAmsterdam(new(big.Int).SetUint64(originInfo.NumberU64()), originInfo.Time()),
		"safe L2 tx block %d must have a post-Amsterdam L1 origin (got L1 #%d, t=%d)",
		l2TxBlock, originInfo.NumberU64(), originInfo.Time(),
	)
	t.Log("safe L2 tx block has post-Amsterdam L1 origin",
		"l2Block", l2TxBlock, "l1Origin", originInfo.NumberU64())

	// 5) Confirm the DA path is calldata, not blob DA.
	batchInbox := rollupCfg.BatchInboxAddress
	l1Eth := sys.L1EL.EthClient()
	var (
		found        bool
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
				// A non-blob tx with calldata distinguishes calldata DA from blob DA.
				require.NotEqualf(uint8(gethtypes.BlobTxType), tx.Type(),
					"batcher tx to inbox on L1 #%d must be CALLDATA, not an EIP-4844 blob tx", info.NumberU64())
				require.NotEmptyf(tx.Data(),
					"batcher calldata tx on L1 #%d must carry the batch data in calldata", info.NumberU64())
				found = true
				batchL1Block = info.NumberU64()
				break
			}
		}
		if !found {
			time.Sleep(time.Second)
		}
	}
	require.True(found,
		"expected a batcher CALLDATA tx to the batch-inbox on a post-Amsterdam L1 block")
	t.Log("found post-Amsterdam batcher calldata tx to batch-inbox",
		"l1Block", batchL1Block, "batchInbox", batchInbox)
}
