package verifierconverge

import (
	"testing"

	"github.com/ethereum-optimism/optimism/op-acceptance-tests/mantle-tests/elysium/internal/testmain"
)

func TestMain(m *testing.M) {
	testmain.RunMultiNode(m, testmain.PostBoundaryAmsterdamOffset)
}
