package ethtransferlog

import (
	"testing"

	"github.com/ethereum-optimism/optimism/op-acceptance-tests/mantle-tests/elysium/internal/testhelpers"
	"github.com/ethereum-optimism/optimism/op-devstack/devtest"
	"github.com/ethereum-optimism/optimism/op-devstack/presets"
	"github.com/ethereum-optimism/optimism/op-service/eth"
	"github.com/ethereum-optimism/optimism/op-service/txplan"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/params"
)

// TestExternal_NoEIP7708Log verifies the Mantle L2 stays on Arsia log rules while
// consuming a Glamsterdam L1. An ordinary EOA value transfer should emit no
// EIP-7708 system Transfer log because that log is gated by the L2 Amsterdam fork.
//
// The assertion scans for the exact system-address/topic signature and also
// requires the EOA transfer receipt to have zero logs, so an accidental L2
// Amsterdam adoption fails immediately.
func TestExternal_NoEIP7708Log(gt *testing.T) {
	t := devtest.SerialT(gt)
	sys := presets.NewMantleMinimal(t)
	require := t.Require()

	// Establish the Glamsterdam L1 environment; the 7708 discriminator itself is
	// still the L2 fork gate.
	l1Config := sys.L1Network.Escape().ChainConfig()
	require.NotNil(l1Config.AmsterdamTime, "L1 AmsterdamTime must be configured")
	testhelpers.WaitForGlamsterdamL1(t, sys.L1EL, *l1Config.AmsterdamTime)

	wallet := sys.FunderL2.NewFundedEOA(eth.OneEther)

	// A code-less EOA recipient makes any receipt log protocol-generated.
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
