package l2output

import (
	"testing"

	"github.com/ethereum-optimism/optimism/op-acceptance-tests/mantle-tests/elysium/internal/testmain"
)

func TestMain(m *testing.M) {
	testmain.RunMinimal(m, testmain.PostBoundaryAmsterdamOffset)
}
