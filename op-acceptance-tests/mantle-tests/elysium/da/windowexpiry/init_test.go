package windowexpiry

import (
	"testing"

	"github.com/ethereum-optimism/optimism/op-acceptance-tests/mantle-tests/elysium/internal/testmain"
)

const (
	amsterdamOffset     = uint64(6)
	sequencerWindowSize = uint64(4)
)

func TestMain(m *testing.M) {
	testmain.RunTestSeq(m, amsterdamOffset, testmain.WithSequencingWindow(sequencerWindowSize))
}
