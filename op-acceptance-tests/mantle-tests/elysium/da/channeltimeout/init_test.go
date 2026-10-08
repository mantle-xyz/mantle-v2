package channeltimeout

import (
	"testing"

	"github.com/ethereum-optimism/optimism/op-acceptance-tests/mantle-tests/elysium/internal/testmain"
)

const (
	amsterdamOffset = uint64(120)
	l1BlockTime     = uint64(6)
)

func TestMain(m *testing.M) {
	testmain.RunTestSeq(m, amsterdamOffset)
}
