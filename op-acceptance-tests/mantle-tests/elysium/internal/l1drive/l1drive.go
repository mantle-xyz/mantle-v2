// Package l1drive drives L1 block production for the suites that take manual control of the
// L1, i.e. the reorg cases. Those suites stop the auto-FakePoS CL and produce every subsequent
// L1 block themselves through the TestSequencer, so they all need the same four operations and
// had grown four near-identical copies of them.
package l1drive

import (
	"time"

	"github.com/ethereum-optimism/optimism/op-devstack/devtest"
	"github.com/ethereum-optimism/optimism/op-devstack/presets"
	"github.com/ethereum-optimism/optimism/op-devstack/stack"
	"github.com/ethereum-optimism/optimism/op-devstack/stack/match"
	"github.com/ethereum-optimism/optimism/op-service/apis"
	"github.com/ethereum-optimism/optimism/op-service/eth"
	"github.com/ethereum-optimism/optimism/op-test-sequencer/sequencer/seqtypes"
	"github.com/ethereum/go-ethereum/common"
)

const (
	// catchUpLag is how far the L2 unsafe origin may trail the L1 head and still count as
	// "in step". Two blocks absorbs the ordinary derivation delay without letting the L2 drift
	// far enough that a later reorg target falls outside the range it has derived.
	catchUpLag = 2
	// clockStep is how far the time-travel clock advances per poll. The L1 is produced by hand,
	// so only the wall-clock-bound L2 sequencer moves when the clock does.
	clockStep = 2 * time.Second
)

// Driver produces L1 blocks on demand and keeps the L2 fed while derivation catches up.
//
// Production errors are recorded rather than asserted, because most production happens inside
// a polled condition: testify runs an Eventually condition in its own goroutine, where
// require.NoError's FailNow is a runtime.Goexit that kills only that goroutine. Nothing is then
// sent on the channel, so Eventually keeps ticking and finally reports "Condition never
// satisfied" minutes later with the real error lost. Err is asserted from the test goroutine
// instead — While does it for you.
type Driver struct {
	t   devtest.T
	sys *presets.MantleSingleChainMultiNodeWithTestSeq
	ts  apis.TestSequencerControlAPI
	err error
}

// New stops the L1's auto-FakePoS and returns a Driver that owns L1 production from here on.
func New(t devtest.T, sys *presets.MantleSingleChainMultiNodeWithTestSeq) *Driver {
	cl := sys.L1Network.Escape().L1CLNode(match.FirstL1CL)
	sys.L1Network.WaitForBlock()
	sys.ControlPlane.FakePoSState(cl.ID(), stack.Stop)
	return &Driver{
		t:   t,
		sys: sys,
		ts:  sys.TestSequencer.Escape().ControlAPI(sys.L1Network.ChainID()),
	}
}

// Err returns the first recorded production error, or nil.
func (d *Driver) Err() error { return d.err }

// InStep produces one L1 block and then advances the clock until the L2 unsafe origin has
// caught up to within catchUpLag of the new head.
//
// It asserts directly, so it must only be called from the test goroutine — never from inside
// a polled condition. Use Produce there instead.
func (d *Driver) InStep() {
	require := d.t.Require()
	require.NoError(d.ts.New(d.t.Ctx(), seqtypes.BuildOpts{Parent: common.Hash{}}))
	require.NoError(d.ts.Next(d.t.Ctx()))
	require.Eventually(func() bool {
		d.sys.AdvanceTime(clockStep)
		l1head := d.sys.L1EL.BlockRefByLabel(eth.Unsafe).Number
		l2origin := d.sys.L2EL.BlockRefByLabel(eth.Unsafe).L1Origin.Number
		return l1head-l2origin <= catchUpLag
	}, 60*time.Second, 200*time.Millisecond)
}

// Produce mines one L1 block without requiring the L2 to keep pace — during a reorg the L2
// origin legitimately falls behind, so the strict catch-up of InStep cannot hold. It records
// the first error instead of asserting, and becomes a no-op once one has been recorded.
func (d *Driver) Produce() {
	if d.err != nil {
		return
	}
	if err := d.ts.New(d.t.Ctx(), seqtypes.BuildOpts{Parent: common.Hash{}}); err != nil {
		d.err = err
		return
	}
	if err := d.ts.Next(d.t.Ctx()); err != nil {
		d.err = err
	}
}

// Fork starts a competing chain on parent, which the TestSequencer forces canonical.
func (d *Driver) Fork(parent common.Hash) {
	require := d.t.Require()
	require.NoError(d.ts.New(d.t.Ctx(), seqtypes.BuildOpts{Parent: parent}))
	require.NoError(d.ts.Next(d.t.Ctx()))
}

// While advances the clock until cond holds, keeping a little L1 ahead of the L2 origin so
// there is always L1 left to consume. A production error ends the wait immediately and is
// asserted here, on the test goroutine, with its real message.
func (d *Driver) While(cond func() bool, timeout time.Duration, msg string) {
	require := d.t.Require()
	require.Eventually(func() bool {
		if d.err != nil {
			return true // bail out of polling; the error is asserted below
		}
		d.sys.AdvanceTime(clockStep)
		if d.sys.L2EL.BlockRefByLabel(eth.Unsafe).L1Origin.Number+catchUpLag >= d.sys.L1EL.BlockRefByLabel(eth.Unsafe).Number {
			d.Produce()
		}
		return cond()
	}, timeout, 300*time.Millisecond, msg)
	require.NoErrorf(d.err, "L1 block production failed while waiting: %s", msg)
}

// EpochOpener returns the FIRST (lowest-numbered) L2 block whose L1 origin is l1Num — the block
// that opens the epoch anchored at that L1 block — searching downward from fromL2.
func (d *Driver) EpochOpener(fromL2 uint64, l1Num uint64) (eth.L2BlockRef, bool) {
	var opener eth.L2BlockRef
	found := false
	for n := fromL2; n > 0; n-- {
		b := d.sys.L2EL.BlockRefByNumber(n)
		if b.L1Origin.Number == l1Num {
			opener = b
			found = true
		} else if found && b.L1Origin.Number < l1Num {
			break
		}
	}
	return opener, found
}
