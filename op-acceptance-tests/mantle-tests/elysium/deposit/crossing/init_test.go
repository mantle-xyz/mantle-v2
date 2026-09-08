package crossing

import (
	"testing"

	"github.com/ethereum-optimism/optimism/op-acceptance-tests/mantle-tests/elysium/internal/testmain"
)

// The 60s offset leaves enough pre-Amsterdam L1 blocks for deposits before crossing.
const amsterdamOffset = uint64(60)

func TestMain(m *testing.M) {
	testmain.RunTestSeq(m, amsterdamOffset)
}
