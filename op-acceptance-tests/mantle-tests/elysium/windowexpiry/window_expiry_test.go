package windowexpiry

import (
	"testing"
	"time"

	"github.com/ethereum-optimism/optimism/op-chain-ops/devkeys"
	"github.com/ethereum-optimism/optimism/op-devstack/devtest"
	"github.com/ethereum-optimism/optimism/op-devstack/dsl"
	"github.com/ethereum-optimism/optimism/op-devstack/presets"
	"github.com/ethereum-optimism/optimism/op-devstack/stack"
	"github.com/ethereum-optimism/optimism/op-devstack/stack/match"
	"github.com/ethereum-optimism/optimism/op-service/apis"
	"github.com/ethereum-optimism/optimism/op-service/eth"
	"github.com/ethereum-optimism/optimism/op-service/txplan"
	"github.com/ethereum-optimism/optimism/op-supervisor/supervisor/types"
	"github.com/ethereum-optimism/optimism/op-test-sequencer/sequencer/seqtypes"
	"github.com/ethereum/go-ethereum/common"
)

const l2FaucetFunderUserKey = devkeys.UserKey(10_000)

// TestVerifierReorgsAfterSequencingWindowExpiry exercises the op-node recovery
// path where batch data is missing long enough for the sequencing window to
// expire: the sequencer reorgs out an old unbatched unsafe block, and the
// verifier must follow that reorg while it stays online and P2P-connected.
//
// This is a general op-node / derivation property that is independent of the L1
// fork — it holds under any L1. The test runs with the L1 on Glamsterdam so the
// behavior is confirmed to survive in that environment, but the Glamsterdam L1
// is the environment, not the discriminator: none of the assertions below are
// L1-fork-sensitive.
func TestVerifierReorgsAfterSequencingWindowExpiry(gt *testing.T) {
	t := devtest.SerialT(gt)
	sys := presets.NewMantleSingleChainMultiNodeWithTestSeq(t)
	require := t.Require()
	logger := t.Logger()
	ctx := t.Ctx()

	cl := sys.L1Network.Escape().L1CLNode(match.FirstL1CL)
	sequenceL1BlockAndWait(t, sys)
	sys.L2ELB.Matched(sys.L2EL, types.LocalUnsafe, 30)
	sys.L2ELB.Matched(sys.L2EL, types.LocalSafe, 30)

	sender := prefundedL2FaucetFunder(t, sys)
	recipient := sys.Wallet.NewEOA(sys.L2EL)

	sequencerStopped := false
	batcherStopped := false
	fakePoSStopped := false
	t.Cleanup(func() {
		_ = sys.L2CL.Escape().RollupAPI().SetRecoverMode(ctx, false)
		if batcherStopped {
			sys.L2Batcher.Start()
		}
		if fakePoSStopped {
			sys.ControlPlane.FakePoSState(cl.ID(), stack.Start)
		}
		if sequencerStopped {
			sys.L2CL.StartSequencer()
		}
	})

	l2Control := sys.TestSequencer.Escape().ControlAPI(sys.L2EL.ChainID())
	stoppedSequencerHead := sys.L2CL.StopSequencer()
	sequencerStopped = true
	logger.Info("Stopped sequencer to inject an unbatched unsafe block", "head", stoppedSequencerHead)

	sys.L2EL.Reached(eth.Unsafe, sys.L2CL.HeadBlockRef(types.LocalUnsafe).Number, 10)
	parentRef := sys.L2EL.BlockRefByLabel(eth.Unsafe)

	sys.ControlPlane.FakePoSState(cl.ID(), stack.Stop)
	fakePoSStopped = true
	sys.L2Batcher.Stop()
	batcherStopped = true

	parent := parentRef.Hash
	l1Origin := sys.L1EL.BlockRefByLabel(eth.Unsafe).Hash
	sequenceBlockWithL1Origin(t, l2Control, parent, l1Origin, recipient, sender, 0)

	oldUnsafe := sys.L2EL.BlockRefByLabel(eth.Unsafe)
	require.Equal(parent, oldUnsafe.ParentHash)
	logger.Info("Injected unsafe block that should be reorged out after window expiry", "oldUnsafe", oldUnsafe)

	sys.L2ELB.Matched(sys.L2EL, types.LocalUnsafe, 30)
	verifierOldUnsafe := sys.L2ELB.BlockRefByNumber(oldUnsafe.Number)
	require.Equal(oldUnsafe.Hash, verifierOldUnsafe.Hash, "verifier must first import the old unsafe branch")

	sys.L2CL.StartSequencer()
	sequencerStopped = false
	require.NoError(sys.L2CL.Escape().RollupAPI().SetRecoverMode(ctx, true))

	// Advance enough L1 blocks to expire the sequencing window for the missing
	// batch data, while also advancing L2 time so the sequencer can build the
	// recovery chain. This is the point where the verifier must not get stuck.
	for i := uint64(0); i < sequencerWindowSize+3; i++ {
		sequenceL1BlockAndWait(t, sys)
		require.Eventually(func() bool {
			sys.AdvanceTime(2 * time.Second)
			l1Head := sys.L1EL.BlockRefByLabel(eth.Unsafe).Number
			l2Origin := sys.L2EL.BlockRefByLabel(eth.Unsafe).L1Origin.Number
			logger.Info("window-expiry progress", "l1", l1Head, "l2Origin", l2Origin, "oldUnsafe", oldUnsafe.Number)
			return l2Origin+2 >= l1Head
		}, 60*time.Second, 2*time.Second)
	}

	newSequencerBlock := waitBlockReorged(t, sys.L2EL, oldUnsafe, "sequencer")

	// This is the main regression assertion: the verifier is not restarted, not
	// manually reset, and P2P remains connected. It must follow the sequencer reorg.
	newVerifierBlock := waitBlockReorged(t, sys.L2ELB, oldUnsafe, "verifier")
	require.Equal(newSequencerBlock.Hash, newVerifierBlock.Hash, "verifier must canonicalize the same replacement block")
	require.NotEqual(oldUnsafe.Hash, newVerifierBlock.Hash, "verifier must not remain on the old unsafe branch")

	waitVerifierPastReorg(t, sys, oldUnsafe)
	waitVerifierConvergesWithSequencer(t, sys, sys.L2EL.BlockRefByLabel(eth.Unsafe))
}

func waitBlockReorged(t devtest.T, el *dsl.L2ELNode, old eth.L2BlockRef, name string) eth.L2BlockRef {
	require := t.Require()
	logger := t.Logger()
	var replacement eth.L2BlockRef

	require.Eventually(func() bool {
		ref, err := el.Escape().L2EthClient().L2BlockRefByNumber(t.Ctx(), old.Number)
		if err != nil {
			logger.Info("waiting for replacement block", "node", name, "number", old.Number, "err", err)
			return false
		}
		replacement = ref
		oldCanonical := el.IsCanonical(old.ID())
		logger.Info("checking block reorg",
			"node", name,
			"old", old,
			"replacement", replacement,
			"oldCanonical", oldCanonical)
		return !oldCanonical && replacement.Hash != old.Hash
	}, 120*time.Second, time.Second, name+" must reorg out the unbatched unsafe block")

	return replacement
}

func waitVerifierPastReorg(t devtest.T, sys *presets.MantleSingleChainMultiNodeWithTestSeq, old eth.L2BlockRef) {
	require := t.Require()
	logger := t.Logger()

	require.Eventually(func() bool {
		status := sys.L2CLB.SyncStatus()
		logger.Info("verifier recovery status",
			"oldUnsafe", old,
			"safe", status.SafeL2,
			"unsafe", status.UnsafeL2)
		return status.SafeL2.Number >= old.Number && status.UnsafeL2.Number >= old.Number
	}, 60*time.Second, time.Second, "verifier safe and unsafe heads must advance past the reorged block")
}

func waitVerifierConvergesWithSequencer(t devtest.T, sys *presets.MantleSingleChainMultiNodeWithTestSeq, target eth.L2BlockRef) {
	require := t.Require()
	logger := t.Logger()

	require.Eventually(func() bool {
		seqHead := sys.L2EL.BlockRefByLabel(eth.Unsafe)
		verHead := sys.L2ELB.BlockRefByLabel(eth.Unsafe)
		targetOnVerifier, err := sys.L2ELB.Escape().L2EthClient().L2BlockRefByNumber(t.Ctx(), target.Number)
		logger.Info("verifier convergence status",
			"target", target,
			"seqHead", seqHead,
			"verHead", verHead,
			"targetOnVerifier", targetOnVerifier,
			"err", err)
		if err != nil {
			return false
		}
		return verHead.Number >= target.Number && targetOnVerifier.Hash == target.Hash
	}, 120*time.Second, time.Second, "verifier must import the sequencer recovery head")

	sys.L2ELB.Matched(sys.L2EL, types.LocalUnsafe, 60)
}

func prefundedL2FaucetFunder(t devtest.T, sys *presets.MantleSingleChainMultiNodeWithTestSeq) *dsl.EOA {
	priv := sys.L2Chain.Escape().Keys().Secret(l2FaucetFunderUserKey)
	eoa := dsl.NewKey(t, priv).User(sys.L2EL)
	t.Require().Greater(eoa.GetBalance().ToBig().Sign(), 0, "prefunded L2 faucet funder must have genesis balance")
	return eoa
}

func sequenceL1BlockAndWait(t devtest.T, sys *presets.MantleSingleChainMultiNodeWithTestSeq) {
	prev := sys.L1EL.BlockRefByLabel(eth.Unsafe).Number
	sys.TestSequencer.SequenceBlock(t, sys.L1Network.ChainID(), common.Hash{})
	t.Require().Eventually(func() bool {
		return sys.L1EL.BlockRefByLabel(eth.Unsafe).Number > prev
	}, 30*time.Second, 200*time.Millisecond, "L1 head must advance after manual sequencing")
}

func sequenceBlockWithL1Origin(t devtest.T, ts apis.TestSequencerControlAPI, parent common.Hash, l1Origin common.Hash, alice *dsl.EOA, cathrine *dsl.EOA, nonce uint64) {
	require := t.Require()
	require.NoError(ts.New(t.Ctx(), seqtypes.BuildOpts{Parent: parent, L1Origin: &l1Origin}))

	to := cathrine.PlanTransfer(alice.Address(), eth.OneWei)
	opt := txplan.Combine(to, txplan.WithStaticNonce(nonce))
	ptx := txplan.NewPlannedTx(opt)
	signedTx, err := ptx.Signed.Eval(t.Ctx())
	require.NoError(err)
	txData, err := signedTx.MarshalBinary()
	require.NoError(err)
	require.NoError(ts.IncludeTx(t.Ctx(), txData))

	require.NoError(ts.Next(t.Ctx()))
}
