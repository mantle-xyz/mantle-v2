package l2engineversions

import (
	"testing"

	"github.com/ethereum-optimism/optimism/op-devstack/devtest"
	"github.com/ethereum-optimism/optimism/op-devstack/presets"
	"github.com/ethereum-optimism/optimism/op-service/eth"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
)

// TestL2Block_ArsiaHeaderShape samples three CONSECUTIVE L2 unsafe headers and
// asserts they keep the steady-state Arsia header shape while the L1 runs
// Glamsterdam (Amsterdam).
//
// The Glamsterdam L1 is the ENVIRONMENT here, not the discriminator: none of the
// sampled L2 header fields are influenced by the L1 fork, so every assertion below
// would hold just as well under a pre-Amsterdam L1. What this guards is survival +
// consistency — the L2 keeps producing well-formed, consecutive Arsia headers even
// while its L1 has crossed the Amsterdam boundary. It is a header-shape proxy read
// over RPC.
//
// This does NOT test engine-method version selection (getPayload / newPayload /
// forkchoiceUpdated versions) — that is covered by the op-node unit test, which
// exercises the engine API directly.
//
// Per block it checks:
//   - header.RequestsHash == the empty-requests hash — the L2 inherits the OP-Stack
//     Isthmus behaviour (a non-nil requests hash fixed to sha256("")), i.e. it
//     emits no execution-layer requests. This is an L2-Arsia property, independent
//     of the L1 fork.
//
// It also checks the three blocks are genuinely consecutive (parent-hash linked
// and numbered n, n+1, n+2), so this is a multi-block statement about steady-state
// block production, not a single lucky sample.
func TestL2Block_ArsiaHeaderShape(gt *testing.T) {
	t := devtest.SerialT(gt)
	sys := presets.NewMantleMinimal(t)
	require := t.Require()
	ctx := t.Ctx()

	// Establish the Glamsterdam-L1 ENVIRONMENT: wait for the L1 to activate
	// Amsterdam so the sampled L2 blocks are produced after the L1 has crossed the
	// boundary. The assertions below are L2-internal (Arsia) and do not depend on
	// this; the wait only guarantees we sample in the post-boundary environment.
	l1Config := sys.L1Network.Escape().ChainConfig()
	require.NotNil(l1Config.AmsterdamTime, "L1 AmsterdamTime must be configured")
	sys.L1EL.WaitForTime(*l1Config.AmsterdamTime)

	// Anchor at the current unsafe head, then wait for three more L2 blocks so that
	// blocks boundary+1, boundary+2, boundary+3 are all freshly produced strictly
	// after the L1 Amsterdam boundary and are all present on the unsafe chain.
	const sampleCount = uint64(3)
	boundary := sys.L2EL.BlockRefByLabel(eth.Unsafe).Number
	sys.L2EL.WaitForUnsafe(func(bi eth.BlockInfo) (bool, error) {
		return bi.NumberU64() >= boundary+sampleCount, nil
	})

	var prevHash common.Hash
	for i := uint64(1); i <= sampleCount; i++ {
		num := boundary + i

		// Resolve the canonical hash for this height, then read the full header via
		// the same verifying eth client the derivation test uses for L1 origins.
		ref := sys.L2EL.BlockRefByNumber(num)
		info, _, err := sys.L2EL.Escape().EthClient().InfoAndTxsByHash(ctx, ref.Hash)
		require.NoErrorf(err, "must read L2 block %d by hash", num)
		require.Equalf(num, info.NumberU64(), "block %d returned unexpected number", num)

		header := info.Header()

		// RequestsHash must behave as on Arsia: present (Isthmus) and fixed to the
		// empty-requests hash, i.e. the L2 produces no execution-layer requests.
		require.NotNilf(header.RequestsHash,
			"L2 (Arsia) block %d must carry a requests hash (Isthmus behaviour)", num)
		require.Equalf(types.EmptyRequestsHash, *header.RequestsHash,
			"L2 (Arsia) block %d requests hash must be the empty-requests hash", num)

		// The sampled blocks must be genuinely consecutive.
		if i > 1 {
			require.Equalf(prevHash, header.ParentHash,
				"L2 block %d must be the child of block %d", num, num-1)
		}
		prevHash = info.Hash()
	}
}
