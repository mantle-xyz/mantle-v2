package l2engineversions

import (
	"testing"

	"github.com/ethereum-optimism/optimism/op-devstack/devtest"
	"github.com/ethereum-optimism/optimism/op-devstack/presets"
	"github.com/ethereum-optimism/optimism/op-service/eth"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
)

// TestL2Block_ArsiaHeaderShape asserts the observable Arsia header shape across
// several freshly produced L2 blocks while the L1 runs Glamsterdam (Amsterdam).
//
// This is a stronger, multi-block version of the l2output single-header checks:
// rather than hooking the engine API, it samples three CONSECUTIVE L2 unsafe
// blocks produced strictly after the L1 crosses the Amsterdam boundary and
// asserts that none of them adopt Amsterdam header fields, while the Isthmus-era
// requests hash still behaves as it does on Arsia.
//
// Per block it checks:
//   - header.BlockAccessListHash == nil   (no EIP-7928 block access list on L2)
//   - header.SlotNumber          == nil   (no EIP-7843 slot number on L2)
//   - header.RequestsHash        == the empty-requests hash (Arsia inherits the
//     OP-Stack Isthmus behaviour: a non-nil requests hash fixed to sha256(""))
//
// It also checks the three blocks are genuinely consecutive (parent-hash linked
// and numbered n, n+1, n+2), so this is a multi-block statement about steady-state
// block production, not a single lucky sample.
//
// COVERAGE: this is the block-header PROXY only. It does NOT hook the L2 engine
// API, so it does not directly assert the engine-method versions (FCU=V3 / NewPayload=V4
// / GetPayload=V5, and never V6/newPayloadV5) nor that the V5 ExecutionPayloadEnvelope
// omits BAL/SlotNumber. Verifying that layer needs either an engine-RPC proxy that
// records op-node's method calls, or an op-e2e unit that calls the engine API directly;
// it is NOT covered by this test.
func TestL2Block_ArsiaHeaderShape(gt *testing.T) {
	t := devtest.SerialT(gt)
	sys := presets.NewMantleMinimal(t)
	require := t.Require()
	ctx := t.Ctx()

	// Wait for the L1 to activate Amsterdam so the sampled L2 blocks are produced
	// while the L2 is genuinely consuming a Glamsterdam L1.
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

		// No Amsterdam header fields must appear on the Mantle (Arsia) L2.
		require.Nilf(header.BlockAccessListHash,
			"L2 (Arsia) block %d must not carry an EIP-7928 BlockAccessListHash", num)
		require.Nilf(header.SlotNumber,
			"L2 (Arsia) block %d must not carry an EIP-7843 SlotNumber", num)

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
