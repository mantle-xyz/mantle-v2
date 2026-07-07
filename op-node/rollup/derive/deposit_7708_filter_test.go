package derive

import (
	"math/big"
	"math/rand"
	"testing"

	"github.com/holiman/uint256"
	"github.com/stretchr/testify/require"

	"github.com/ethereum-optimism/optimism/op-service/testutils"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/params"
)

// TestUserDeposits_Ignores7708TransferLog is the GAP A(i) discriminating test for
// the "L1 Glamsterdam compatibility" suite: with the L1 upgraded to Glamsterdam
// (Amsterdam EL), EIP-7708 makes every non-zero ETH transfer emit a system-level
// "Transfer(address,address,uint256)" log. op-geth's Transfer() emits it under
// rules.IsAmsterdam:
//
//	op-geth core/evm.go:151-152
//	    if rules.IsAmsterdam && !amount.IsZero() && sender != recipient {
//	        db.AddLog(types.EthTransferLog(sender, recipient, amount))
//
// and op-geth core/types/log.go:70-81 builds that log with
//   - Address = params.SystemAddress (0xffff...fffe)
//   - Topics[0] = params.EthTransferLogEvent
//     = keccak256("Transfer(address,address,uint256)")
//     = 0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef
//   - exactly 3 topics: [event, from, to]
//
// op-node's deposit derivation must parse ONLY real deposits and never mis-read a
// 7708 Transfer log as a deposit. The filter is UserDeposits (deposits.go:22):
//
//	if log.Address == depositContractAddr && len(log.Topics) > 0 && log.Topics[0] == DepositEventABIHash {
//
// with DepositEventABIHash = keccak256("TransactionDeposited(address,address,uint256,bytes)")
// (deposit_log.go:18-19), and UnmarshalDepositLogEvent additionally requires
// exactly 4 topics (deposit_log.go:37-39).
//
// DISCRIMINATION. The real deposit topic0 differs from the 7708 topic0, and the
// deposit event has 4 topics vs 7708's 3. This test seeds one receipt with a real
// TransactionDeposited log PLUS two 7708-shaped Transfer logs and asserts exactly
// one real deposit comes out, with no error. It would FAIL if the L2 stack ever
// adopted the Amsterdam 7708 log into the deposit path:
//   - Real deposit topic0 0x<TransactionDeposited> vs Amsterdam 7708 topic0
//     0xddf252ad... — if the topic0 guard were dropped, log #2 (a 7708 shape placed
//     AT the deposit-contract address) would reach UnmarshalDepositLogEvent, which
//     rejects it (only 3 topics) → UserDeposits returns a non-nil error → NoError fails.
//   - If the address guard were loosened, the SystemAddress 7708 log (#1) would also
//     be attempted and rejected the same way.
//
// Either regression flips this test red; on correct Arsia-only behaviour it is green.
func TestUserDeposits_Ignores7708TransferLog(t *testing.T) {
	rng := rand.New(rand.NewSource(1234))
	blockHash := testutils.RandomHash(rng)

	// 1) A genuine deposit log emitted by the deposit contract.
	source := UserDepositSource{L1BlockHash: blockHash, LogIndex: 0}
	realDep := testutils.GenerateDeposit(source.SourceHash(), rng)
	depLog, err := MarshalDepositLogEventV0(MockDepositContractAddr, realDep)
	require.NoError(t, err)
	depLog.TxIndex = 0
	depLog.Index = 0
	depLog.BlockHash = blockHash

	// 2) A real EIP-7708 system Transfer log exactly as op-geth Amsterdam emits it:
	//    address = params.SystemAddress, topic0 = params.EthTransferLogEvent, 3 topics.
	//    Constructed via the actual op-geth constructor so this test tracks the real
	//    Amsterdam signature the op-node build links against.
	sysTransfer := types.EthTransferLog(
		testutils.RandomAddress(rng),
		testutils.RandomAddress(rng),
		uint256.NewInt(1_000_000_000),
	)
	sysTransfer.TxIndex = 0
	sysTransfer.Index = 1
	sysTransfer.BlockHash = blockHash

	// 3) Adversarial: the SAME 7708 Transfer shape but planted AT the deposit-contract
	//    address, to defeat the address clause and force the topic0/topic-count clauses
	//    to do the filtering.
	advTransfer := types.EthTransferLog(
		testutils.RandomAddress(rng),
		testutils.RandomAddress(rng),
		uint256.NewInt(2_000_000_000),
	)
	advTransfer.Address = MockDepositContractAddr
	advTransfer.TxIndex = 0
	advTransfer.Index = 2
	advTransfer.BlockHash = blockHash

	// Sanity: confirm the 7708 signature we assert against is what op-geth defines,
	// and that it is distinct from the deposit selector.
	require.Equal(t, params.SystemAddress, sysTransfer.Address)
	require.Len(t, sysTransfer.Topics, 3)
	require.Equal(t, params.EthTransferLogEvent, sysTransfer.Topics[0])
	require.NotEqual(t, DepositEventABIHash, params.EthTransferLogEvent,
		"deposit selector must differ from the EIP-7708 Transfer selector")

	receipts := []*types.Receipt{
		{
			Type:             types.DynamicFeeTxType,
			Status:           types.ReceiptStatusSuccessful,
			Logs:             []*types.Log{sysTransfer, depLog, advTransfer},
			BlockHash:        blockHash,
			TransactionIndex: 0,
		},
	}

	got, err := UserDeposits(receipts, MockDepositContractAddr)
	require.NoError(t, err, "7708 Transfer logs must be filtered out, not parsed as deposits")
	require.Len(t, got, 1, "exactly the one real deposit must be derived")
	require.Equal(t, realDep, got[0], "derived deposit fields must match the real deposit")
}

// TestUnmarshalDepositLogEvent_Rejects7708Shape pins the lower-level guard: even if
// a 7708 Transfer log were somehow handed straight to the deposit decoder, it is
// rejected because it carries 3 topics, not the 4 a TransactionDeposited event has
// (deposit_log.go:37-39). This is the invariant that makes the address/topic0 filter
// in UserDeposits fail-safe rather than merely fail-open-with-garbage.
func TestUnmarshalDepositLogEvent_Rejects7708Shape(t *testing.T) {
	transfer := types.EthTransferLog(
		common.BigToAddress(big.NewInt(0x1111)),
		common.BigToAddress(big.NewInt(0x2222)),
		uint256.NewInt(42),
	)
	_, err := UnmarshalDepositLogEvent(transfer)
	require.Error(t, err, "a 3-topic 7708 Transfer log must not decode as a 4-topic deposit")
}
