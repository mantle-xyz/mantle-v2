package windowexpiry

import (
	"testing"

	"github.com/ethereum-optimism/optimism/op-acceptance-tests/mantle-tests/elysium/internal/testmain"
)

const (
	// This case never observes the L2 before the activation boundary, so it takes the
	// post-boundary offset by its stated criterion rather than repeating the number.
	amsterdamOffset     = testmain.PostBoundaryAmsterdamOffset
	sequencerWindowSize = uint64(4)
)

func TestMain(m *testing.M) {
	testmain.RunTestSeq(m, amsterdamOffset, testmain.WithSequencingWindow(sequencerWindowSize))
}
