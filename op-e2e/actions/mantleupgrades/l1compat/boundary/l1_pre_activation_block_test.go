package boundary

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/log"
	"github.com/stretchr/testify/require"

	actionsHelpers "github.com/ethereum-optimism/optimism/op-e2e/actions/helpers"
	l1compatHelpers "github.com/ethereum-optimism/optimism/op-e2e/actions/mantleupgrades/l1compat/helpers"
	"github.com/ethereum-optimism/optimism/op-e2e/e2eutils"
	"github.com/ethereum-optimism/optimism/op-service/eth"
	"github.com/ethereum-optimism/optimism/op-service/testlog"
)

// TestBoundary_L1PreActivationBlock pins that when L1 Amsterdam is configured
// to activate far in the future, every L1
// block mined in the test horizon stays in the legacy format — no
// Header.BlockAccessListHash, no Header.SlotNumber. This pins the regression
// that pre-Amsterdam L1 blocks must keep their old shape even when
// AmsterdamTime is set on ChainConfig.
//
// Scope: L1-side only. We deliberately do not exercise the L2 sequencer
// build path here, because the baseline (320943dfa + op-geth v...20260526)
// is independently broken on L2: the sequencer asks for engine_getPayloadV5
// (Osaka-shaped) and the L2 op-geth has not exposed that method yet. That
// failure also breaks every Arsia test in op-e2e/actions/mantleupgrades on
// the same baseline, and is independent of the L1 Amsterdam wiring under
// test here. The L2 build path is covered by sibling tests under derivation/
// and reorg/
// once both gaps (this one and the L1Miner BAL gap) are resolved.
func TestBoundary_L1PreActivationBlock(gt *testing.T) {
	t := actionsHelpers.NewDefaultTesting(gt)

	// Amsterdam scheduled 24h out — well past anything this test mines.
	amsterdamOffset := hexutil.Uint64(24 * 60 * 60)
	dp := l1compatHelpers.MakeL1GlamsterdamL2ArsiaDeployParams(t, l1compatHelpers.DefaultRollupTestParams(), &amsterdamOffset)
	sd := e2eutils.SetupMantleNormal(t, dp, actionsHelpers.DefaultAlloc)
	logger := testlog.Logger(t, log.LevelDebug)

	// We only need the L1 miner; we still go through SetupMantleReorgTestActors
	// to get a fully-wired miner with the right chain config, but we never drive
	// the L2 sequencer build loop (which is broken on baseline; see test comment).
	_, _, miner, _, _, _, _, _ := actionsHelpers.SetupMantleReorgTestActors(t, dp, sd, logger, true)
	minerCl := miner.L1Client(t, sd.RollupCfg)

	miner.ActL1SetFeeRecipient(common.Address{'P', 'r', 'e'})
	const blocksToMine = 4
	for i := 0; i < blocksToMine; i++ {
		miner.ActEmptyBlock(t)
	}

	head, err := minerCl.L1BlockRefByLabel(t.Ctx(), eth.Unsafe)
	require.NoError(t, err)
	require.Greater(t, head.Number, uint64(0), "L1 must have advanced past genesis")
	require.False(t,
		sd.L1Cfg.Config.IsAmsterdam(new(big.Int).SetUint64(head.Number), head.Time),
		"L1 head must remain pre-Amsterdam (offset=%ds, head.Time=%d)", uint64(amsterdamOffset), head.Time,
	)

	// Inspect the actual L1 header — neither Amsterdam-only field should be set.
	headHeader, err := miner.EthClient().HeaderByHash(t.Ctx(), head.Hash)
	require.NoError(t, err)
	require.Nil(t, headHeader.BlockAccessListHash, "pre-Amsterdam L1 header must not carry BlockAccessListHash")
	require.Nil(t, headHeader.SlotNumber, "pre-Amsterdam L1 header must not carry SlotNumber")
}
