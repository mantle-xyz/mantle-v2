package reorg

import (
	"time"

	"github.com/ethereum-optimism/optimism/op-devstack/devtest"
	"github.com/ethereum-optimism/optimism/op-devstack/dsl"
	"github.com/ethereum-optimism/optimism/op-service/eth"
)

// waitL2HeadSettled polls until the L2 unsafe head stops advancing (two equal
// reads ~1s apart). A just-stopped op-node sequencer can still have an in-flight
// block committing; building on the head before it settles would build on a
// stale parent and lose to op-node's last block.
func waitL2HeadSettled(t devtest.T, el *dsl.L2ELNode) eth.L2BlockRef {
	var last eth.L2BlockRef
	first := true
	t.Require().Eventuallyf(func() bool {
		cur := el.BlockRefByLabel(eth.Unsafe)
		if first || cur.Number != last.Number {
			last, first = cur, false
			return false
		}
		last = cur
		return true
	}, 30*time.Second, 1*time.Second, "L2 unsafe head must settle after StopSequencer")
	return last
}
