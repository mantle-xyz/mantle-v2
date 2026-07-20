package deposit

import (
	"testing"
	"time"

	"github.com/ethereum-optimism/optimism/op-devstack/devtest"
	"github.com/ethereum-optimism/optimism/op-devstack/presets"
	"github.com/ethereum-optimism/optimism/op-node/rollup/derive"
	"github.com/ethereum-optimism/optimism/op-service/eth"
	"github.com/ethereum-optimism/optimism/op-service/txintent/bindings"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
)

// initCode deploys a 32-byte runtime word ending in 0x42.
//
//	PUSH1 0x42 ; PUSH1 0x00 ; MSTORE ; PUSH1 0x20 ; PUSH1 0x00 ; RETURN
var (
	creationInitCode    = common.FromHex("0x604260005260206000f3")
	creationRuntimeCode = common.FromHex("0x0000000000000000000000000000000000000000000000000000000000000042")
)

// TestDepositContractCreationByPortal verifies that an L1->L2 deposit contract
// creation (`isCreation=true`, `to=nil`) reports the correct `contractAddress`
// in its L2 receipt.
//
// Regression test for the op-reth bug where the RPC layer derived
// `contractAddress` from `tx.nonce()` (hard-coded to 0 for deposits) instead of
// the sender's real L2 nonce (the deposit nonce). The bug only manifests when the
// deposit sender's L2 nonce > 0, so the test bumps it first; otherwise
// `CREATE(from, 0)` would be coincidentally correct.
func TestDepositContractCreationByPortal(gt *testing.T) {
	t := devtest.SerialT(gt)
	sys := presets.NewMantleMinimal(t)

	sys.L1Network.WaitForOnline()

	l1User := sys.FunderL1.NewFundedEOA(eth.OneTenthEther)
	l2User := l1User.AsEL(sys.L2EL)
	sys.FunderL2.FundAtLeast(l2User, eth.OneTenthEther)

	// Bump the deposit sender's L2 nonce above 0 (two zero-value self-transfers).
	l2User.Transfer(l2User.Address(), eth.ZeroWei).Included.Value()
	l2User.Transfer(l2User.Address(), eth.ZeroWei).Included.Value()

	l2Client := sys.L2EL.Escape().EthClient()
	from := l1User.Address() // Mantle has no L1->L2 address aliasing
	nonce, err := l2Client.PendingNonceAt(t.Ctx(), from)
	t.Require().NoError(err)
	t.Require().Greater(nonce, uint64(0), "deposit sender L2 nonce must be > 0 to exercise the bug")

	expectedAddr := crypto.CreateAddress(from, nonce)

	portalAddr := sys.L2Chain.Escape().RollupConfig().DepositContractAddress
	portal := bindings.NewBindings[bindings.MantleOptimismPortal](
		bindings.WithTest(t),
		bindings.WithClient(sys.L1EL.EthClient()),
		bindings.WithTo(portalAddr),
	)
	// value=0, mint=0, to=zero (ignored for creation), msgValue=0, gasLimit, isCreation=true, data=initCode
	call := portal.DepositTransaction(eth.ZeroWei, eth.ZeroWei, common.Address{}, eth.ZeroWei, 1_000_000, true, creationInitCode)
	l1Receipt := writeDepositTx(t, l1User, call, eth.ZeroWei)

	l2Receipt, depositFrom := getL2DepositReceipt(t, sys, l1Receipt)
	t.Require().Equal(types.ReceiptStatusSuccessful, l2Receipt.Status, "L2 deposit creation failed")
	t.Require().Equal(from, depositFrom, "Mantle deposit sender must not be aliased")

	// 1) contractAddress must be derived from the L2 nonce, not the deposit tx nonce (0).
	t.Require().Equal(expectedAddr, l2Receipt.ContractAddress,
		"receipt.contractAddress must equal CREATE(from, l2Nonce)")
	t.Require().NotEqual(crypto.CreateAddress(from, 0), l2Receipt.ContractAddress,
		"receipt.contractAddress must NOT be CREATE(from, 0)")

	// 2) the contract is actually deployed at the reported address.
	latest, err := l2Client.InfoByLabel(t.Ctx(), eth.Unsafe)
	t.Require().NoError(err)
	code, err := l2Client.CodeAtHash(t.Ctx(), l2Receipt.ContractAddress, latest.Hash())
	t.Require().NoError(err)
	t.Require().Equal(creationRuntimeCode, code, "deployed runtime code mismatch at receipt.contractAddress")
}

// getL2DepositReceipt reconstructs the L2 deposit tx from the L1 receipt's
// TransactionDeposited log, waits for its L2 receipt, and returns it together
// with the deposit sender address.
func getL2DepositReceipt(t devtest.T, sys *presets.MantleMinimal, l1Receipt *types.Receipt) (*types.Receipt, common.Address) {
	var l2DepositTx *types.DepositTx
	for _, log := range l1Receipt.Logs {
		if dep, err := derive.UnmarshalDepositLogEvent(log); err == nil {
			l2DepositTx = dep
			break
		}
	}
	t.Require().NotNil(l2DepositTx, "could not reconstruct L2 deposit transaction")

	hash := types.NewTx(l2DepositTx).Hash()
	sequencingWindow := time.Duration(sys.L2Chain.Escape().RollupConfig().SeqWindowSize) * sys.L1EL.EstimateBlockTime()
	var l2Receipt *types.Receipt
	t.Require().Eventually(func() bool {
		var err error
		l2Receipt, err = sys.L2EL.Escape().EthClient().TransactionReceipt(t.Ctx(), hash)
		return err == nil
	}, sequencingWindow, 500*time.Millisecond, "L2 deposit never found")

	return l2Receipt, l2DepositTx.From
}
