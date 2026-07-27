package crossing

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

// TestDerivation_AcrossL1Upgrade places deposits at three positions — before,
// at, and after L1 Amsterdam activation — and proves each is correctly derived
// into the L2: BVM_ETH credits equally on sequencer and verifier, the derived
// epoch block carries exactly one user deposit plus the L1-attributes deposit,
// and EIP-7708 system Transfer logs do not produce spurious L2 deposits.
func TestDerivation_AcrossL1Upgrade(gt *testing.T) {
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

	// Fund before stopping FakePoS; funding needs L1 blocks.
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

	// Land a deposit in the last pre-Amsterdam block.
	driveL1To(expectedBoundary - 2)
	txBefore := submitDeposit(userBefore)
	produceL1Block() // block boundary-1 (pre-Amsterdam)
	// Land a deposit in the activation block.
	txAt := submitDeposit(userAt)
	produceL1Block() // block boundary (first Amsterdam)
	// Land a deposit in the first post-activation block.
	txAfter := submitDeposit(userAfter)
	produceL1Block() // block boundary+1 (post-Amsterdam)

	// checkL1 verifies the receipt and returns the L2 deposit tx hash op-node must derive.
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
	// Equality catches double-crediting from a misinterpreted EIP-7708 log.
	credited := func(l2User *dsl.EOA) bool {
		return l2User.GetTokenBalance(bvmETHAddr).ToBig().Cmp(depositAmount.ToBig()) == 0
	}

	// Wait until sequencer and verifier both credit all three BVM_ETH deposits.
	require.Eventually(func() bool {
		sys.AdvanceTime(2 * time.Second)
		l2origin := sys.L2EL.BlockRefByLabel(eth.Unsafe).L1Origin.Number
		if l2origin+2 >= sys.L1EL.BlockRefByLabel(eth.Unsafe).Number {
			produceL1Block()
		}
		return credited(seqBefore) && credited(seqAt) && credited(seqAfter) &&
			credited(verBefore) && credited(verAt) && credited(verAfter)
	}, 240*time.Second, 300*time.Millisecond)

	// Inspect op-node's derived L2 epoch block, not a receipt-side reconstruction.
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
		// The epoch-opening block has one L1-attributes deposit plus our one user deposit.
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
