package boundaryrestart

import (
	"testing"

	"github.com/ethereum-optimism/optimism/op-acceptance-tests/mantle-tests/elysium/internal/testmain"
)

// Large enough to let the test stop op-node before L1 crosses Amsterdam.
const amsterdamOffset = uint64(90)

func TestMain(m *testing.M) {
	testmain.RunMinimal(m, amsterdamOffset)
}
