package midflight

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

// TestL1UpgradeMidFlight verifies user traffic continues while the L1 upgrades
// to Glamsterdam mid-run.
//
// It submits one L2 tx before Amsterdam and one after the L2 origin crosses
// Amsterdam, then requires both blocks to become safe by hash. The origin checks
// prove the txs straddle the fork, and header checks ensure the L2 stays Arsia.
//
// The first assertion runs while the L1 is still pre-Amsterdam, so this package
// uses testmain.DefaultAmsterdamOffset to keep a pre-boundary window open.
func TestL1UpgradeMidFlight(gt *testing.T) {
	t := devtest.SerialT(gt)
	sys := presets.NewMantleMinimal(t)
	require := t.Require()
	ctx := t.Ctx()

	l1Config := sys.L1Network.Escape().ChainConfig()
	require.True(sys.L2Chain.IsMantleForkActive(opforks.MantleElysium), "L2 must run with Mantle Elysium active")
	require.NotNil(l1Config.AmsterdamTime, "L1 AmsterdamTime must be configured")

	// A single funded wallet transacts straight through the upgrade, to distinct
	// code-less EOA recipients so both txs are plain value transfers.
	wallet := sys.FunderL2.NewFundedEOA(eth.OneEther)
	recipientPre := common.HexToAddress("0x00000000000000000000000000000000BEEF0001")
	recipientPost := common.HexToAddress("0x00000000000000000000000000000000BEEF0002")

	// (a) Submit the first tx while the current L1 head is still pre-Amsterdam.
	l1Head := sys.L1EL.BlockRefByLabel(eth.Unsafe)
	require.Falsef(
		l1Config.IsAmsterdam(new(big.Int).SetUint64(l1Head.Number), l1Head.Time),
		"L1 head #%d (t=%d) must still be pre-Amsterdam when the pre-boundary tx is submitted; "+
			"use a fresh real-CL devnet with a later AmsterdamTime if bring-up consumed the whole window",
		l1Head.Number, l1Head.Time,
	)

	preRcpt, err := txplan.NewPlannedTx(txplan.Combine(
		wallet.Plan(),
		txplan.WithTo(&recipientPre),
		txplan.WithValue(eth.OneTenthEther),
		txplan.WithGasLimit(1_000_000),
	)).Included.Eval(ctx)
	require.NoError(err)
	require.Equal(gethtypes.ReceiptStatusSuccessful, preRcpt.Status, "pre-upgrade L2 tx must succeed")
	preBlock := preRcpt.BlockNumber.Uint64()
	t.Log("pre-upgrade L2 tx included", "block", preBlock, "hash", preRcpt.TxHash)

	// (b) Drive the L1 across the Glamsterdam (Amsterdam) boundary.
	t.Log("waiting for L1 Amsterdam to activate")
	testhelpers.WaitForGlamsterdamL1(t, sys.L1EL, *l1Config.AmsterdamTime)
	t.Log("L1 Amsterdam activated")

	// The L2 origin lags the L1 head; wait for the sequencer origin itself to cross.
	require.Eventually(func() bool {
		originNum := sys.L2EL.BlockRefByLabel(eth.Unsafe).L1Origin.Number
		originRef := sys.L1EL.BlockRefByNumber(originNum)
		return l1Config.IsAmsterdam(new(big.Int).SetUint64(originNum), originRef.Time)
	}, 120*time.Second, time.Second, "L2 unsafe origin must cross Amsterdam before the post-boundary tx")

	// (c) Submit a second tx after the L2 origin crosses Amsterdam.
	postRcpt, err := txplan.NewPlannedTx(txplan.Combine(
		wallet.Plan(),
		txplan.WithTo(&recipientPost),
		txplan.WithValue(eth.OneTenthEther),
		txplan.WithGasLimit(1_000_000),
	)).Included.Eval(ctx)
	require.NoError(err)
	require.Equal(gethtypes.ReceiptStatusSuccessful, postRcpt.Status, "post-upgrade L2 tx must succeed")
	postBlock := postRcpt.BlockNumber.Uint64()
	require.Greater(postBlock, preBlock, "post-upgrade tx must land in a strictly later L2 block")
	t.Log("post-upgrade L2 tx included", "block", postBlock, "hash", postRcpt.TxHash)

	// (d) Both tx blocks must reach safe by hash, not just height.
	sys.L2CL.ReachedRef(suptypes.CrossSafe, eth.BlockID{Number: preBlock, Hash: preRcpt.BlockHash}, 90)
	sys.L2CL.ReachedRef(suptypes.CrossSafe, eth.BlockID{Number: postBlock, Hash: postRcpt.BlockHash}, 90)

	// The post-boundary tx must derive from a post-Amsterdam L1 origin.
	postRef := sys.L2EL.BlockRefByNumber(postBlock)
	postOrigin, _, err := sys.L1EL.EthClient().InfoAndTxsByHash(ctx, postRef.L1Origin.Hash)
	require.NoError(err, "L1 origin of the post-upgrade safe L2 block must exist on L1")
	require.Truef(
		l1Config.IsAmsterdam(new(big.Int).SetUint64(postOrigin.NumberU64()), postOrigin.Time()),
		"post-upgrade safe L2 block %d must have a post-Amsterdam L1 origin (got L1 #%d t=%d)",
		postBlock, postOrigin.NumberU64(), postOrigin.Time(),
	)

	// The pre-boundary tx must still derive from a pre-Amsterdam L1 origin.
	preRef := sys.L2EL.BlockRefByNumber(preBlock)
	preOrigin, _, err := sys.L1EL.EthClient().InfoAndTxsByHash(ctx, preRef.L1Origin.Hash)
	require.NoError(err, "L1 origin of the pre-upgrade safe L2 block must exist on L1")
	require.Falsef(
		l1Config.IsAmsterdam(new(big.Int).SetUint64(preOrigin.NumberU64()), preOrigin.Time()),
		"pre-upgrade safe L2 block %d must have a pre-Amsterdam L1 origin (got L1 #%d t=%d)",
		preBlock, preOrigin.NumberU64(), preOrigin.Time(),
	)
	t.Log("txs straddle the boundary",
		"preBlock", preBlock, "preOrigin", preOrigin.NumberU64(),
		"postBlock", postBlock, "postOrigin", postOrigin.NumberU64())

	// (e) Sample a post-boundary L2 header past the post-upgrade tx; it must stay Arsia — no
	// Amsterdam header field leaked onto the L2 during the fork. Whether the L2 adopts a BAL /
	// slot-number field is a systematic code property (it would appear on every block or none),
	// so one post-boundary sample is discriminating; wait for it to be produced first.
	sample := postBlock + 6
	sys.L2EL.WaitForUnsafe(func(bi eth.BlockInfo) (bool, error) {
		return bi.NumberU64() >= sample, nil
	})
	ref := sys.L2EL.BlockRefByNumber(sample)
	info, _, err := sys.L2EL.Escape().EthClient().InfoAndTxsByHash(ctx, ref.Hash)
	require.NoErrorf(err, "must read L2 block %d by hash", sample)
	require.Equalf(sample, info.NumberU64(), "L2 block %d returned unexpected number", sample)
	header := info.Header()
	require.Nilf(header.BlockAccessListHash,
		"L2 (Arsia) block %d must not carry an EIP-7928 BlockAccessListHash", sample)
	require.Nilf(header.SlotNumber,
		"L2 (Arsia) block %d must not carry an EIP-7843 SlotNumber", sample)
}
