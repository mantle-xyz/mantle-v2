package consistency

import (
	"testing"
	"time"

	"github.com/ethereum-optimism/optimism/op-devstack/devtest"
	"github.com/ethereum-optimism/optimism/op-devstack/presets"
	"github.com/ethereum-optimism/optimism/op-service/eth"
)

// TestDeposit_SeqVerifierConsistency_AcrossL1Upgrade closes the "sequencer 与 verifier
// 对同一 L1 块还原出一致的 deposit" half that the single-node deposit/derivation
// tests structurally cannot cover: it runs an independent verifier node alongside the
// sequencer and asserts BOTH derive the same L2 state — including an L1->L2 deposit
// submitted while the L1 runs Glamsterdam (Amsterdam).
//
// Two independent facts are asserted:
//  1. Both the sequencer and the verifier credit the deposited MNT to the depositor's
//     L2 native balance (WaitForBalance on each node's EL).
//  2. The two nodes' SAFE chains — which each node derives from L1 independently, not by
//     gossiping unsafe blocks — are byte-identical at a common height (equal block hash).
func TestDeposit_SeqVerifierConsistency_AcrossL1Upgrade(gt *testing.T) {
	t := devtest.SerialT(gt)
	sys := presets.NewMantleSingleChainMultiNode(t)
	require := t.Require()

	// Wait for the L1 to activate Amsterdam so the deposit is derived on both nodes while
	// the L1 genuinely runs Glamsterdam.
	l1Config := sys.L1Network.Escape().ChainConfig()
	require.NotNil(l1Config.AmsterdamTime, "L1 AmsterdamTime must be configured")
	sys.L1EL.WaitForTime(*l1Config.AmsterdamTime)

	// Bridge MNT from the Glamsterdam L1 to L2 (same flow as the single-node deposit test).
	depositAmount := eth.GWei(1_000_000)
	l1User := sys.FunderL1.NewFundedEOA(eth.OneTenthEther)
	l2UserSeq := l1User.AsEL(sys.L2EL)
	l2UserVerifier := l1User.AsEL(sys.L2ELB)
	fundL1MNT(t, &sys.MantleMinimal, l1User, depositAmount)

	l1MNTAddr := sys.L2Chain.Escape().Deployment().L1MNTAddr()
	l1BridgeAddr := sys.L2Chain.Escape().Deployment().L1StandardBridgeProxyAddr()
	l1User.ApproveToken(l1MNTAddr, l1BridgeAddr, depositAmount)

	expected := l2UserSeq.GetBalance().Add(depositAmount)

	bridge := sys.MantleBridge()
	bridge.DepositMNT(depositAmount, l1User)

	// (1) The sequencer must credit the deposit, and the independent verifier must
	// INDEPENDENTLY reach the same credited balance.
	l2UserSeq.WaitForBalance(expected)
	l2UserVerifier.WaitForBalance(expected)

	// (2) Both nodes derive the SAFE chain from L1 independently. Wait until the verifier's
	// safe head reaches the sequencer's, then assert the two safe chains are identical at
	// that height — equal hashes prove consistent derivation, not mere unsafe propagation.
	seqSafe := sys.L2EL.BlockRefByLabel(eth.Safe)
	require.Greater(seqSafe.Number, uint64(0), "sequencer must have a safe head")
	require.Eventually(func() bool {
		return sys.L2ELB.BlockRefByLabel(eth.Safe).Number >= seqSafe.Number
	}, 90*time.Second, time.Second, "verifier must derive up to the sequencer's safe head")

	require.Equal(seqSafe.Hash, sys.L2ELB.BlockRefByNumber(seqSafe.Number).Hash,
		"sequencer and verifier must derive an identical safe L2 block at height %d", seqSafe.Number)

	// Sanity: the whole run is genuinely past the L1 Amsterdam activation.
	require.Greater(sys.L1EL.BlockRefByLabel(eth.Unsafe).Number, amsterdamOffset,
		"L1 must be past the Amsterdam activation offset")
}
