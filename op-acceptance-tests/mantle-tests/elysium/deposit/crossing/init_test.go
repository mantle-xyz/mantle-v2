package crossing

import (
	"testing"

	"github.com/ethereum-optimism/optimism/op-acceptance-tests/mantle-tests/elysium/internal/testmain"
	"github.com/ethereum-optimism/optimism/op-chain-ops/devkeys"
)

// L1 is mined manually; the offset specifies the exact boundary, not a setup deadline.
const amsterdamOffset = uint64(60)

const (
	userBeforeKey = devkeys.UserKey(10_001)
	userAtKey     = devkeys.UserKey(10_002)
	userAfterKey  = devkeys.UserKey(10_003)
)

func TestMain(m *testing.M) {
	testmain.RunTestSeq(m, amsterdamOffset, testmain.WithManualL1Mining(),
		testmain.WithPrefundedL1Users(userBeforeKey, userAtKey, userAfterKey))
}
