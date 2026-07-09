package badheader

import (
	"context"
	"math/big"
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

// TestDerivation_MaliciousL1Header_Rejected proves op-node's L1 client REJECTS an inconsistent
// post-Glamsterdam L1 header WITHOUT panicking — the safety property behind derivation trusting L1.
//
// op-node fetches L1 headers with TrustRPC=false, so it recomputes each header's hash and checks
// the result matches the block it asked for (op-service/sources/eth_client.go headerCall +
// CheckID). This drives that path against a genuine Amsterdam header: a control client fetches a
// well-formed post-Amsterdam header cleanly, then a client whose RPC returns the WRONG block's
// header for the same request must be rejected with an ID-mismatch error and no panic.
//
// Flips red if op-node accepts a header that does not match the requested block, or panics on it.
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

	// Malicious: the RPC returns the parent block's header for the same request. op-node recomputes
	// the header hash and its ID-consistency check must reject it, without panicking.
	badCl, err := sources.NewL1Client(wrongBlockRPC{raw}, logger, nil, cfg)
	require.NoError(err, "build malicious L1 client")

	var badInfo eth.BlockInfo
	var badErr error
	require.NotPanics(func() { badInfo, badErr = badCl.InfoByNumber(ctx, n) },
		"op-node must not panic on an inconsistent L1 header")
	require.Error(badErr, "op-node must reject an L1 header that does not match the requested block")
	require.Nil(badInfo, "no block info must be returned on rejection")
	require.Contains(badErr.Error(), "does not match requested ID",
		"rejection must come from op-node's header ID-consistency check")
	t.Log("op-node rejected an inconsistent post-Amsterdam L1 header without panic", "block", n, "err", badErr)
}
