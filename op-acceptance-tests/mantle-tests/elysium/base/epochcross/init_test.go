package epochcross

import (
	"testing"

	"github.com/ethereum-optimism/optimism/op-acceptance-tests/mantle-tests/elysium/internal/testmain"
)

// The 192s offset lands Amsterdam on L1 block 32 with the default 6s block time,
// making the activation block the first slot of beacon epoch 1.
const amsterdamOffset = uint64(192)

func TestMain(m *testing.M) {
	testmain.RunMinimal(m, amsterdamOffset)
}
