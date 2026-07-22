package submission

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"strings"
	"testing"
	"time"

	opforks "github.com/ethereum-optimism/optimism/op-core/forks"
	"github.com/ethereum-optimism/optimism/op-devstack/devtest"
	"github.com/ethereum-optimism/optimism/op-devstack/presets"
	"github.com/ethereum-optimism/optimism/op-service/client"
	"github.com/ethereum-optimism/optimism/op-service/eth"
	"github.com/ethereum-optimism/optimism/op-service/sources"
	"github.com/ethereum/go-ethereum/common/hexutil"
)

// wrongBlockRPC wraps op-node's L1 RPC client and, for a by-number header fetch, asks the L1 for
// the PARENT block instead — so op-node receives a header inconsistent with the block number it
// requested. Every other call passes through to the real RPC.
type wrongBlockRPC struct {
	client.RPC
}

func (r wrongBlockRPC) CallContext(ctx context.Context, result any, method string, args ...any) error {
	if method == "eth_getBlockByNumber" && len(args) >= 1 {
		if numHex, ok := args[0].(string); ok {
			if n, err := hexutil.DecodeUint64(numHex); err == nil && n > 1 {
				args[0] = hexutil.EncodeUint64(n - 1)
			}
		}
	}
	return r.RPC.CallContext(ctx, result, method, args...)
}

type headerMutator func(*sources.RPCHeader)

type mutatedHeaderRPC struct {
	client.RPC
	mutate headerMutator
}

func (r mutatedHeaderRPC) CallContext(ctx context.Context, result any, method string, args ...any) error {
	if method != "eth_getBlockByNumber" {
		return r.RPC.CallContext(ctx, result, method, args...)
	}
	var header *sources.RPCHeader
	if err := r.RPC.CallContext(ctx, &header, method, args...); err != nil {
		return err
	}
	if header != nil {
		r.mutate(header)
	}
	if out, ok := result.(**sources.RPCHeader); ok {
		*out = header
		return nil
	}
	return r.RPC.CallContext(ctx, result, method, args...)
}

type malformedHeaderFieldRPC struct {
	client.RPC
	field string
	value any
}

func (r malformedHeaderFieldRPC) CallContext(ctx context.Context, result any, method string, args ...any) error {
	if method != "eth_getBlockByNumber" {
		return r.RPC.CallContext(ctx, result, method, args...)
	}
	var raw json.RawMessage
	if err := r.RPC.CallContext(ctx, &raw, method, args...); err != nil {
		return err
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return err
	}
	value, err := json.Marshal(r.value)
	if err != nil {
		return err
	}
	obj[r.field] = value
	out, err := json.Marshal(obj)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(out, result); err != nil {
		return fmt.Errorf("decode malformed L1 header field %s: %w", r.field, err)
	}
	return nil
}

// TestDerivation_MaliciousL1Header_Rejected proves op-node's L1 client REJECTS a header that does
// not correspond to the block number/hash it requested or whose new Amsterdam fields are malformed,
// WITHOUT panicking — the safety property behind derivation trusting L1.
//
// The rejection paths are op-node's real L1 header validation paths: by-number fetches must satisfy
// numberID.CheckID, and untrusted RPC headers must hash back to the RPC hash after decoding the
// Amsterdam header fields. The control assertion first proves the header being mutated is a genuine
// post-Amsterdam header that carries BlockAccessListHash and SlotNumber.
//
// The drive: a control client fetches a well-formed post-Amsterdam header cleanly, then clients
// that mutate the returned header must be rejected with no panic:
//   - parent/ID mismatch;
//   - RPC hash mismatch;
//   - Amsterdam BAL / SlotNumber stripped while the old hash is retained;
//   - an ordinary RLP/header field changed while the old hash is retained;
//   - BAL / SlotNumber invalid JSON types.
//
// Flips red if op-node accepts a header whose block number/hash/Amsterdam fields do not match the
// requested block, or panics on malformed header fields.
func TestDerivation_MaliciousL1Header_Rejected(gt *testing.T) {
	t := devtest.SerialT(gt)
	sys := presets.NewMantleMinimal(t)
	require := t.Require()
	ctx := t.Ctx()
	logger := t.Logger()

	l1Config := sys.L1Network.Escape().ChainConfig()
	require.True(sys.L2Chain.IsMantleForkActive(opforks.MantleElysium), "L2 must run with Mantle Elysium active")
	require.NotNil(l1Config.AmsterdamTime, "L1 AmsterdamTime must be configured")

	// Cross Amsterdam so the header op-node validates is a genuine Glamsterdam header.
	sys.L1EL.WaitForTime(*l1Config.AmsterdamTime)
	require.Eventually(func() bool {
		h := sys.L1EL.BlockRefByLabel(eth.Unsafe)
		if h.Number < 3 {
			return false
		}
		// Require the block BELOW the head to be post-Amsterdam, so the settled block we validate
		// (head-1) is itself a genuine Glamsterdam header.
		prev := sys.L1EL.BlockRefByNumber(h.Number - 1)
		return l1Config.IsAmsterdam(new(big.Int).SetUint64(prev.Number), prev.Time)
	}, 90*time.Second, time.Second, "need a settled post-Amsterdam L1 block with a parent")
	n := sys.L1EL.BlockRefByLabel(eth.Unsafe).Number - 1 // a settled post-Amsterdam block that has a parent

	// Decorate the SAME underlying L1 RPC op-node uses, so the header path exercised is the real one.
	raw := sys.L1EL.EthClient().RPC()
	cfg := sources.L1ClientSimpleConfig(false, sources.RPCKindStandard, 10)

	// Control: op-node's L1 client fetches the well-formed post-Amsterdam header cleanly, proving the
	// setup works and the header is a genuine Glamsterdam header.
	goodCl, err := sources.NewL1Client(raw, logger, nil, cfg)
	require.NoError(err, "build control L1 client")
	info, err := goodCl.InfoByNumber(ctx, n)
	require.NoError(err, "control: op-node's L1 client must fetch a well-formed post-Amsterdam header")
	require.Equal(n, info.NumberU64(), "control must return the requested block")
	require.NotNil(info.Header().BlockAccessListHash, "the validated header must be a genuine Glamsterdam header (BAL)")
	require.NotNil(info.Header().SlotNumber, "the validated header must be a genuine Glamsterdam header (SlotNumber)")

	cases := []struct {
		name       string
		rpc        client.RPC
		wantErrAll []string
	}{
		{
			name:       "parent block returned for requested number",
			rpc:        wrongBlockRPC{raw},
			wantErrAll: []string{"does not match requested ID"},
		},
		{
			name: "rpc hash mismatch",
			rpc: mutatedHeaderRPC{RPC: raw, mutate: func(h *sources.RPCHeader) {
				h.Hash[0] ^= 0x01
			}},
			wantErrAll: []string{"failed to verify block hash"},
		},
		{
			name: "block access list hash stripped",
			rpc: mutatedHeaderRPC{RPC: raw, mutate: func(h *sources.RPCHeader) {
				h.BlockAccessListHash = nil
			}},
			wantErrAll: []string{"failed to verify block hash"},
		},
		{
			name: "slot number stripped",
			rpc: mutatedHeaderRPC{RPC: raw, mutate: func(h *sources.RPCHeader) {
				h.SlotNumber = nil
			}},
			wantErrAll: []string{"failed to verify block hash"},
		},
		{
			name: "ordinary header field mismatch",
			rpc: mutatedHeaderRPC{RPC: raw, mutate: func(h *sources.RPCHeader) {
				h.Root[0] ^= 0x01
			}},
			wantErrAll: []string{"failed to verify block hash"},
		},
		{
			name:       "block access list hash invalid type",
			rpc:        malformedHeaderFieldRPC{RPC: raw, field: "blockAccessListHash", value: "0x1234"},
			wantErrAll: []string{"blockAccessListHash", "hex string has length 4", "want 64", "common.Hash"},
		},
		{
			name:       "slot number invalid type",
			rpc:        malformedHeaderFieldRPC{RPC: raw, field: "slotNumber", value: "not-a-quantity"},
			wantErrAll: []string{"slotNumber", "hex string without 0x prefix", "hexutil.Uint64"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t devtest.T) {
			badCl, err := sources.NewL1Client(tc.rpc, logger, nil, cfg)
			require.NoError(err, "build malicious L1 client")

			var badInfo eth.BlockInfo
			var badErr error
			require.NotPanics(func() { badInfo, badErr = badCl.InfoByNumber(ctx, n) },
				"op-node must not panic on an inconsistent L1 header")
			require.Error(badErr, "op-node must reject the malicious L1 header")
			require.Nil(badInfo, "no block info must be returned on rejection")
			for _, want := range tc.wantErrAll {
				require.Truef(strings.Contains(badErr.Error(), want),
					"rejection reason mismatch for %q: got %q, want substring %q", tc.name, badErr.Error(), want)
			}
			t.Log("op-node rejected malicious post-Amsterdam L1 header", "block", n, "case", tc.name, "err", badErr)
		})
	}
}
