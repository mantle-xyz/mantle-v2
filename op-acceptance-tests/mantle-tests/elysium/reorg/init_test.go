package reorg

import (
	"testing"
	"time"

	"github.com/ethereum-optimism/optimism/op-acceptance-tests/mantle-tests/elysium/internal/testmain"
)

const (
	amsterdamOffset = uint64(30)
	l1BlockTime     = 6 * time.Second
)

func TestMain(m *testing.M) {
	testmain.RunTestSeq(m, amsterdamOffset)
}
