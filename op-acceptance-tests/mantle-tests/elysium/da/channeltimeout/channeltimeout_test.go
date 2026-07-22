package channeltimeout

import (
	"bytes"
	"io"
	"math/big"
	"testing"
	"time"

	"github.com/ethereum-optimism/optimism/op-chain-ops/devkeys"
	opforks "github.com/ethereum-optimism/optimism/op-core/forks"
	"github.com/ethereum-optimism/optimism/op-devstack/devtest"
	"github.com/ethereum-optimism/optimism/op-devstack/dsl"
	"github.com/ethereum-optimism/optimism/op-devstack/presets"
	"github.com/ethereum-optimism/optimism/op-devstack/stack"
	"github.com/ethereum-optimism/optimism/op-devstack/stack/match"
	"github.com/ethereum-optimism/optimism/op-node/rollup"
	"github.com/ethereum-optimism/optimism/op-node/rollup/derive"
	derive_params "github.com/ethereum-optimism/optimism/op-node/rollup/derive/params"
	"github.com/ethereum-optimism/optimism/op-service/eth"
	"github.com/ethereum-optimism/optimism/op-service/txplan"
	suptypes "github.com/ethereum-optimism/optimism/op-supervisor/supervisor/types"
	"github.com/ethereum-optimism/optimism/op-test-sequencer/sequencer/seqtypes"
	"github.com/ethereum/go-ethereum/common"
	gethtypes "github.com/ethereum/go-ethereum/core/types"
)

// channelTargetSize is the compressor's target output size — large enough that the handful of
// L2 blocks this test batches always fit in one channel, so the only thing controlling how many
// frames come out is the per-frame cap chosen in buildChannel.
const channelTargetSize = uint64(100_000)

// TestDerivation_ChannelTimeout_PostUpgrade pins channel-timeout behaviour across the L1
// Glamsterdam boundary. The test stops the real batcher, posts frames from the batcher's key, and
// advances L1 one block at a time so every frame lands at a known height.
//
// Two otherwise-identical channels discriminate the deadline: one has its final frame exactly at
// open+timeout and must become safe by hash; the other is one L1 block late and must not become
// safe. This covers the pipeline behaviour directly instead of re-checking the ChannelTimeout
// constant.
func TestDerivation_ChannelTimeout_PostUpgrade(gt *testing.T) {
	t := devtest.SerialT(gt)
	sys := presets.NewMantleSingleChainMultiNodeWithTestSeq(t)
	require := t.Require()
	logger := t.Logger()
	ctx := t.Ctx()

	rollupCfg := sys.L2Chain.Escape().RollupConfig()
	l1Config := sys.L1Network.Escape().ChainConfig()
	require.True(sys.L2Chain.IsMantleForkActive(opforks.MantleElysium), "L2 must run with Mantle Elysium active")
	require.NotNil(l1Config.AmsterdamTime, "L1 AmsterdamTime must be configured")

	chainSpec := rollup.NewChainSpec(rollupCfg)
	// Read the deadline op-node itself will apply, rather than hardcoding 50 here.
	channelTimeout := chainSpec.ChannelTimeout(*l1Config.AmsterdamTime)
	require.Greater(channelTimeout, uint64(1), "channel timeout must be a usable number of L1 blocks")
	logger.Info("channel timeout in effect", "blocks", channelTimeout)

	sys.L1Network.WaitForBlock()

	// Stop the real batcher: from here, every byte of batch data on L1 is posted by this test,
	// so an L2 block can only become safe through a channel we placed ourselves.
	sys.L2Batcher.Stop()
	t.Cleanup(func() { sys.L2Batcher.Start() })

	// Take manual control of L1 production so each frame lands in an exact L1 block.
	cl := sys.L1Network.Escape().L1CLNode(match.FirstL1CL)
	sys.ControlPlane.FakePoSState(cl.ID(), stack.Stop)
	t.Cleanup(func() { sys.ControlPlane.FakePoSState(cl.ID(), stack.Start) })

	ts := sys.TestSequencer.Escape().ControlAPI(sys.L1Network.ChainID())
	produceL1Block := func() {
		require.NoError(ts.New(ctx, seqtypes.BuildOpts{Parent: common.Hash{}}))
		require.NoError(ts.Next(ctx))
	}
	// driveL1To produces L1 blocks until the head reaches target, nudging the L2 clock so the
	// sequencer keeps building and its L1 origin follows along.
	driveL1To := func(target uint64) {
		for sys.L1EL.BlockRefByLabel(eth.Unsafe).Number < target {
			produceL1Block()
			sys.AdvanceTime(time.Duration(l1BlockTime) * time.Second)
		}
	}

	// The batch inbox only accepts data from the configured batcher address, so post as it.
	batcherPriv := sys.L2Chain.Escape().Keys().Secret(devkeys.BatcherRole.Key(rollupCfg.L2ChainID))
	batcherEOA := dsl.NewKey(t, batcherPriv).User(sys.L1EL)
	inbox := rollupCfg.BatchInboxAddress

	// l2Block reconstructs a full block from the L2 EL: the channel encoder needs the
	// transactions and the L1-info deposit, both carried here.
	l2Block := func(n uint64) *gethtypes.Block {
		info, txs, err := sys.L2EL.Escape().EthClient().InfoAndTxsByNumber(ctx, n)
		require.NoErrorf(err, "read L2 block %d", n)
		return gethtypes.NewBlockWithHeader(info.Header()).WithBody(gethtypes.Body{Transactions: txs})
	}

	// encodeChannel encodes L2 blocks [from,to] into a closed channel and returns its frames,
	// each capped at frameSize. This is the batcher's own encoder (span batch + zlib), so the
	// bytes are exactly what a real batcher would post — only their L1 placement is under test.
	encodeChannel := func(from, to uint64, frameSize uint64) [][]byte {
		ch, err := derive.NewSpanChannelOut(channelTargetSize, derive.Zlib, chainSpec)
		require.NoError(err, "create channel")
		for n := from; n <= to; n++ {
			_, err := ch.AddBlock(rollupCfg, l2Block(n))
			require.NoErrorf(err, "buffer L2 block %d into the channel", n)
		}
		require.NoError(ch.Close(), "close channel")

		var frames [][]byte
		for {
			buf := new(bytes.Buffer)
			_, err := ch.OutputFrame(buf, frameSize)
			frames = append(frames, buf.Bytes())
			if err == io.EOF {
				break
			}
			require.NoError(err, "emit channel frame")
		}
		return frames
	}

	// buildChannel splits a channel into exactly two frames: it first encodes the blocks
	// whole to measure the payload, then re-encodes with a frame cap of half that size. The
	// blocks carry little more than their L1-info deposit, so a fixed frame cap would leave
	// the whole channel in a single frame — and a single frame can only ever land in one L1
	// block, which is precisely what this test must avoid.
	buildChannel := func(from, to uint64) [][]byte {
		whole := encodeChannel(from, to, channelTargetSize)
		require.Lenf(whole, 1, "measuring pass must yield one frame (got %d)", len(whole))
		dataLen := uint64(len(whole[0])) - derive.FrameV0OverHeadSize
		require.Greaterf(dataLen, uint64(1), "channel payload must be splittable (got %d bytes)", dataLen)

		frames := encodeChannel(from, to, derive.FrameV0OverHeadSize+(dataLen+1)/2)
		require.GreaterOrEqualf(len(frames), 2,
			"need a channel of at least 2 frames to spread across L1 blocks (got %d, payload %d bytes)", len(frames), dataLen)
		return frames
	}

	// postFrames submits one inbox tx carrying the given frames and mines exactly one L1 block
	// to include it, returning that block's number.
	postFrames := func(what string, frames ...[]byte) uint64 {
		payload := []byte{derive_params.DerivationVersion0}
		for _, f := range frames {
			payload = append(payload, f...)
		}
		_, err := txplan.NewPlannedTx(txplan.Combine(
			batcherEOA.Plan(),
			txplan.WithTo(&inbox),
			txplan.WithData(payload),
			txplan.WithGasLimit(1_000_000),
		)).Submitted.Eval(ctx)
		require.NoErrorf(err, "submit %s inbox tx", what)
		produceL1Block()
		n := sys.L1EL.BlockRefByLabel(eth.Unsafe).Number
		logger.Info("posted channel frames", "what", what, "l1Block", n, "frames", len(frames), "bytes", len(payload))
		return n
	}

	isAmsterdam := func(n uint64) bool {
		ref := sys.L1EL.BlockRefByNumber(n)
		return l1Config.IsAmsterdam(new(big.Int).SetUint64(n), ref.Time)
	}

	// nextUnsafeRange returns a contiguous range of L2 blocks above the current safe head that
	// the channel will carry. They are unsafe-only: with the batcher stopped, nothing else can
	// make them safe, so "did they become safe" is a clean verdict on our channel alone.
	const blocksPerChannel = uint64(3)
	nextUnsafeRange := func(what string) (from, to uint64) {
		safe := sys.L2CL.SyncStatus().SafeL2.Number
		from = safe + 1
		to = from + blocksPerChannel - 1
		require.Eventually(func() bool {
			sys.AdvanceTime(2 * time.Second)
			return sys.L2EL.BlockRefByLabel(eth.Unsafe).Number >= to
		}, 60*time.Second, 250*time.Millisecond, "L2 must build the blocks "+what+" will carry")
		return from, to
	}

	// ---- CHANNEL A: opens PRE-Amsterdam, closes at exactly open+timeout POST-Amsterdam ----

	amsterdamL1Block := amsterdamOffset / l1BlockTime
	require.False(isAmsterdam(sys.L1EL.BlockRefByLabel(eth.Unsafe).Number),
		"channel A must open while the L1 is still pre-Amsterdam")

	fromA, toA := nextUnsafeRange("channel A")
	refA := sys.L2EL.BlockRefByNumber(toA)
	framesA := buildChannel(fromA, toA)

	openA := postFrames("channel A first frame", framesA[0])
	require.Falsef(isAmsterdam(openA), "channel A must open on a PRE-Amsterdam L1 block (opened at %d)", openA)

	closeTargetA := openA + channelTimeout
	require.Greaterf(closeTargetA, amsterdamL1Block,
		"the Amsterdam activation (L1 #%d) must fall inside channel A's window (%d..%d)", amsterdamL1Block, openA, closeTargetA)

	// Drive to one block short of the deadline, then post the rest so they land exactly on it.
	driveL1To(closeTargetA - 1)
	closedA := postFrames("channel A remaining frames", framesA[1:]...)
	require.Equalf(closeTargetA, closedA, "channel A's last frame must land exactly on the deadline block %d", closeTargetA)
	require.Truef(isAmsterdam(closedA), "channel A must close on a POST-Amsterdam L1 block (closed at %d)", closedA)
	logger.Info("channel A spans the Glamsterdam boundary at the exact deadline",
		"open", openA, "close", closedA, "amsterdamAt", amsterdamL1Block, "timeout", channelTimeout)

	// A channel closed exactly ON the deadline is still in time: its blocks must become safe.
	// Matching the HASH proves those exact L2 blocks were reconstructed from these frames.
	sys.L2CL.ReachedRef(suptypes.CrossSafe, eth.BlockID{Number: refA.Number, Hash: refA.Hash}, 120)
	logger.Info("channel A accepted at open+timeout: its L2 blocks are safe", "l2", refA.Number)

	// ---- CHANNEL B: one L1 block LATE, entirely post-Amsterdam ----

	fromB, toB := nextUnsafeRange("channel B")
	refB := sys.L2EL.BlockRefByNumber(toB)
	framesB := buildChannel(fromB, toB)

	openB := postFrames("channel B first frame", framesB[0])
	require.Truef(isAmsterdam(openB), "channel B must open on a POST-Amsterdam L1 block (opened at %d)", openB)

	lateTargetB := openB + channelTimeout + 1
	driveL1To(lateTargetB - 1)
	closedB := postFrames("channel B remaining frames", framesB[1:]...)
	require.Equalf(lateTargetB, closedB, "channel B's last frame must land exactly one block past the deadline")
	logger.Info("channel B closed one block past the deadline", "open", openB, "close", closedB, "timeout", channelTimeout)

	// The channel timed out, so its frames must be ignored and its L2 blocks must NOT become
	// safe. Give derivation ample L1 blocks and time to prove the non-advance is settled, not
	// merely slow — the identical channel A above became safe under the same treatment.
	driveL1To(closedB + 5)
	require.Never(func() bool {
		sys.AdvanceTime(2 * time.Second)
		return sys.L2CL.SyncStatus().SafeL2.Number >= refB.Number
	}, 45*time.Second, time.Second,
		"channel B closed past the timeout, so its L2 blocks (up to %d) must never become safe", refB.Number)

	safe := sys.L2CL.SyncStatus().SafeL2
	require.Lessf(safe.Number, refB.Number,
		"safe head %d must stay below the timed-out channel's blocks (%d..%d)", safe.Number, fromB, toB)
	require.GreaterOrEqualf(safe.Number, refA.Number,
		"safe head must still hold channel A's blocks (accepted at the deadline)")
	logger.Info("channel B dropped at open+timeout+1: its L2 blocks never became safe",
		"safe", safe.Number, "wouldHaveBeen", refB.Number)

	// ---- CHANNEL C: the SAME L2 blocks B failed to deliver, re-posted inside the deadline ----
	//
	// Without this, "B's blocks never became safe" would also be satisfied if B had been
	// rejected for some reason OTHER than the timeout — a malformed encoding, or an
	// Amsterdam-specific rejection that has nothing to do with the deadline. Channel C is
	// identical to B in every respect that could cause such a rejection (same L2 blocks, same
	// encoder, same poster, both endpoints post-Amsterdam) and differs ONLY in landing its last
	// frame one block earlier — exactly on the deadline instead of one past it. If C is accepted
	// and B was not, the single L1 block of spread is provably the whole cause.
	framesC := buildChannel(fromB, toB)
	openC := postFrames("channel C first frame", framesC[0])
	require.Truef(isAmsterdam(openC), "channel C must open on a POST-Amsterdam L1 block (opened at %d)", openC)

	closeTargetC := openC + channelTimeout
	driveL1To(closeTargetC - 1)
	closedC := postFrames("channel C remaining frames", framesC[1:]...)
	require.Equalf(closeTargetC, closedC, "channel C's last frame must land exactly on the deadline block %d", closeTargetC)

	sys.L2CL.ReachedRef(suptypes.CrossSafe, eth.BlockID{Number: refB.Number, Hash: refB.Hash}, 120)
	logger.Info("channel C accepted at open+timeout: the very blocks the timed-out channel B carried are now safe",
		"l2", refB.Number, "open", openC, "close", closedC)
}
