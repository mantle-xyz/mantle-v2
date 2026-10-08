package afterrestart

import (
	"testing"

	"github.com/ethereum-optimism/optimism/op-acceptance-tests/mantle-tests/elysium/internal/testmain"
)

// Steady-state restart: every observation happens after the L1 has already crossed Amsterdam,
// so a long pre-boundary window buys nothing. The cross-the-boundary-while-down variant lives
// in elysium/reorg/boundaryrestart, which needs its own (much larger) offset.
func TestMain(m *testing.M) {
	testmain.RunMinimal(m, testmain.PostBoundaryAmsterdamOffset)
}
