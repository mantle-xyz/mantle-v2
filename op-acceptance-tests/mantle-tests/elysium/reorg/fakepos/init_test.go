package fakepos

import (
	"testing"

	"github.com/ethereum-optimism/optimism/op-acceptance-tests/mantle-tests/elysium/internal/testmain"
)

const amsterdamOffset = uint64(30)

func TestMain(m *testing.M) {
	testmain.RunTestSeqWallClock(m, amsterdamOffset)
}
