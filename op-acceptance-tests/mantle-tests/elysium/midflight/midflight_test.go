package midflight

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

// TestL1UpgradeMidFlight is a discriminating test: the Mantle L2 keeps
// processing user traffic WITHOUT interruption as the L1 upgrades to Glamsterdam
// (Amsterdam EL) mid-run.
//
// This is deliberately distinct from the smoke test, which only checks
// liveness AFTER the boundary. Here the L2 must be actively transacting BEFORE the
// boundary and keep succeeding THROUGH and AFTER it, with both txs safely derived —
// i.e. the fork moment does not stall the sequencer, the batcher, or derivation.
//
// SETUP. The L1 EL is an external Glamsterdam geth subprocess (helpers.go /
// init_test.go), and Amsterdam activates 60 SECONDS after L1 genesis (a deliberately
// larger offset than the smoke/derivation suites so a genuine pre-Amsterdam window
// exists — see init_test.go). The L2 is a Mantle (Arsia) minimal system.
//
// FLOW / DISCRIMINATION.
//	(a) Assert the current L1 head is still PRE-Amsterdam, then submit an L2 tx and
//	    require it succeeds — the L2 is genuinely transacting before the upgrade.
//	(b) Drive the L1 across the Amsterdam boundary (WaitForTime(AmsterdamTime)).
//	(c) After the L2's own L1 origin has crossed Amsterdam, submit a second L2 tx and
//	    require it succeeds — the L2 keeps taking user traffic after the upgrade.
//	(d) Require BOTH tx blocks reach the L2 SAFE head (derivation consolidated them
//	    from the L1 across the boundary), the pre-boundary block has a pre-Amsterdam
//	    L1 origin, and the post-boundary block has a post-Amsterdam L1 origin — so the
//	    two txs provably straddle the fork and derivation advanced past it.
//	(e) Sample post-boundary L2 headers and require each stays Arsia: no EIP-7928
//	    BlockAccessListHash and no EIP-7843 SlotNumber leaked onto the L2.
//
// This flips red if the L2 stalls at the fork moment (either tx fails or never
// reaches safe), if the two txs do not actually straddle the boundary (origins on
// the wrong side), or if the L2 adopts an Amsterdam header field as the L1 upgrades.
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

	// (a) BEFORE Amsterdam. The current L1 head must still be pre-Amsterdam, so the
	// first L2 tx is genuinely submitted while the L1 has NOT yet upgraded.
	l1Head := sys.L1EL.BlockRefByLabel(eth.Unsafe)
	require.Falsef(
		l1Config.IsAmsterdam(new(big.Int).SetUint64(l1Head.Number), l1Head.Time),
		"L1 head #%d (t=%d) must still be pre-Amsterdam when the pre-boundary tx is submitted; "+
			"raise amsterdamOffset (SECONDS) in init_test.go if bring-up consumed the whole window",
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
	sys.L1EL.WaitForTime(*l1Config.AmsterdamTime)
	t.Log("L1 Amsterdam activated")

	// The L2 sequencer's L1 origin lags the L1 head, so right after activation an L2 tx
	// can still take a pre-Amsterdam origin. Wait until the L2 unsafe origin itself
	// crosses Amsterdam so the post-boundary tx lands on a post-Amsterdam L1 origin.
	require.Eventually(func() bool {
		originNum := sys.L2EL.BlockRefByLabel(eth.Unsafe).L1Origin.Number
		originRef := sys.L1EL.BlockRefByNumber(originNum)
		return l1Config.IsAmsterdam(new(big.Int).SetUint64(originNum), originRef.Time)
	}, 120*time.Second, time.Second, "L2 unsafe origin must cross Amsterdam before the post-boundary tx")

	// (c) AFTER Amsterdam. Submit a second L2 tx from the SAME wallet; it must also
	// succeed, proving user traffic keeps flowing straight through the upgrade.
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

	// (d) BOTH tx blocks must reach the L2 SAFE head via derivation from the L1 — matched by
	// HASH (ReachedRef), not just height: the pipeline consumed the L1 continuously across the
	// upgrade and consolidated the byte-identical pre- and post-boundary blocks we submitted. A
	// divergent re-derivation at either height fails the hash match rather than slipping through.
	sys.L2CL.ReachedRef(suptypes.CrossSafe, eth.BlockID{Number: preBlock, Hash: preRcpt.BlockHash}, 90)
	sys.L2CL.ReachedRef(suptypes.CrossSafe, eth.BlockID{Number: postBlock, Hash: postRcpt.BlockHash}, 90)

	// The post-boundary tx's safe L2 block must have a genuinely post-Amsterdam L1
	// origin — derivation advanced PAST the upgrade, it did not stall at it.
	postRef := sys.L2EL.BlockRefByNumber(postBlock)
	postOrigin, _, err := sys.L1EL.EthClient().InfoAndTxsByHash(ctx, postRef.L1Origin.Hash)
	require.NoError(err, "L1 origin of the post-upgrade safe L2 block must exist on L1")
	require.Truef(
		l1Config.IsAmsterdam(new(big.Int).SetUint64(postOrigin.NumberU64()), postOrigin.Time()),
		"post-upgrade safe L2 block %d must have a post-Amsterdam L1 origin (got L1 #%d t=%d)",
		postBlock, postOrigin.NumberU64(), postOrigin.Time(),
	)

	// ...and the pre-boundary tx's safe L2 block must have a PRE-Amsterdam L1 origin,
	// so the two txs genuinely straddle the boundary (not both post-upgrade).
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
