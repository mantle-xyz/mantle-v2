package consistency

import (
	"math/big"
	"testing"
	"time"

	"github.com/ethereum-optimism/optimism/op-acceptance-tests/mantle-tests/elysium/internal/testhelpers"
	"github.com/ethereum-optimism/optimism/op-devstack/devtest"
	"github.com/ethereum-optimism/optimism/op-devstack/presets"
	"github.com/ethereum-optimism/optimism/op-service/eth"
)

// TestDeposit_SeqVerifierConsistency_AcrossL1Upgrade checks that sequencer and
// verifier agree after a deposit derived while L1 runs Glamsterdam. Balance
// crediting proves both nodes see the deposit; matching safe hashes prove both
// derivation pipelines converge on the same post-upgrade L1 data.
func TestDeposit_SeqVerifierConsistency_AcrossL1Upgrade(gt *testing.T) {
	t := devtest.SerialT(gt)
	sys := presets.NewMantleSingleChainMultiNode(t)
	require := t.Require()

	// Wait for the L1 to activate Amsterdam so the deposit is derived on both nodes while
	// the L1 genuinely runs Glamsterdam.
	l1Config := sys.L1Network.Escape().ChainConfig()
	require.NotNil(l1Config.AmsterdamTime, "L1 AmsterdamTime must be configured")
	testhelpers.WaitForGlamsterdamL1(t, sys.L1EL, *l1Config.AmsterdamTime)

	// Bridge MNT from the Glamsterdam L1 to L2 (same flow as the single-node deposit test).
	depositAmount := eth.GWei(1_000_000)
	l1User := sys.FunderL1.NewFundedEOA(eth.OneTenthEther)
	l2UserSeq := l1User.AsEL(sys.L2EL)
	l2UserVerifier := l1User.AsEL(sys.L2ELB)
	testhelpers.FundL1MNT(t, &sys.MantleMinimal, l1User, depositAmount)

	l1MNTAddr := sys.L2Chain.Escape().Deployment().L1MNTAddr()
	l1BridgeAddr := sys.L2Chain.Escape().Deployment().L1StandardBridgeProxyAddr()
	l1User.ApproveToken(l1MNTAddr, l1BridgeAddr, depositAmount)

	expected := l2UserSeq.GetBalance().Add(depositAmount)

	bridge := sys.MantleBridge()
	bridge.DepositMNT(depositAmount, l1User)

	// Both nodes must settle on the credited balance.
	l2UserSeq.WaitForBalance(expected)
	l2UserVerifier.WaitForBalance(expected)

	// Compare only after the safe head anchors to a post-Amsterdam L1 origin.
	require.Eventually(func() bool {
		safe := sys.L2EL.BlockRefByLabel(eth.Safe)
		if safe.Number == 0 {
			return false
		}
		origin := sys.L1EL.BlockRefByNumber(safe.L1Origin.Number)
		return l1Config.IsAmsterdam(new(big.Int).SetUint64(origin.Number), origin.Time)
	}, 90*time.Second, time.Second, "sequencer safe head must anchor to a post-Amsterdam L1 origin")

	// Capture that safe head and confirm its L1 origin is post-Amsterdam (safe heads only
	// advance, so a fresh read stays past the activation this gate just cleared).
	seqSafe := sys.L2EL.BlockRefByLabel(eth.Safe)
	seqSafeOrigin := sys.L1EL.BlockRefByNumber(seqSafe.L1Origin.Number)
	require.True(l1Config.IsAmsterdam(new(big.Int).SetUint64(seqSafeOrigin.Number), seqSafeOrigin.Time),
		"compared safe L2 block %d must derive from a post-Amsterdam L1 origin (L1 block %d)",
		seqSafe.Number, seqSafeOrigin.Number)

	// Let the verifier's own derivation reach that height, then assert the two safe blocks are
	// byte-identical — equal hashes prove both derivation pipelines produced the same block from
	// the same Glamsterdam L1 data, not mere unsafe gossip.
	require.Eventually(func() bool {
		return sys.L2ELB.BlockRefByLabel(eth.Safe).Number >= seqSafe.Number
	}, 90*time.Second, time.Second, "verifier must derive up to the sequencer's safe head")

	require.Equal(seqSafe.Hash, sys.L2ELB.BlockRefByNumber(seqSafe.Number).Hash,
		"sequencer and verifier must derive an identical safe L2 block at height %d", seqSafe.Number)
}
