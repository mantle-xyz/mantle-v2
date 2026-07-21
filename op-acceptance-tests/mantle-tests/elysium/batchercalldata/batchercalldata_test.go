package batchercalldata

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

// TestBatcher_CalldataCost_After7976 asserts that the Mantle L2 batcher, pinned to the
// CALLDATA data-availability path, still successfully lands its batch-inbox submissions on
// a Glamsterdam (Amsterdam EL) L1 after EIP-7976 raises the L1 calldata floor cost.
//
// WHY THIS MATTERS. EIP-7976 (Amsterdam) raises the transaction calldata FLOOR: the
// per-token floor cost goes from 10 to 16 (params.TxCostFloorPerToken ->
// params.TxCostFloorPerToken7976) and every byte is charged as a non-zero byte, so a
// calldata tx's minimum gas jumps from `21000 + 40*nonZero + 10*zero` (pre-Amsterdam) to
// `21000 + 64*len` (Amsterdam). A batcher posting large CALLDATA batches therefore pays
// strictly more per submission on a Glamsterdam L1. The batcher must adapt its fee/gas
// estimation and keep landing batches; if it underprices under the raised floor, its
// inbox tx would be rejected/never mined and the L2 would stop advancing to safe.
//
// SETUP. The L1 EL is an external Glamsterdam geth subprocess (helpers.go / init_test.go),
// Amsterdam activates a few blocks after L1 genesis, and the batcher is pinned to
// DataAvailabilityType=calldata. So the batcher submits ordinary calldata txs to the
// batch-inbox on post-Amsterdam L1 blocks, priced under the EIP-7976 floor.
//
// FLOW / DISCRIMINATION.
//  1. Drive the L1 across the Amsterdam boundary (WaitForTime(AmsterdamTime)) and wait for
//     the L2 sequencer's L1 origin to itself cross Amsterdam.
//  2. Submit an L2 value transfer, then wait for its EXACT block (number AND hash) to reach
//     the SAFE head via ReachedRef. Reaching safe-by-hash proves the batcher successfully
//     posted the CALLDATA batch carrying that block to a post-Amsterdam L1 and op-node
//     re-derived the byte-identical block from it: end-to-end liveness of the calldata DA
//     path across the raised floor.
//  3. Locate the batcher's batch-inbox tx on a post-Amsterdam L1 block and assert it is a
//     normal CALLDATA tx (not an EIP-4844 blob tx, carries no blob hashes, has calldata).
//  4. Fetch that L1 tx's receipt and assert receipt.Status == Successful: the raised 7976
//     floor did not make the submission fail or get mined as a revert.
//  5. Ask the L1 to estimate that exact submission (same sender, inbox, calldata) and require the
//     batcher's committed gas limit to COVER the L1's own estimate. Under-committing is the
//     direction that breaks submission: the L1 rejects with ErrFloorDataGas and the batch stalls
//     until a resubmission re-estimates. Letting the L1 do the estimating keeps this correct
//     across future repricings without hardcoding an EIP-7623/7976 constant here.
//  6. Log the mined receipt.GasUsed next to the floor recomputed under Amsterdam (EIP-7976) and
//     pre-Amsterdam (EIP-7623) rules for visibility. We deliberately do NOT assert GasUsed against
//     those floors: they are computed with the pinned Mantle op-geth's FloorDataGas, while the L1
//     is a SEPARATE vanilla-geth build whose exact byte tokenization / floor charging can differ.
//
// WHAT IS NOT ASSERTED, AND WHY. The batcher deliberately biases its gas limit HIGH: it does not
// use the L1's estimate at all but sets the limit to a locally computed data floor under the
// newest rules (op-batcher/batcher/driver.go). That over-reservation costs nothing — a sender pays
// gasUsed, not the limit — and it is what keeps a batch crafted just before a fork boundary from
// being rejected by the raised floor after it. How far above the true cost that lands is a
// function of batch size (a near-empty devnet channel over-reserves a large fraction, a
// production-sized batch a negligible one), so it is not a stable signal and this test does not
// bound it.
//
// DISCRIMINATION is therefore LIVENESS + DA-path + gas-coverage. This flips red if: the batcher
// can't get its calldata batch onto a Glamsterdam L1 so the L2 tx never reaches safe (ReachedRef
// by hash); the inbox tx is a blob tx (wrong DA path); the inbox tx's L1 receipt reverted/failed;
// or the batcher commits less gas than the Glamsterdam L1 itself requires for that submission.
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
