package evm

import (
	"encoding/binary"
	"testing"

	"github.com/ethereum-optimism/optimism/op-acceptance-tests/mantle-tests/elysium/internal/testhelpers"
	"github.com/ethereum-optimism/optimism/op-devstack/devtest"
	"github.com/ethereum-optimism/optimism/op-devstack/dsl"
	"github.com/ethereum-optimism/optimism/op-devstack/presets"
	"github.com/ethereum-optimism/optimism/op-service/eth"
	"github.com/ethereum-optimism/optimism/op-service/txplan"
	"github.com/ethereum/go-ethereum/common"
	gethTypes "github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm/program"
	"github.com/ethereum/go-ethereum/params"
	"github.com/holiman/uint256"
)

// TestL2EVM_PectraFeatures asserts that the Pectra/Isthmus EVM features the Mantle L2 adopted at
// the Skadi fork stay active and correct on the current (Elysium) L2 while the L1 it consumes runs
// Glamsterdam:
//
//   - 7702: a SetCode transaction delegates an EOA to a contract; the EOA's code becomes the
//     delegation designator and the delegated code executes in the EOA's storage.
//   - 2935: the block-hash history contract is deployed and each block records its parent hash in
//     the slot EIP-2935 specifies.
//   - 7685: the L2 header's requestsHash is present and equals the empty requests hash (Mantle
//     emits no execution-layer requests).
//
// These three assertions previously lived only in mantle-tests/skadi, a sysext-only suite pinned to
// a 2^50 gas limit that reverts under sysgo and so never ran in per-PR CI. They are permanent L2
// behaviors -- inherited by every fork after Skadi (Limb, Arsia, Elysium) -- not Skadi
// activation-boundary checks, so they were relocated here (an Elysium-at-genesis, CI-gated package
// where the features are active and sysgo can run them) before the skadi suite was retired. The
// grep that would have caught the drift: 7702 (SetCode/AddressToDelegation) and 2935
// (HistoryStorageAddress) appear nowhere else in mantle-tests, and the empty-requestsHash *value*
// (as opposed to mere key presence, covered by elysium/base/rpcschema) is asserted only here.
func TestL2EVM_PectraFeatures(gt *testing.T) {
	gt.Run("7702_SetCodeTxBasic", runSetCode7702)
	gt.Run("2935_BlockHistoryConsistency", runBlockHistory2935)
	gt.Run("7685_EmptyRequestsHash", runEmptyRequestsHash7685)
}

// pectraSetup brings up the Elysium L2 and blocks until its L1 has actually crossed to Glamsterdam,
// mirroring the sibling evm sub-cases: the L1 is the environment, the L2 property is the assertion.
func pectraSetup(gt *testing.T) (devtest.T, *presets.MantleMinimal) {
	t := devtest.SerialT(gt)
	sys := presets.NewMantleMinimal(t)

	l1Config := sys.L1Network.Escape().ChainConfig()
	t.Require().NotNil(l1Config.AmsterdamTime, "L1 AmsterdamTime must be configured")
	testhelpers.WaitForGlamsterdamL1(t, sys.L1EL, *l1Config.AmsterdamTime)

	return t, sys
}

// runSetCode7702 deploys a store contract, then sends a 7702 SetCode tx delegating an EOA to it and
// checks the EOA gains the delegation designator and the delegated SSTORE lands in the EOA's slot 0.
func runSetCode7702(gt *testing.T) {
	t, sys := pectraSetup(gt)
	require := t.Require()

	alice := sys.FunderL2.NewFundedEOA(eth.HundredEther)

	// Part 1: deploy a contract whose runtime SSTOREs 0xbeef into slot 0.
	storeProgram := program.New().Sstore(0, 0xbeef).Bytes()
	initCode := program.New().ReturnViaCodeCopy(storeProgram)
	storeAddr := deployStoreContract(t, alice, initCode.Bytes())
	t.Logf("Store contract deployed at: %s", storeAddr.Hex())

	l2Client := sys.L2EL.Escape().EthClient()

	latestBlock, err := l2Client.InfoByLabel(t.Ctx(), eth.Unsafe)
	require.NoError(err)

	code, err := l2Client.CodeAtHash(t.Ctx(), storeAddr, latestBlock.Hash())
	require.NoError(err)
	require.NotEmpty(code, "Store contract not deployed")
	require.Equal(code, storeProgram, "Store contract code incorrect")

	// Part 2: send the SetCode transaction from alice, delegating her EOA to the store contract.
	fromAddr := alice.Address()
	nonce, err := l2Client.PendingNonceAt(t.Ctx(), fromAddr)
	require.NoError(err, "Failed to fetch pending nonce")

	auth1, err := gethTypes.SignSetCode(alice.Key().Priv(), gethTypes.SetCodeAuthorization{
		ChainID: *uint256.MustFromBig(sys.L2Chain.ChainID().ToBig()),
		Address: storeAddr,
		// before the nonce is compared with the authorization in the EVM, it is incremented by 1.
		Nonce: nonce + 1,
	})
	require.NoError(err, "Failed to sign 7702 authorization")

	setCodeTx := txplan.NewPlannedTx(txplan.Combine(
		alice.Plan(),
		txplan.WithType(gethTypes.SetCodeTxType),
		txplan.WithTo(&fromAddr),
		txplan.WithAuthorizations([]gethTypes.SetCodeAuthorization{auth1}),
	))

	receipt, err := setCodeTx.Included.Eval(t.Ctx())
	require.NoError(err)
	require.Equal(uint64(1), receipt.Status)

	newBlock, err := l2Client.InfoByLabel(t.Ctx(), eth.Unsafe)
	require.NoError(err)

	// alice's EOA code must now be the delegation designator (0xef0100 || storeAddr).
	code, err = l2Client.CodeAtHash(t.Ctx(), fromAddr, newBlock.Hash())
	require.NoError(err)
	wantCode := gethTypes.AddressToDelegation(auth1.Address)
	require.Equal(wantCode, code, "Delegation code incorrect")

	// and the delegated code's SSTORE must have written 0xbeef into alice's slot 0.
	storageValue, err := l2Client.GetStorageAt(t.Ctx(), fromAddr, common.Hash{}, newBlock.Hash().String())
	require.NoError(err)
	require.EqualValues(storageValue, common.BytesToHash([]byte{0xbe, 0xef}), "Storage slot not set in delegated EOA")
}

// runBlockHistory2935 checks that the EIP-2935 history-storage contract is deployed and that the
// slot for the previous block holds that block's hash.
func runBlockHistory2935(gt *testing.T) {
	t, sys := pectraSetup(gt)
	require := t.Require()

	l2Client := sys.L2EL.Escape().EthClient()

	latestBlock := sys.L2EL.WaitForBlockNumber(2) // need at least 2 blocks for the history slot

	code, err := l2Client.CodeAtHash(t.Ctx(), params.HistoryStorageAddress, latestBlock.Hash())
	require.NoError(err)
	require.NotEmpty(code, "Block history contract not deployed")

	// The parent block's hash is stored at slot ((latest-1) % HistoryServeWindow).
	parentHashSlotNum := (latestBlock.NumberU64() - 1) % params.HistoryServeWindow
	parentHashSlot := make([]byte, 32)
	binary.BigEndian.PutUint64(parentHashSlot[24:32], parentHashSlotNum)

	parentHashSlotValue, err := l2Client.GetStorageAt(t.Ctx(), params.HistoryStorageAddress, common.BytesToHash(parentHashSlot), latestBlock.Hash().String())
	require.NoError(err)

	require.EqualValues(
		latestBlock.ParentHash(),
		parentHashSlotValue,
		"Parent block hash in history contract does not match the latest block's parent hash",
	)
}

// runEmptyRequestsHash7685 checks the L2 header carries a requestsHash equal to the empty requests
// hash (Mantle emits no execution-layer requests, so the value is a constant, not merely present).
func runEmptyRequestsHash7685(gt *testing.T) {
	t, sys := pectraSetup(gt)
	require := t.Require()

	var rawHeader struct {
		RequestsHash *common.Hash `json:"requestsHash,omitempty"`
	}
	err := sys.L2EL.Escape().L2EthClient().RPC().CallContext(t.Ctx(), &rawHeader, "eth_getBlockByNumber", "latest", false)
	require.NoError(err, "Failed to get raw block header")

	require.NotNil(rawHeader.RequestsHash, "RequestsHash field not found - chain may not be Isthmus-activated")
	require.Equal(*rawHeader.RequestsHash, gethTypes.EmptyRequestsHash, "Requests hash is not empty on L2")
}

// deployStoreContract deploys init code that returns a runtime blob and returns the new address.
func deployStoreContract(t devtest.T, user *dsl.EOA, initCode []byte) common.Address {
	require := t.Require()

	deployTx := txplan.NewPlannedTx(txplan.Combine(
		user.Plan(),
		txplan.WithData(initCode),
	))
	receipt, err := deployTx.Included.Eval(t.Ctx())
	require.NoError(err)
	require.Equal(uint64(1), receipt.Status, "Contract deployment failed")
	require.NotNil(receipt.ContractAddress, "Contract address not set in receipt")

	return receipt.ContractAddress
}
