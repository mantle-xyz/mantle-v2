package ethtransferlog

import (
	"testing"

	"github.com/ethereum-optimism/optimism/op-devstack/devtest"
	"github.com/ethereum-optimism/optimism/op-devstack/presets"
	"github.com/ethereum-optimism/optimism/op-service/eth"
	"github.com/ethereum-optimism/optimism/op-service/txplan"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/params"
)

// TestL2EVM_NoEIP7708TransferLog is an L2-fork isolation test: the Mantle L2,
// which runs Arsia at genesis with AmsterdamTime left nil, must NOT emit an
// EIP-7708 system Transfer log for an ordinary ETH value transfer. This is a
// purely L2-internal fork property — the EIP-7708 log gate reads the L2's own
// chain rules, so the L1 fork state cannot influence it. The L1 running
// Glamsterdam (Amsterdam EL) is the environment these tests execute in, not the
// discriminator; no assertion here is L1-sensitive.
//
// MECHANISM. EIP-7708's log is gated on rules.IsAmsterdam in op-geth Transfer():
//
//	op-geth core/evm.go:151-152
//	    if rules.IsAmsterdam && !amount.IsZero() && sender != recipient {
//	        db.AddLog(types.EthTransferLog(sender, recipient, amount))
//
// rules.IsAmsterdam is derived from the L2 chain config, which sets MantleArsiaTime
// (params/config.go:522, IsMantleArsia) but leaves AmsterdamTime nil (params/config.go:497,
// IsAmsterdam) — the two fork gates are INDEPENDENT (params/config.go:1010-1011 vs
// 1082-1083). So on the Arsia L2, rules.IsAmsterdam == false and the value transfer emits
// NO system log, whereas an L2 that had (wrongly) adopted Amsterdam would emit exactly one
// Transfer log with:
//   - Address    = params.SystemAddress (0xffff...fffe)          (log.go:73)
//   - Topics[0]  = params.EthTransferLogEvent
//     = keccak256("Transfer(address,address,uint256)")
//     = 0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef  (log.go:74-75, protocol_params.go:243)
//
// DISCRIMINATION. A plain value transfer to a code-less EOA runs no contract code,
// so on Arsia its receipt carries ZERO logs. The Amsterdam value would be ONE 7708
// Transfer log at the system address. The assertion below scans the receipt for any
// log bearing that exact (address, topic0) signature and requires none — it flips
// red the instant the L2 starts producing Amsterdam 7708 logs.
func TestL2EVM_NoEIP7708TransferLog(gt *testing.T) {
	t := devtest.SerialT(gt)
	sys := presets.NewMantleMinimal(t)
	require := t.Require()

	wallet := sys.FunderL2.NewFundedEOA(eth.OneEther)

	// A code-less EOA recipient (well above the precompile range, not a Mantle
	// 0x42.. predeploy): a value transfer to it runs no code, so any log in the
	// receipt could only be a protocol-level system log such as EIP-7708's.
	recipient := common.HexToAddress("0x00000000000000000000000000000000C0FFEE07")

	receipt, err := txplan.NewPlannedTx(txplan.Combine(
		wallet.Plan(),
		txplan.WithTo(&recipient),
		txplan.WithValue(eth.HalfEther),
		txplan.WithGasLimit(1_000_000),
	)).Included.Eval(t.Ctx())
	require.NoError(err)
	require.Equal(types.ReceiptStatusSuccessful, receipt.Status, "value transfer must succeed")

	for _, lg := range receipt.Logs {
		if len(lg.Topics) == 0 {
			continue
		}
		is7708 := lg.Address == params.SystemAddress && lg.Topics[0] == params.EthTransferLogEvent
		require.Falsef(is7708,
			"L2 must NOT emit an EIP-7708 system Transfer log (addr=%s topic0=%s); the L2 stays on Arsia, not Amsterdam",
			lg.Address, lg.Topics[0])
	}

	// Strongest form: an ordinary Arsia ETH transfer to an EOA produces no logs at all.
	require.Empty(receipt.Logs,
		"an Arsia ETH transfer to a code-less EOA emits zero logs; any log here signals Amsterdam/EIP-7708 adoption")

	t.Log("L2 Arsia ETH transfer emitted no EIP-7708 system Transfer log",
		"logCount", len(receipt.Logs),
		"amsterdamTopic0", params.EthTransferLogEvent,
		"systemAddress", params.SystemAddress)
}
