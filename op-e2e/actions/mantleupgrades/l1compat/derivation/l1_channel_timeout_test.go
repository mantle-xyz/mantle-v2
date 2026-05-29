package derivation

import (
	"testing"

	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/log"

	actionsHelpers "github.com/ethereum-optimism/optimism/op-e2e/actions/helpers"
	l1compatHelpers "github.com/ethereum-optimism/optimism/op-e2e/actions/mantleupgrades/l1compat/helpers"
	"github.com/ethereum-optimism/optimism/op-e2e/e2eutils"
	"github.com/ethereum-optimism/optimism/op-service/testlog"
)

// TestDerivation_ChannelTimeout_PostUpgrade pins that the L1 → L2 derivation channel-timeout deadline (ChannelTimeout blocks of
// inactivity from the channel's opening L1 block) must remain unchanged
// across the L1 Glamsterdam activation boundary — both the count of blocks
// and the rejection behavior once the deadline elapses.
//
// Test shape (when unblocked):
//  1. Setup deploy params with `MakeL1GlamsterdamL2ArsiaDeployParams`,
//     amsterdamOffset small enough that the test crosses activation.
//  2. Open a batcher channel pre-Amsterdam; submit one frame.
//  3. Mine L1 across Amsterdam activation without including the channel's
//     closing frame, until L1 is `ChannelTimeout + 1` post-channel-start.
//  4. Submit the closing frame on a post-Amsterdam L1 block.
//  5. Assert verifier rejects the closing frame (channel-timeout reached) —
//     the timeout window must not have been silently extended by the
//     activation boundary.
//
// STATUS: skeleton only. The body never runs; the t.Skip below documents
// why and what to remove when op-geth's mining path is wired through BAL.
func TestDerivation_ChannelTimeout_PostUpgrade(gt *testing.T) {
	gt.Skip("blocked by op-geth: post-Amsterdam L1 block construction does not populate Header.BlockAccessListHash, so op-e2e L1Miner cannot mine a valid Amsterdam block yet")

	t := actionsHelpers.NewDefaultTesting(gt)
	amsterdamOffset := hexutil.Uint64(24)
	dp := l1compatHelpers.MakeL1GlamsterdamL2ArsiaDeployParams(t, l1compatHelpers.DefaultRollupTestParams(), &amsterdamOffset)
	sd := e2eutils.SetupMantleNormal(t, dp, actionsHelpers.DefaultAlloc)
	logger := testlog.Logger(t, log.LevelDebug)

	_, _, _, _, _, _, _, _ = actionsHelpers.SetupMantleReorgTestActors(t, dp, sd, logger, true)

	// TODO(unblock): open channel pre-Amsterdam, mine ChannelTimeout L1 blocks
	// across activation without including the closing frame, attempt to close
	// post-Amsterdam, assert rejection. Use sd.RollupCfg.ChannelTimeout(...)
	// for the deadline computation; mirror the pattern in
	// op-e2e/actions/mantletests/derivation/derivation_test.go around the
	// existing channel-timeout assertions.
}
