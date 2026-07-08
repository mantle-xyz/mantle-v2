package depositcrossing

import (
	"math/big"
	"testing"
	"time"

	"github.com/ethereum-optimism/optimism/op-devstack/devtest"
	"github.com/ethereum-optimism/optimism/op-devstack/dsl"
	"github.com/ethereum-optimism/optimism/op-devstack/presets"
	"github.com/ethereum-optimism/optimism/op-devstack/stack"
	"github.com/ethereum-optimism/optimism/op-devstack/stack/match"
	"github.com/ethereum-optimism/optimism/op-node/rollup/derive"
	"github.com/ethereum-optimism/optimism/op-service/eth"
	"github.com/ethereum-optimism/optimism/op-service/txintent/bindings"
	"github.com/ethereum-optimism/optimism/op-service/txintent/contractio"
	"github.com/ethereum-optimism/optimism/op-service/txplan"
	"github.com/ethereum-optimism/optimism/op-test-sequencer/sequencer/seqtypes"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/params"
)

const l1BlockTime = 6 * time.Second

const depositGasLimit uint32 = 300_000

// bvmETHAddr is the Mantle L2 BVM_ETH predeploy that an L1 ETH deposit mints into.
var bvmETHAddr = common.HexToAddress("0xdEAddEaDdeadDEadDEADDEAddEADDEAddead1111")

// TestDeposit_AcrossL1Upgrade_ThreePositions lands three L1->L2 ETH deposits in three
// L1 blocks spanning the Glamsterdam activation boundary — one BEFORE the activation block,
// one AT it, one AFTER — and asserts:
//   - each is credited as BVM_ETH on the Mantle L2 by BOTH the sequencer and an independent
//     verifier (the "sequencer 与 verifier 对同一 L1 块还原出一致的 deposit" requirement);
//   - the two post-Amsterdam deposit L1 receipts carry a real EIP-7708 system Transfer log
//     (DepositETH moves native L1 ETH, so post-Amsterdam L1 emits one), yet op-node's ACTUAL
//     derivation output — the deposit transactions it produces into the L2 — contains exactly
//     ONE user deposit per L1 block, equal to the real deposit and never a spurious one from the
//     7708 log (its address+topic0 filter neither mis-takes the 7708 log nor drops the deposit).
//
// Deposits are placed in exact L1 blocks by taking manual control of L1 production via the
// TestSequencer: each deposit tx is submitted to the L1 mempool (non-blocking) and then a
// single L1 block is produced to include it. This test drives L1 exclusively, so it is the
// only test in this package.
func TestDeposit_AcrossL1Upgrade_ThreePositions(gt *testing.T) {
	t := devtest.SerialT(gt)
	sys := presets.NewMantleSingleChainMultiNodeWithTestSeq(t)
	require := t.Require()
	logger := t.Logger()
	ctx := t.Ctx()

	l1Config := sys.L1Network.Escape().ChainConfig()
	require.NotNil(l1Config.AmsterdamTime, "L1 AmsterdamTime must be configured")

	ts := sys.TestSequencer.Escape().ControlAPI(sys.L1Network.ChainID())
	cl := sys.L1Network.Escape().L1CLNode(match.FirstL1CL)

	sys.L1Network.WaitForBlock()

	// Fund three distinct L1 depositors (so their L1 nonces and L2 BVM_ETH balances are
	// independent) WHILE the auto-FakePoS is still producing L1 blocks — funding needs blocks.
	// Only after they are funded do we take manual control of L1 production.
	userBefore := sys.FunderL1.NewFundedEOA(eth.OneTenthEther)
	userAt := sys.FunderL1.NewFundedEOA(eth.OneTenthEther)
	userAfter := sys.FunderL1.NewFundedEOA(eth.OneTenthEther)

	// Take manual control of L1 production so deposits can be placed in exact L1 blocks.
	sys.ControlPlane.FakePoSState(cl.ID(), stack.Stop)

	// Amsterdam activates at L1 block expectedBoundary (offset is seconds; 6s blocks).
	expectedBoundary := amsterdamOffset / uint64(l1BlockTime/time.Second)
	require.GreaterOrEqual(expectedBoundary, uint64(3), "offset must leave room for a pre-Amsterdam deposit block")
	require.Less(sys.L1EL.BlockRefByLabel(eth.Unsafe).Number, expectedBoundary-1,
		"must take L1 control before the activation block so a deposit can land pre-Amsterdam")

	bridgeAddr := sys.L2Chain.Escape().Deployment().L1StandardBridgeProxyAddr()
	bridge := bindings.NewBindings[bindings.MantleL1StandardBridge](
		bindings.WithTest(t), bindings.WithClient(sys.L1EL.EthClient()), bindings.WithTo(bridgeAddr))
	portalAddr := sys.L2Chain.Escape().RollupConfig().DepositContractAddress

	produceL1Block := func() {
		require.NoError(ts.New(ctx, seqtypes.BuildOpts{Parent: common.Hash{}}))
		require.NoError(ts.Next(ctx))
	}
	driveL1To := func(target uint64) {
		for sys.L1EL.BlockRefByLabel(eth.Unsafe).Number < target {
			produceL1Block()
		}
	}

	depositAmount := eth.GWei(1_000_000) // 0.001 ETH per deposit
	submitDeposit := func(user *dsl.EOA) *txplan.PlannedTx {
		call := bridge.DepositETH(depositGasLimit, []byte{})
		plan, err := contractio.Plan(call)
		require.NoError(err)
		tx := txplan.NewPlannedTx(plan, txplan.Combine(
			user.Plan(), txplan.WithValue(depositAmount), txplan.WithGasRatio(2.0)))
		_, err = tx.Submitted.Eval(ctx)
		require.NoError(err, "deposit L1 tx must submit to the mempool")
		return tx
	}

	// 前: land a deposit in the last pre-Amsterdam block (boundary-1).
	driveL1To(expectedBoundary - 2)
	txBefore := submitDeposit(userBefore)
	produceL1Block() // block boundary-1 (pre-Amsterdam)
	// 当块: land a deposit in the activation block itself.
	txAt := submitDeposit(userAt)
	produceL1Block() // block boundary (first Amsterdam)
	// 后: land a deposit in the first post-activation block.
	txAfter := submitDeposit(userAfter)
	produceL1Block() // block boundary+1 (post-Amsterdam)

	// checkL1 verifies the INPUT side on L1: the deposit landed at the intended boundary position,
	// its receipt carries exactly one real portal deposit log, and (post-Amsterdam) also a real
	// EIP-7708 system Transfer log. It returns the L1 block number and the L2 deposit tx hash that
	// op-node MUST derive from the real deposit log — checked against op-node's actual output below.
	checkL1 := func(name string, tx *txplan.PlannedTx, wantAmsterdam bool) (uint64, common.Hash) {
		r, err := tx.Included.Eval(ctx)
		require.NoErrorf(err, "%s: L1 deposit must be included", name)
		require.Equalf(types.ReceiptStatusSuccessful, r.Status, "%s: L1 deposit must succeed", name)
		blkNum := r.BlockNumber.Uint64()
		blk := sys.L1EL.BlockRefByNumber(blkNum)
		isAms := l1Config.IsAmsterdam(new(big.Int).SetUint64(blkNum), blk.Time)
		require.Equalf(wantAmsterdam, isAms, "%s: deposit L1 block %d Amsterdam-ness", name, blkNum)

		depositLogs, has7708 := 0, false
		var l2DepositHash common.Hash
		for _, lg := range r.Logs {
			if lg.Address == portalAddr {
				if dep, err := derive.UnmarshalDepositLogEvent(lg); err == nil {
					depositLogs++
					l2DepositHash = types.NewTx(dep).Hash() // the L2 deposit tx op-node must derive
				}
			}
			if lg.Address == params.SystemAddress && len(lg.Topics) > 0 && lg.Topics[0] == params.EthTransferLogEvent {
				has7708 = true
			}
		}
		require.Equalf(1, depositLogs, "%s: the L1 receipt must carry exactly one real portal deposit log", name)
		if wantAmsterdam {
			require.Truef(has7708, "%s: post-Amsterdam ETH deposit receipt must carry an EIP-7708 system Transfer log", name)
		} else {
			require.Falsef(has7708, "%s: pre-Amsterdam ETH deposit receipt must not carry a 7708 log", name)
		}
		logger.Info("deposit landed", "name", name, "l1Block", blkNum, "amsterdam", isAms, "has7708", has7708)
		return blkNum, l2DepositHash
	}
	l1Before, hashBefore := checkL1("before", txBefore, false)
	l1At, hashAt := checkL1("at", txAt, true)
	l1After, hashAfter := checkL1("after", txAfter, true)

	// Pre-resolve each depositor against the sequencer and the verifier EL.
	seqBefore, verBefore := userBefore.AsEL(sys.L2EL), userBefore.AsEL(sys.L2ELB)
	seqAt, verAt := userAt.AsEL(sys.L2EL), userAt.AsEL(sys.L2ELB)
	seqAfter, verAfter := userAfter.AsEL(sys.L2EL), userAfter.AsEL(sys.L2ELB)
	// Each depositor is a fresh EOA making exactly one deposit, so a correct derivation credits
	// EXACTLY depositAmount of BVM_ETH — never more. Using == (not >=) means a double-count
	// (e.g. op-node fooled into taking the 7708 system Transfer log as a second deposit) would
	// over-credit and fail here — closing the "must not mis-take" half of the requirement that
	// a >= check silently lets through.
	credited := func(l2User *dsl.EOA) bool {
		return l2User.GetTokenBalance(bvmETHAddr).ToBig().Cmp(depositAmount.ToBig()) == 0
	}

	// Drive the L2 (by advancing the clock, keeping the L1 a little ahead so derivation always
	// has blocks) until all three deposits are credited as BVM_ETH on BOTH the sequencer and
	// the independent verifier — proving both derive the same deposits from the same L1 blocks.
	require.Eventually(func() bool {
		sys.AdvanceTime(2 * time.Second)
		l2origin := sys.L2EL.BlockRefByLabel(eth.Unsafe).L1Origin.Number
		if l2origin+2 >= sys.L1EL.BlockRefByLabel(eth.Unsafe).Number {
			produceL1Block()
		}
		return credited(seqBefore) && credited(seqAt) && credited(seqAfter) &&
			credited(verBefore) && credited(verAt) && credited(verAfter)
	}, 240*time.Second, 300*time.Millisecond)

	// --- OUTPUT side: OBSERVE op-node's ACTUAL derivation, not a receipt-side replica ----------
	// For each deposit's L1 block, find the L2 epoch-opening block (the first L2 block whose L1
	// origin IS that block — it carries that L1 block's deposits) and inspect the deposit
	// transactions op-node ACTUALLY produced. Besides the single L1-attributes deposit that opens
	// every L2 block, there must be EXACTLY ONE user deposit, and it must be OUR reconstructed
	// deposit tx — proving op-node neither dropped the real deposit nor minted a spurious one from
	// the EIP-7708 system Transfer log.
	assertDerived := func(name string, l1BlockNum uint64, wantL2DepositHash common.Hash) {
		head := sys.L2EL.BlockRefByLabel(eth.Unsafe).Number
		var epochHash common.Hash
		found := false
		for n := uint64(1); n <= head; n++ {
			ref := sys.L2EL.BlockRefByNumber(n)
			if ref.L1Origin.Number == l1BlockNum {
				epochHash = ref.Hash
				found = true
				break
			}
		}
		require.Truef(found, "%s: must find the L2 epoch-opening block for L1 origin %d", name, l1BlockNum)

		_, txs, err := sys.L2EL.Escape().EthClient().InfoAndTxsByHash(ctx, epochHash)
		require.NoErrorf(err, "%s: must read the L2 epoch block transactions", name)

		depositTxs, ourFound := 0, false
		for _, tx := range txs {
			if tx.Type() != types.DepositTxType {
				continue
			}
			depositTxs++
			if tx.Hash() == wantL2DepositHash {
				ourFound = true
			}
		}
		// The epoch-opening L2 block carries the L1-attributes deposit (always index 0) plus the
		// user deposits from that L1 block; we placed exactly ONE user deposit there.
		require.Equalf(2, depositTxs,
			"%s: op-node must derive exactly 1 user deposit (+1 L1-attributes) from L1 block %d — a 7708-fooled op-node would add a spurious deposit", name, l1BlockNum)
		require.Truef(ourFound,
			"%s: op-node must derive OUR exact deposit tx into the L2 epoch block (not drop the real one)", name)
		logger.Info("op-node derived exactly the real deposit", "name", name, "l1Block", l1BlockNum, "l2DepositTxs", depositTxs)
	}
	assertDerived("before", l1Before, hashBefore)
	assertDerived("at", l1At, hashAt)
	assertDerived("after", l1After, hashAfter)

	logger.Info("all three boundary-spanning deposits credited on both sequencer and verifier; op-node derived exactly the three real deposits, unfooled by the EIP-7708 logs",
		"depositAmount", depositAmount)
}
