package deposit

import (
	"testing"

	"github.com/ethereum-optimism/optimism/op-devstack/devtest"
	"github.com/ethereum-optimism/optimism/op-devstack/presets"
	"github.com/ethereum-optimism/optimism/op-service/eth"
)

// TestDeposit_AcrossL1Upgrade asserts that an L1->L2 deposit initiated while
// the L1 runs Glamsterdam (Amsterdam EL) is still credited on the Mantle L2. The L2
// stays on Mantle Arsia rules across the boundary and only *consumes* the upgraded
// L1, so a native MNT gas-token deposit must land as a plain L2 native-balance
// increase for the depositor.
func TestDeposit_AcrossL1Upgrade(gt *testing.T) {
	t := devtest.SerialT(gt)
	sys := presets.NewMantleMinimal(t)
	require := t.Require()

	// Wait for the L1 to activate Amsterdam, so the deposit below is submitted and
	// derived while the L1 genuinely runs Glamsterdam.
	l1Config := sys.L1Network.Escape().ChainConfig()
	require.NotNil(l1Config.AmsterdamTime, "L1 AmsterdamTime must be configured")
	sys.L1EL.WaitForTime(*l1Config.AmsterdamTime)

	// The depositor is an L1 EOA that holds native ETH for gas and L1 MNT to bridge.
	// The same key addresses the depositor's L2 native (MNT) balance.
	depositAmount := eth.GWei(1_000_000)
	l1User := sys.FunderL1.NewFundedEOA(eth.OneTenthEther)
	l2User := l1User.AsEL(sys.L2EL)
	fundL1MNT(t, sys, l1User, depositAmount)

	// MantleBridge.DepositMNT pulls MNT via the L1 StandardBridge, so the depositor
	// must approve it first.
	l1MNTAddr := sys.L2Chain.Escape().Deployment().L1MNTAddr()
	l1BridgeAddr := sys.L2Chain.Escape().Deployment().L1StandardBridgeProxyAddr()
	l1User.ApproveToken(l1MNTAddr, l1BridgeAddr, depositAmount)

	// Record the depositor's L2 native (MNT gas-token) balance before the deposit.
	initialL2 := l2User.GetBalance()

	// Bridge MNT from the Glamsterdam L1 to L2. The DSL Deposit blocks until the L2
	// deposit tx is included and asserts its L2 receipt succeeded.
	bridge := sys.MantleBridge()
	bridge.DepositMNT(depositAmount, l1User)

	// The deposited MNT must be credited to the depositor on L2, i.e. the native
	// balance increases by exactly the deposited amount.
	l2User.WaitForBalance(initialL2.Add(depositAmount))
}
