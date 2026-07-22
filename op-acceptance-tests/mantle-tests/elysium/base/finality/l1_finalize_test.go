package finality

import (
	"errors"
	"math/big"
	"testing"
	"time"

	opforks "github.com/ethereum-optimism/optimism/op-core/forks"
	"github.com/ethereum-optimism/optimism/op-devstack/devtest"
	"github.com/ethereum-optimism/optimism/op-devstack/presets"
	"github.com/ethereum-optimism/optimism/op-service/eth"
	"github.com/ethereum/go-ethereum"
)

// TestL1Finalize_PostUpgrade verifies that L1 finality over post-Amsterdam blocks propagates into
// L2 finality. The L1 finalized head must itself carry the Amsterdam header fields, the L2
// finalized head must advance past the boundary, and its L1 origin must never run ahead of the
// current L1 finalized head.
func TestL1Finalize_PostUpgrade(gt *testing.T) {
	t := devtest.SerialT(gt)
	sys := presets.NewMantleMinimal(t)
	require := t.Require()
	ctx := t.Ctx()

	rollupCfg := sys.L2Chain.Escape().RollupConfig()
	l1Config := sys.L1Network.Escape().ChainConfig()

	require.True(sys.L2Chain.IsMantleForkActive(opforks.MantleElysium), "L2 must run with the Mantle fork active")
	require.NotNil(rollupCfg.MantleElysiumTime, "MantleElysiumTime must be configured")
	require.NotNil(l1Config.AmsterdamTime, "L1 AmsterdamTime must be configured")

	isL1Amsterdam := func(num uint64, blockTime uint64) bool {
		return l1Config.IsAmsterdam(new(big.Int).SetUint64(num), blockTime)
	}

	// (1) Wait for the L1 to activate Amsterdam (Glamsterdam EL).
	t.Log("Waiting for L1 Amsterdam to activate")
	sys.L1EL.WaitForTime(*l1Config.AmsterdamTime)
	t.Log("L1 Amsterdam activated")

	// (2) Record the L2 finalized head at the boundary as a baseline.
	baseFinalized := sys.L2CL.SyncStatus().FinalizedL2
	t.Logf("recorded L2 baseline finalized head: number=%d l1Origin=%d", baseFinalized.Number, baseFinalized.L1Origin.Number)

	// (3) Wait until the L1 FINALIZED head is a genuinely post-Amsterdam block.
	// fakepos finalizes head-20, so this proves the L1 is finalizing Glamsterdam
	// blocks (not just the pre-fork tail).
	l1FinalizedRef := sys.L1EL.WaitForLabelRef(eth.Finalized, func(info eth.BlockInfo) (bool, error) {
		return isL1Amsterdam(info.NumberU64(), info.Time()), nil
	})
	t.Logf("L1 finalized head is post-Amsterdam: number=%d", l1FinalizedRef.Number)

	// The finalized L1 header itself must carry the Amsterdam header fields.
	finL1Info, _, err := sys.L1EL.EthClient().InfoAndTxsByHash(ctx, l1FinalizedRef.Hash)
	require.NoError(err, "must read the finalized L1 block by hash")
	finL1Header := finL1Info.Header()
	require.NotNil(finL1Header.BlockAccessListHash, "finalized post-Amsterdam L1 block must carry an EIP-7928 BlockAccessListHash")
	require.NotNil(finL1Header.SlotNumber, "finalized post-Amsterdam L1 block must carry an EIP-7843 SlotNumber")

	// (4) Wait until the L2 finalized head advances past the baseline AND its L1
	// origin is a post-Amsterdam L1 block, then run the discriminating checks.
	l2BlockTime := time.Duration(rollupCfg.BlockTime) * time.Second
	for {
		fin := sys.L2CL.SyncStatus().FinalizedL2

		if fin.Number > baseFinalized.Number {
			// Fetch the L1 origin the finalized L2 block was derived against.
			originInfo, _, err := sys.L1EL.EthClient().InfoAndTxsByHash(ctx, fin.L1Origin.Hash)
			if errors.Is(err, ethereum.NotFound) {
				t.Log("L2 finalized head references an L1 origin not found by L1 EL yet, waiting...", "origin", fin.L1Origin)
			} else {
				require.NoError(err, "must read the L2 finalized head's L1 origin by hash")

				if isL1Amsterdam(originInfo.NumberU64(), originInfo.Time()) {
					// (4a) The finalized L2 block is derived from a genuinely
					// post-Amsterdam L1 origin: re-assert the Amsterdam header fields.
					originHeader := originInfo.Header()
					require.NotNil(originHeader.BlockAccessListHash,
						"L2 finalized head's L1 origin (post-Amsterdam) must carry an EIP-7928 BlockAccessListHash")
					require.NotNil(originHeader.SlotNumber,
						"L2 finalized head's L1 origin (post-Amsterdam) must carry an EIP-7843 SlotNumber")

					// L2 finality is gated by the L1 finalized head. Read L1 second
					// to avoid false failures when L1 finality advances between samples.
					l1FinalizedNow := sys.L1EL.BlockRefByLabel(eth.Finalized)
					require.LessOrEqual(fin.L1Origin.Number, l1FinalizedNow.Number,
						"L2 finalized head's L1 origin (%d) must not run ahead of the L1 finalized head (%d)",
						fin.L1Origin.Number, l1FinalizedNow.Number)

					t.Logf("L2 finalized head advanced across the Glamsterdam boundary: number=%d (baseline %d), l1Origin=%d (post-Amsterdam), l1Finalized=%d",
						fin.Number, baseFinalized.Number, fin.L1Origin.Number, l1FinalizedNow.Number)
					return
				}
				t.Log("L2 finalized head advanced but still references a pre-Amsterdam L1 origin, waiting...", "origin", fin.L1Origin.Number)
			}
		} else {
			t.Log("L2 finalized head has not advanced past the baseline yet, waiting...", "finalized", fin.Number, "baseline", baseFinalized.Number)
		}

		select {
		case <-time.After(l2BlockTime):
		case <-ctx.Done():
			require.Fail("L2 finalized head never advanced to a post-Amsterdam L1 origin (finality stalled at the Glamsterdam boundary)")
		}
	}
}
