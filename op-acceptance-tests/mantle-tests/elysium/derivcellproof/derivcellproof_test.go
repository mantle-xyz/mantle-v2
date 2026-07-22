package derivcellproof

import (
	"context"
	"crypto/rand"
	"math/big"
	"testing"
	"time"

	opforks "github.com/ethereum-optimism/optimism/op-core/forks"
	"github.com/ethereum-optimism/optimism/op-devstack/devtest"
	"github.com/ethereum-optimism/optimism/op-devstack/presets"
	"github.com/ethereum-optimism/optimism/op-service/eth"
	"github.com/ethereum-optimism/optimism/op-service/txmgr"
	"github.com/ethereum-optimism/optimism/op-service/txplan"
	suptypes "github.com/ethereum-optimism/optimism/op-supervisor/supervisor/types"
	"github.com/ethereum/go-ethereum/common"
	gethtypes "github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto/kzg4844"
	"github.com/ethereum/go-ethereum/params"
)

// blobSink is an arbitrary destination for the probe blob txs this test submits itself.
var blobSink = common.HexToAddress("0x00000000000000000000000000000000CE110001")

// TestDerivation_BlobCellProofs verifies the blob DA path when txmgr builds EIP-7594 cell-proof
// sidecars. Unlike TestDerivation_BlobPathIntact, this package sets CellProofTime=0 so the batcher
// uses the Version1 sidecar path a Fusaka+ network expects.
//
// The L1 cannot reveal the sender's original sidecar version after mining, so the test checks only
// observable facts: locally built Version1 sidecars have 128 proofs per blob and are accepted by
// the Glamsterdam L1; legacy Version0 sidecars remain accepted; and the batcher's own blob batch
// still re-derives a specific L2 block to the safe head by number and hash.
func TestDerivation_BlobCellProofs(gt *testing.T) {
	t := devtest.SerialT(gt)
	sys := presets.NewMantleMinimal(t)
	require := t.Require()
	ctx := t.Ctx()

	rollupCfg := sys.L2Chain.Escape().RollupConfig()
	l1Config := sys.L1Network.Escape().ChainConfig()

	require.True(sys.L2Chain.IsMantleForkActive(opforks.MantleElysium), "L2 must run with Mantle Elysium active")
	require.NotNil(l1Config.AmsterdamTime, "L1 AmsterdamTime must be configured")
	require.NotNil(l1Config.OsakaTime, "L1 OsakaTime must be configured: EIP-7594 cell proofs only exist post-Osaka")

	// Drive the L1 all the way to Glamsterdam (Amsterdam EL) so this is genuinely
	// post-Glamsterdam derivation.
	t.Log("Waiting for L1 Amsterdam to activate")
	sys.L1EL.WaitForTime(*l1Config.AmsterdamTime)
	t.Log("L1 Amsterdam activated")

	l1Eth := sys.L1EL.EthClient()
	isAmsterdam := func(info eth.BlockInfo) bool {
		return l1Config.IsAmsterdam(new(big.Int).SetUint64(info.NumberU64()), info.Time())
	}

	// Phase 1: cell proofs must be in scope on this L1 at all — they are an Osaka (EIP-7594)
	// feature, and the batcher's CellProofTime=0 only makes sense on a post-Osaka chain.
	head := sys.L1EL.BlockRefByLabel(eth.Unsafe)
	require.True(l1Config.IsOsaka(new(big.Int).SetUint64(head.Number), head.Time),
		"L1 head must be post-Osaka for EIP-7594 cell proofs to apply")

	// Phase 2: the cell-proof sidecar the batcher builds must be a genuine EIP-7594 sidecar.
	// This is txmgr's own cell-proof branch, evaluated locally so its shape is pinned before we
	// ask the L1 to accept it.
	probeBlob := randomBlob(t)
	cellSidecar, cellHashes, err := txmgr.MakeSidecar([]*eth.Blob{probeBlob}, true)
	require.NoError(err, "build a cell-proof sidecar the way the batcher does under CellProofTime=0")
	require.Equal(gethtypes.BlobSidecarVersion1, cellSidecar.Version, "cell-proof sidecar must be Version1")
	require.Lenf(cellSidecar.Proofs, kzg4844.CellProofsPerBlob*len(cellSidecar.Blobs),
		"a Version1 sidecar must carry %d cell proofs per blob, not a single legacy blob proof", kzg4844.CellProofsPerBlob)
	t.Log("built EIP-7594 cell-proof sidecar", "version", cellSidecar.Version, "blobs", len(cellSidecar.Blobs), "proofs", len(cellSidecar.Proofs))

	// Phase 3: that exact cell-proof blob tx must be accepted and mined by the Glamsterdam L1.
	// The EL verifies all 128 cell proofs per blob before inclusion.
	eoa := sys.FunderL1.NewFundedEOA(eth.OneEther)
	cellRcpt, err := txplan.NewPlannedTx(txplan.Combine(
		eoa.Plan(),
		txplan.WithTo(&blobSink),
		txplan.WithGasLimit(200_000),
		withPreparedSidecar(probeBlob, cellSidecar, cellHashes, l1Config),
	)).Included.Eval(ctx)
	require.NoError(err, "a Version1 cell-proof blob tx must be accepted by the Glamsterdam L1")
	require.Equal(gethtypes.ReceiptStatusSuccessful, cellRcpt.Status, "cell-proof blob tx must be mined successfully")
	cellInfo, _, err := l1Eth.InfoAndTxsByHash(ctx, cellRcpt.BlockHash)
	require.NoError(err, "read the L1 block that included the cell-proof blob tx")
	require.True(isAmsterdam(cellInfo), "the cell-proof blob tx must be included in a post-Amsterdam L1 block")
	t.Log("Glamsterdam L1 verified and mined a Version1 cell-proof blob tx",
		"l1Block", cellInfo.NumberU64(), "tx", cellRcpt.TxHash)

	// Phase 4: a LEGACY Version0 sidecar is still accepted too (geth converts it via ToV1). This
	// is the tolerance the default batcher config depends on; pin it so a future L1 that drops it
	// shows up here rather than as a stalled batcher.
	legacyBlob := randomBlob(t)
	legacySidecar, legacyHashes, err := txmgr.MakeSidecar([]*eth.Blob{legacyBlob}, false)
	require.NoError(err, "build a legacy sidecar the way a default-configured batcher does")
	require.Equal(gethtypes.BlobSidecarVersion0, legacySidecar.Version, "legacy sidecar must be Version0")
	require.Len(legacySidecar.Proofs, len(legacySidecar.Blobs), "a Version0 sidecar carries one proof per blob")
	legacyRcpt, err := txplan.NewPlannedTx(txplan.Combine(
		eoa.Plan(),
		txplan.WithTo(&blobSink),
		txplan.WithGasLimit(200_000),
		withPreparedSidecar(legacyBlob, legacySidecar, legacyHashes, l1Config),
	)).Included.Eval(ctx)
	require.NoError(err, "a legacy Version0 blob tx must still be accepted by the post-Osaka Glamsterdam L1 (geth converts it via ToV1)")
	require.Equal(gethtypes.ReceiptStatusSuccessful, legacyRcpt.Status, "legacy blob tx must be mined successfully")
	t.Log("Glamsterdam L1 also accepted a legacy Version0 blob tx", "tx", legacyRcpt.TxHash)

	// Phase 5: find the BATCHER's own blob tx (To == batch inbox) on a post-Amsterdam L1 block.
	// Under this suite's CellProofTime=0 those were crafted through txmgr's cell-proof branch.
	// Scan from the head downward so we pick the most recent one, making "the safe head passes
	// it" a meaningful liveness requirement.
	batchInbox := rollupCfg.BatchInboxAddress
	var (
		batchTx  *gethtypes.Transaction
		blobInfo eth.BlockInfo
		found    bool
	)
	deadline := time.Now().Add(120 * time.Second)
	for !found && time.Now().Before(deadline) {
		l1Head := sys.L1EL.BlockRefByLabel(eth.Unsafe)
		floor := uint64(1)
		if l1Head.Number > 64 {
			floor = l1Head.Number - 64
		}
		for n := l1Head.Number; n >= floor && !found; n-- {
			info, txs, err := l1Eth.InfoAndTxsByNumber(ctx, n)
			require.NoError(err, "read L1 block %d", n)
			if !isAmsterdam(info) {
				continue
			}
			for _, tx := range txs {
				if tx.To() == nil || *tx.To() != batchInbox {
					continue
				}
				require.Equalf(uint8(gethtypes.BlobTxType), tx.Type(),
					"batcher tx to the inbox on L1 #%d must be an EIP-4844 blob tx (type-3)", info.NumberU64())
				require.NotEmptyf(tx.BlobHashes(),
					"batcher blob tx on L1 #%d must carry blob versioned hashes", info.NumberU64())
				batchTx = tx
				blobInfo = info
				found = true
				break
			}
		}
		if !found {
			select {
			case <-time.After(time.Second):
			case <-ctx.Done():
				require.Fail("context cancelled before a post-Amsterdam cell-proof blob batch appeared on L1")
			}
		}
	}
	require.True(found, "batcher must land a cell-proof EIP-4844 blob batch on a post-Amsterdam L1 within the deadline")

	batchRcpt, err := l1Eth.TransactionReceipt(ctx, batchTx.Hash())
	require.NoError(err, "batcher blob inbox tx must have an L1 receipt")
	require.Equalf(gethtypes.ReceiptStatusSuccessful, batchRcpt.Status,
		"batcher cell-proof blob tx on L1 #%d must be mined successfully", blobInfo.NumberU64())

	blobHeader := blobInfo.Header()
	require.NotNil(blobHeader.BlockAccessListHash, "post-Amsterdam blob-carrying L1 block must carry BlockAccessListHash")
	require.NotNil(blobHeader.SlotNumber, "post-Amsterdam blob-carrying L1 block must carry SlotNumber")
	t.Log("batcher landed a cell-proof blob batch on the Glamsterdam L1",
		"l1Block", blobInfo.NumberU64(), "tx", batchTx.Hash(), "blobs", len(batchTx.BlobHashes()))

	// Phase 6: anchor a SPECIFIC L2 block and prove IT — matched by number AND hash — was
	// reconstructed byte-identically from those cell-proof blobs.
	l2BlockTime := time.Duration(rollupCfg.BlockTime) * time.Second
	var target eth.L2BlockRef
	require.Eventually(func() bool {
		target = sys.L2EL.BlockRefByLabel(eth.Unsafe)
		return target.L1Origin.Number >= blobInfo.NumberU64()
	}, 120*time.Second, l2BlockTime, "an L2 unsafe block must build on a post-Amsterdam cell-proof-blob L1 origin")

	sys.L2CL.ReachedRef(suptypes.CrossSafe, eth.BlockID{Number: target.Number, Hash: target.Hash}, 120)
	t.Log("L2 block reconstructed byte-identically from cell-proof blobs across the Glamsterdam boundary",
		"l2", target.Number, "l1Origin", target.L1Origin.Number)

	// Phase 7: cross-check the reconstructed block's L1 origin is itself post-Amsterdam, so the
	// decode genuinely happened off a Glamsterdam L1.
	originInfo, _, err := l1Eth.InfoAndTxsByHash(ctx, target.L1Origin.Hash)
	require.NoError(err, "L1 origin of the reconstructed safe L2 block must exist on L1")
	require.True(isAmsterdam(originInfo), "the L1 origin referenced by the reconstructed L2 block must be post-Amsterdam")
	originHeader := originInfo.Header()
	require.NotNil(originHeader.BlockAccessListHash, "post-Amsterdam L2 safe-head L1 origin must carry BlockAccessListHash")
	require.NotNil(originHeader.SlotNumber, "post-Amsterdam L2 safe-head L1 origin must carry SlotNumber")
	t.Log("reconstructed L2 block's L1 origin is a Glamsterdam block",
		"safeOrigin", originInfo.NumberU64(), "blobBlock", blobInfo.NumberU64())
}

// withPreparedSidecar plans a blob tx carrying an already-built sidecar, so the test controls the
// sidecar VERSION instead of letting txplan.WithBlobs pick it from the fork. It reuses WithBlobs
// for the tx type and blob fee cap, then overrides only the sidecar and its versioned hashes.
func withPreparedSidecar(blob *eth.Blob, sidecar *gethtypes.BlobTxSidecar, hashes []common.Hash, config *params.ChainConfig) txplan.Option {
	return func(tx *txplan.PlannedTx) {
		txplan.WithBlobs([]*eth.Blob{blob}, config)(tx)
		tx.Sidecar.Fn(func(_ context.Context) (*gethtypes.BlobTxSidecar, error) {
			return sidecar, nil
		})
		tx.BlobHashes.Fn(func(_ context.Context) ([]common.Hash, error) {
			return hashes, nil
		})
	}
}

// randomBlob returns a blob whose field elements are all in range, so KZG commitment and proof
// generation succeed for both sidecar versions.
func randomBlob(t devtest.T) *eth.Blob {
	var blob eth.Blob
	_, err := rand.Read(blob[:])
	t.Require().NoError(err)
	for i := range 4096 {
		blob[32*i] &= 0b0011_1111
	}
	return &blob
}
