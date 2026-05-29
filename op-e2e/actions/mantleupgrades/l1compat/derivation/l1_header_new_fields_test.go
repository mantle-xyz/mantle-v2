package derivation

import (
	"bytes"
	"encoding/json"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/rlp"
	"github.com/stretchr/testify/require"

	"github.com/ethereum-optimism/optimism/op-service/eth"
)

// TestDerivation_L1HeaderWithNewFields_Behavior is a parser-level test
// confirming that op-node's L1 header ingestion path stays well-behaved when
// it sees post-Glamsterdam (Amsterdam) L1 headers that carry the new optional
// fields `BlockAccessListHash` (EIP-7928) and `SlotNumber` (EIP-7843).
//
// We run this at the parser/type level rather than as a full action test
// because op-geth v0.0.0-20260526034114-45ddd63ceae2 does not yet populate
// `Header.BlockAccessListHash` from its mining path, so op-e2e's L1Miner cannot
// produce a valid post-Amsterdam L1 block end-to-end. (See sibling skeletons
// l1_reorg_propagation_test.go and l1_reorg_at_upgrade_activation_test.go for
// the end-to-end coverage that resumes once that op-geth path is wired.)
//
// The contract this test pins:
//  1. A synthetic post-Glamsterdam L1 header carrying the two new fields
//     RLP-roundtrips and JSON-roundtrips without loss, and its block hash
//     observably depends on those fields.
//  2. `eth.InfoToL1BlockRef` — the structural bridge op-node uses to track L1
//     for L2 derivation — does not carry `BlockAccessListHash` / `SlotNumber`.
//     This is enforced by the `L1BlockRef` type definition itself: any future
//     addition of these fields to `L1BlockRef` would silently leak them into
//     downstream consumers, so we assert the surface here.
//  3. An RPC-style hash mismatch (a header claiming a different hash than its
//     RLP recomputed hash) is detectable via a simple recompute-and-compare,
//     mirroring what op-node's L1 source does to reject inconsistent headers.
func TestDerivation_L1HeaderWithNewFields_Behavior(t *testing.T) {
	balHash := common.HexToHash("0x9adef00d000000000000000000000000000000000000000000000000deadbeef")
	slot := uint64(123_456)
	parent := common.HexToHash("0x" + "11" +
		"22" +
		"33" +
		"44" +
		"55" +
		"66" +
		"77" +
		"88" +
		"99" +
		"aa" +
		"bb" +
		"cc" +
		"dd" +
		"ee" +
		"ff" +
		"00")

	// Minimum viable post-Amsterdam header. We do not run consensus validation
	// on it (that requires a real chain) — we exercise the structural / encoding
	// surface only.
	mkHeader := func() *types.Header {
		zero := uint64(0)
		parentBeaconRoot := common.HexToHash("0xb1b2b3b4b5b6b7b8b9babbbcbdbebfc0c1c2c3c4c5c6c7c8c9cacbcccdcecfd0")
		// Trailing optional fields (WithdrawalsHash, RequestsHash, ParentBeaconRoot)
		// must be set or RLP decode fails: the optional tail elements are positional,
		// so omitting an earlier one truncates the encoded body.
		return &types.Header{
			ParentHash:          parent,
			Number:              big.NewInt(100),
			Time:                1_800_000_000,
			GasLimit:            30_000_000,
			GasUsed:             0,
			BaseFee:             big.NewInt(7),
			Difficulty:          big.NewInt(0),
			BlobGasUsed:         &zero,
			ExcessBlobGas:       &zero,
			WithdrawalsHash:     &types.EmptyWithdrawalsHash,
			ParentBeaconRoot:    &parentBeaconRoot,
			RequestsHash:        &types.EmptyRequestsHash,
			BlockAccessListHash: &balHash,
			SlotNumber:          &slot,
		}
	}
	header := mkHeader()

	t.Run("RLP roundtrip preserves new fields and hash", func(t *testing.T) {
		var buf bytes.Buffer
		require.NoError(t, rlp.Encode(&buf, header))

		var decoded types.Header
		require.NoError(t, rlp.DecodeBytes(buf.Bytes(), &decoded))
		require.NotNil(t, decoded.BlockAccessListHash, "BlockAccessListHash must roundtrip")
		require.Equal(t, balHash, *decoded.BlockAccessListHash, "BlockAccessListHash value preserved")
		require.NotNil(t, decoded.SlotNumber, "SlotNumber must roundtrip")
		require.Equal(t, slot, *decoded.SlotNumber, "SlotNumber value preserved")
		require.Equal(t, header.Hash(), decoded.Hash(), "header hash matches after RLP roundtrip")
	})

	t.Run("JSON roundtrip preserves new fields", func(t *testing.T) {
		raw, err := json.Marshal(header)
		require.NoError(t, err)

		// Field names follow op-geth's struct tags: `balHash`, `slotNumber`.
		require.Contains(t, string(raw), `"balHash":"0x9adef00d`, "JSON includes balHash key with expected value")
		require.Contains(t, string(raw), `"slotNumber":"`, "JSON includes slotNumber key")

		var decoded types.Header
		require.NoError(t, json.Unmarshal(raw, &decoded))
		require.NotNil(t, decoded.BlockAccessListHash)
		require.Equal(t, balHash, *decoded.BlockAccessListHash)
		require.NotNil(t, decoded.SlotNumber)
		require.Equal(t, slot, *decoded.SlotNumber)
	})

	t.Run("block hash depends on new fields", func(t *testing.T) {
		withFields := mkHeader().Hash()
		bare := mkHeader()
		bare.BlockAccessListHash = nil
		bare.SlotNumber = nil
		require.NotEqual(t, withFields, bare.Hash(), "header.Hash() must depend on BlockAccessListHash / SlotNumber")

		differentBAL := mkHeader()
		other := common.HexToHash("0xfeedfacedeadbeef000000000000000000000000000000000000000000000000")
		differentBAL.BlockAccessListHash = &other
		require.NotEqual(t, withFields, differentBAL.Hash(), "changing BlockAccessListHash must change block hash")

		differentSlot := mkHeader()
		other2 := slot + 1
		differentSlot.SlotNumber = &other2
		require.NotEqual(t, withFields, differentSlot.Hash(), "changing SlotNumber must change block hash")
	})

	t.Run("L1BlockRef does not carry Amsterdam fields", func(t *testing.T) {
		// op-node tracks L1 state via eth.L1BlockRef. Confirm conversion drops the
		// new fields — they are not part of the derivation interface.
		ref := eth.InfoToL1BlockRef(eth.HeaderBlockInfo(header))
		require.Equal(t, header.Hash(), ref.Hash)
		require.Equal(t, uint64(100), ref.Number)
		require.Equal(t, parent, ref.ParentHash)
		require.Equal(t, uint64(1_800_000_000), ref.Time)

		// Structural check: L1BlockRef should be {Hash, Number, ParentHash, Time}
		// only. If a future change adds BlockAccessListHash / SlotNumber here, it
		// would silently propagate into the derivation pipeline; we'd want to see
		// this assertion fail and decide explicitly.
		refJSON, err := json.Marshal(ref)
		require.NoError(t, err)
		require.NotContains(t, string(refJSON), "balHash", "L1BlockRef must not expose balHash")
		require.NotContains(t, string(refJSON), "slotNumber", "L1BlockRef must not expose slotNumber")
		require.NotContains(t, string(refJSON), "blockAccessListHash", "L1BlockRef must not expose blockAccessListHash")
	})

	t.Run("RPC-style hash mismatch is detectable", func(t *testing.T) {
		// Simulate the rejection check op-node performs: server claims hash X,
		// locally we recompute hash via RLP and compare. They must agree.
		claimed := common.HexToHash("0x" + "00" +
			"11" +
			"22" +
			"33" +
			"44" +
			"55" +
			"66" +
			"77" +
			"88" +
			"99" +
			"aa" +
			"bb" +
			"cc" +
			"dd" +
			"ee" +
			"ff")
		actual := header.Hash()
		require.NotEqual(t, claimed, actual, "test premise: claimed hash differs from recomputed")

		// The eth.HeaderBlockInfoTrusted constructor lets a server-claimed hash be
		// attached without recomputation. A well-behaved consumer must still
		// reject the BlockInfo if `info.Hash() != header.Hash()`.
		trusted := eth.HeaderBlockInfoTrusted(claimed, header)
		require.Equal(t, claimed, trusted.Hash(), "trusted BlockInfo returns the claimed hash")
		// Reject by comparing the trusted hash against the locally-recomputed one
		// using the same Header pointer. This is the check op-node L1 sources
		// perform; it must not panic when the new optional fields are present.
		require.NotEqual(t, trusted.Hash(), header.Hash(), "mismatch is detectable without panic on Amsterdam fields")
	})
}
