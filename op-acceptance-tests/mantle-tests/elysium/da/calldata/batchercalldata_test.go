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

	// 1) Drive the L1 across the Glamsterdam (Amsterdam) boundary.
	t.Log("Waiting for L1 Amsterdam to activate")
	sys.L1EL.WaitForTime(*l1Config.AmsterdamTime)
	t.Log("L1 Amsterdam activated")

	// The L2 sequencer's L1 origin lags the L1 head by the confirmation depth, so right after
	// activation it still points at a pre-Amsterdam L1 block. Wait until the L2 origin has
	// itself crossed Amsterdam so the tx below lands in an L2 block whose batch is posted to a
	// post-Amsterdam L1 block (and thus priced under the raised EIP-7976 floor).
	require.Eventually(func() bool {
		originNum := sys.L2EL.BlockRefByLabel(eth.Unsafe).L1Origin.Number
		originRef := sys.L1EL.BlockRefByNumber(originNum)
		return l1Config.IsAmsterdam(new(big.Int).SetUint64(originNum), originRef.Time)
	}, 120*time.Second, time.Second, "L2 unsafe origin must cross Amsterdam before the tx is submitted")

	// 2) Submit an L2 tx AFTER the L1 upgrade so its batch is posted to a post-Amsterdam L1.
	//    A code-less EOA recipient keeps this a plain transfer.
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

	// Wait for the L2 tx's EXACT block — same number AND hash — to reach the SAFE head. Safe
	// by hash means op-node re-derived the byte-identical block from the batcher's L1 CALLDATA
	// submission: the batcher DID land its calldata batch on the Glamsterdam L1 despite the
	// raised floor. If it had underpriced and failed to submit, this would time out.
	sys.L2CL.ReachedRef(suptypes.CrossSafe, eth.BlockID{Number: l2TxBlock, Hash: receipt.BlockHash}, 60)
	t.Log("L2 tx block reached safe head — batcher landed its calldata batch on the Glamsterdam L1")

	// 3) Locate the batcher's batch-inbox CALLDATA tx on a post-Amsterdam L1 block. The batch
	//    that made our L2 tx safe is itself such a tx, so it is guaranteed to be present.
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
				// Discriminate calldata DA from blob DA: this must be a normal calldata tx.
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

	// 4) The submission must have SUCCEEDED on-chain: fetch its L1 receipt and require a
	//    successful status. A batcher that underpriced under the raised 7976 floor would
	//    leave no successful inbox tx here (the L2 tx above would also never have gone safe).
	batchReceipt, err := l1Eth.TransactionReceipt(ctx, batchTx.Hash())
	require.NoError(err, "batcher inbox tx must have an L1 receipt")
	require.Equalf(gethtypes.ReceiptStatusSuccessful, batchReceipt.Status,
		"batcher CALLDATA inbox tx on L1 #%d must be mined successfully under the raised EIP-7976 floor", batchL1Block)

	// 5) The batcher's gas commitment must COVER what the Glamsterdam L1 itself says the tx
	//    needs. We ask the L1 to estimate the very same call (same sender, inbox, calldata) and
	//    require the batcher's submitted gas limit to be at least that. The L1 computes with its
	//    own live fork rules, so this stays correct across repricings without hardcoding any
	//    EIP-7623/7976 constant on the test side.
	//
	//    This is the falsifiable half of "still lands": under-committing is the direction that
	//    breaks the batcher (the L1 rejects with ErrFloorDataGas and the batch stalls until a
	//    resubmission re-estimates). Over-committing is not asserted against — the batcher
	//    deliberately biases high (op-batcher/batcher/driver.go sets the gas limit to a locally
	//    computed floor under the newest rules), which is safe for a code-less batch inbox.
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

	// 6) GAS VISIBILITY. Recompute the calldata floor under Amsterdam (EIP-7976) and
	//    pre-Amsterdam (EIP-7623) rules purely for the log below. These use the pinned Mantle
	//    op-geth's FloorDataGas, which need NOT match the separate vanilla-geth L1's actual
	//    charging, so nothing is asserted against them (a floorAmsterdam>floorPre check would be
	//    a pure-formula tautology on batchTx.Data(), unrelated to what the L1 charged).
	floorAmsterdam, err := core.FloorDataGas(params.Rules{IsAmsterdam: true}, batchTx.Data(), batchTx.AccessList())
	require.NoError(err, "compute Amsterdam (EIP-7976) floor for the batcher tx")
	floorPre, err := core.FloorDataGas(params.Rules{}, batchTx.Data(), batchTx.AccessList())
	require.NoError(err, "compute pre-Amsterdam (EIP-7623) floor for the batcher tx")

	// Log the mined gas against the recomputed floors for visibility, but do NOT assert
	// gasUsed == floorAmsterdam: this test computes the floor with the pinned Mantle op-geth's
	// FloorDataGas, while the L1 is a SEPARATE vanilla-geth build, and their exact byte
	// tokenization / floor charging can differ. The robust, discriminating claim is asserted
	// above — the calldata batch was mined successfully on a Glamsterdam L1 AND op-node
	// re-derived the block from it, so the raised 7976 floor did not underprice the batcher.
	t.Log("batcher calldata batch mined on the Glamsterdam L1",
		"l1Block", batchL1Block,
		"dataLen", len(batchTx.Data()),
		"gasUsed", batchReceipt.GasUsed,
		"floorAmsterdam_7976", floorAmsterdam,
		"floorPre_7623", floorPre)
}
