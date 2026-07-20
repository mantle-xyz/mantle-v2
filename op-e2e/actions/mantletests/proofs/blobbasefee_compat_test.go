package proofs_test

import (
	"math/big"
	"testing"

	batcherFlags "github.com/ethereum-optimism/optimism/op-batcher/flags"
	"github.com/ethereum-optimism/optimism/op-chain-ops/genesis"
	"github.com/ethereum-optimism/optimism/op-core/predeploys"
	actionsHelpers "github.com/ethereum-optimism/optimism/op-e2e/actions/helpers"
	"github.com/ethereum-optimism/optimism/op-e2e/actions/mantletests/proofs/helpers"
	legacybindings "github.com/ethereum-optimism/optimism/op-e2e/mantlebindings"
	"github.com/ethereum-optimism/optimism/op-node/rollup"
	"github.com/ethereum-optimism/optimism/op-node/rollup/derive"
	"github.com/ethereum-optimism/optimism/op-service/eth"
	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/params"
	"github.com/stretchr/testify/require"
)

// runBlobBaseFeeChainIDFreezeTest end-to-end verifies that the sequencer
// derivation computes blobBaseFee matching the on-chain consensus rules of
// Mantle Arsia mainnet, governed by the return value of
// `op-service/eth/config.go: MantleArsiaL1ChainConfigByChainID(L1ChainID)`.
//
// Consensus discipline:
//   - When Mantle Arsia mainnet went live (2026-04-22), op-geth's
//     MainnetChainConfig was missing BPO entries, causing L1 blob fee to be
//     computed with the wrong fork fraction (~58B× incident). That faulty
//     behaviour is now part of the L2 chain history and must be frozen
//     pre-Elysium.
//   - The current elysium upgrade (src/mantle-v2) freezes the incident-era
//     ChainConfig by hardcoding it in MantleArsiaL1ChainConfigByChainID
//     (mainnet only up to Prague), so that the sequencer and verifier produce
//     identical blobBaseFee values to the historical mainnet during the
//     Arsia && !Elysium window.
//   - The fix can only land via the Elysium hardfork (else branch already
//     switches back to the caller-supplied l1ChainConfig for normal compute).
//
// This test constructs different L1 chain IDs (mainnet=1, sepolia=11155111)
// and verifies derivation paths line up with the consensus rule:
//
//	L1ChainID = 1            → MantleArsiaL1ChainConfigByChainID(1) returns
//	                           hardcoded mainnet config (Cancun/Prague only),
//	                           used as the blob-fee chain config.
//	L1ChainID = 11155111     → returns nil; falls back to caller's
//	                           l1ChainConfig (normal EIP-4844 compute path).
//	(Production sepolia is past the Elysium window and goes through the else
//	branch; this test synthesises an Arsia && !Elysium window to assert the
//	freeze-path behaviour stays deterministic per chain ID.)
func runBlobBaseFeeChainIDFreezeTest(networkName string, l1ChainID uint64) func(*testing.T, *helpers.TestCfg[any]) {
	return func(gt *testing.T, testCfg *helpers.TestCfg[any]) {
		t := actionsHelpers.NewDefaultTesting(gt)

		env := helpers.NewL2ProofEnv(t, testCfg, helpers.NewTestParams(),
			helpers.NewBatcherCfg(
				func(c *actionsHelpers.BatcherCfg) {
					c.DataAvailabilityType = batcherFlags.BlobsType
				},
			),
			func(dp *genesis.DeployConfig) {
				// Override L1 ChainID to exercise both branches of
				// MantleArsiaL1ChainConfigByChainID (mainnet hardcoded vs nil
				// fallback) so derivation can be validated per consensus path.
				dp.L1ChainID = l1ChainID

				// Activate every L1 fork in sequence so derivation crosses all
				// fork boundaries (Cancun → Prague → Osaka → BPO1..4).
				dp.L1CancunTimeOffset = ptr(hexutil.Uint64(0))
				dp.L1PragueTimeOffset = ptr(hexutil.Uint64(12))
				dp.L1OsakaTimeOffset = ptr(hexutil.Uint64(24))
				dp.L1BPO1TimeOffset = ptr(hexutil.Uint64(36))
				dp.L1BPO2TimeOffset = ptr(hexutil.Uint64(48))
				dp.L1BPO3TimeOffset = ptr(hexutil.Uint64(60))
				dp.L1BPO4TimeOffset = ptr(hexutil.Uint64(72))
				dp.L1BlobScheduleConfig = &params.BlobScheduleConfig{
					Cancun: params.DefaultCancunBlobConfig,
					Prague: params.DefaultPragueBlobConfig,
					Osaka:  params.DefaultOsakaBlobConfig,
					BPO1:   params.DefaultBPO1BlobConfig,
					BPO2:   params.DefaultBPO2BlobConfig,
					BPO3:   params.DefaultBPO3BlobConfig,
					BPO4:   params.DefaultBPO4BlobConfig,
				}
				dp.L1GenesisBlockExcessBlobGas = ptr(hexutil.Uint64(1e8))
			},
		)

		miner, sequencer := env.Miner, env.Sequencer

		l1BlockContract, err := legacybindings.NewL1Block(predeploys.L1BlockAddr, env.Engine.EthClient())
		require.NoError(t, err, "%s: bind L2 L1Block contract", networkName)

		atBlockWithHash := func(hash common.Hash) *bind.CallOpts {
			return &bind.CallOpts{BlockHash: hash}
		}

		buildL1ToTime := func(forkTime *uint64) {
			require.NotNil(t, forkTime, "%s: fork time must be configured", networkName)
			h := miner.L1Chain().CurrentHeader()
			for h.Time < *forkTime {
				h = miner.ActEmptyBlock(t).Header()
			}
		}

		// expectedBlobBaseFee mirrors the consensus rule in l1_block_info.go:
		//   1) When MantleArsiaL1ChainConfigByChainID(L1ChainID) is non-nil,
		//      use it (mainnet path; frozen at the incident-era ChainConfig).
		//   2) When nil, fall back to the caller-supplied l1ChainConfig (i.e.
		//      env.Sd.L1Cfg.Config) for the standard EIP-4844 compute path
		//      used by sepolia / devnet.
		//
		// Computed via eth.HeaderBlockInfo(...).BlobBaseFee(cfg) so the
		// invocation path matches the production sequencer call site
		// `block.BlobBaseFee(arsiaL1ChainConfig)` in l1_block_info.go.
		expectedBlobBaseFee := func(l1Origin *types.Header) *big.Int {
			cfg := eth.MantleArsiaL1ChainConfigByChainID(
				eth.ChainIDFromBig(new(big.Int).SetUint64(l1ChainID)),
			)
			if cfg == nil {
				cfg = env.Sd.L1Cfg.Config
			}
			return eth.HeaderBlockInfo(l1Origin).BlobBaseFee(cfg)
		}

		cfg := env.Sd.L1Cfg.Config
		forks := []struct {
			label    string
			forkTime *uint64
		}{
			{"Prague", cfg.PragueTime},
			{"Osaka", cfg.OsakaTime},
			{"BPO1", cfg.BPO1Time},
			{"BPO2", cfg.BPO2Time},
		}

		for _, f := range forks {
			buildL1ToTime(f.forkTime)
			sequencer.ActL2EmptyBlock(t)
			sequencer.ActL1HeadSignal(t)
			sequencer.ActBuildToL1HeadUnsafe(t)

			l2Block := sequencer.L2Unsafe()
			require.Greater(t, l2Block.Number, uint64(1),
				"%s/%s: sequencer did not advance L2", networkName, f.label)

			// 1. Read sequencer-written blobBaseFee from the L2 L1Block contract.
			bbfL2, err := l1BlockContract.BlobBaseFee(atBlockWithHash(l2Block.Hash))
			require.NoError(t, err, "%s/%s: read L1Block.BlobBaseFee", networkName, f.label)

			// 2. Compute expected value from the consensus rule.
			l1Origin := miner.L1Chain().GetHeaderByHash(l2Block.L1Origin.Hash)
			want := expectedBlobBaseFee(l1Origin)

			// 3. Freeze assertion: sequencer derivation must match the consensus
			//    rule exactly. Any drift means a fork from Mantle Arsia mainnet
			//    history (pre-Elysium freeze must be preserved).
			require.Equal(t, want.String(), bbfL2.String(),
				"%s/%s L1ChainID=%d: sequencer derivation drifts from the on-chain "+
					"consensus rule (L1 origin #%d time=%d). Such a change would "+
					"fork the L2 chain from Mantle Arsia mainnet history; pre-Elysium "+
					"must remain frozen.",
				networkName, f.label, l1ChainID, l1Origin.Number, l1Origin.Time)
		}

		env.BatchMineAndSync(t)
	}
}

// Test_ProgramAction_BlobBaseFee_MainnetCompat exercises L1ChainID=1
// (Ethereum Mainnet) so the sequencer derivation goes through the hardcoded
// MantleArsia mainnet config path, asserting alignment with the blobBaseFee
// consensus rule established on Mantle Arsia mainnet at 2026-04-22.
func Test_ProgramAction_BlobBaseFee_MainnetCompat(gt *testing.T) {
	matrix := helpers.NewMatrix[any]()
	matrix.AddDefaultTestCases(nil, helpers.MantleLatestForkOnly,
		runBlobBaseFeeChainIDFreezeTest("mainnet", 1))
	matrix.Run(gt)
}

// Test_ProgramAction_BlobBaseFee_SepoliaCompat exercises L1ChainID=11155111
// (Ethereum Sepolia) so the sequencer derivation falls back to the caller's
// l1ChainConfig (normal EIP-4844 compute path), asserting freeze-path
// determinism for the sepolia chain ID. Production sepolia is past the Elysium
// window and never enters this branch; this test synthesises an
// Arsia && !Elysium window to validate behaviour.
func Test_ProgramAction_BlobBaseFee_SepoliaCompat(gt *testing.T) {
	matrix := helpers.NewMatrix[any]()
	matrix.AddDefaultTestCases(nil, helpers.MantleLatestForkOnly,
		runBlobBaseFeeChainIDFreezeTest("sepolia", 11155111))
	matrix.Run(gt)
}

// liveSnapshotCase pins a real (L2 head ⇄ L1 origin) data point captured from
// Mantle mainnet / sepolia. All fields are hardcoded from a one-shot off-line
// probe, so the test runs as a fast, deterministic unit test without any
// network I/O.
//
// L2 anchor (l2BlockNumber / l2BlockHash) is recorded for traceability only —
// the test does not query the L2 chain at runtime, but these let an operator
// reproduce the snapshot or replay it against an archive node.
//
// To refresh a snapshot — use a SINGLE locked L2 head so the three reads stay
// consistent (public RPCs round-robin backends and can return mismatched values):
//
//	# 1. Lock the L2 head
//	LATEST=$(cast block-number -r <L2_RPC>)
//	# 2. Read L1Block predeploy fields atBlock=LATEST (all three from same head)
//	cast call 0x4200..0015 'number()(uint64)'      -r <L2_RPC> --block $LATEST
//	cast call 0x4200..0015 'timestamp()(uint64)'   -r <L2_RPC> --block $LATEST
//	cast call 0x4200..0015 'blobBaseFee()(uint256)' -r <L2_RPC> --block $LATEST
//	# 3. Fetch the corresponding L1 header and verify timestamps agree
//	cast block <L1_NUMBER> --json -r <L1_RPC>
type liveSnapshotCase struct {
	name string

	// L1 chain identity — drives the MantleArsia hardcoded lookup vs fallback path.
	l1ChainID     uint64
	l1FallbackCfg *params.ChainConfig

	// L2 anchor: the exact L2 head at which the L1Block predeploy was read.
	// Used only for traceability / reproducibility; not consumed by the test.
	l2BlockNumber uint64
	l2BlockHash   string // 0x-prefixed hex

	// L1 origin block snapshot (the actual input to derivation).
	l1BlockNumber   uint64
	l1BlockTime     uint64
	l1ExcessBlobGas uint64

	// The blobBaseFee that the sequencer wrote into the L2 L1Block predeploy
	// at the L2 head above. Stored as decimal string so uint256 values stay
	// readable.
	onChainL2BlobBaseFee string
}

// liveSnapshots is a hand-curated table of (L2 head ⇄ L1 origin) data points
// pulled live from Mantle mainnet / sepolia, replayed in-process via the same
// derive.L1InfoDeposit → L1BlockInfoFromBytes path the production sequencer
// uses.
var liveSnapshots = []liveSnapshotCase{
	{
		// Mantle mainnet, captured 2026-05-28 with a locked L2 head so all
		// reads off the L1Block predeploy come from the same block (avoids
		// public-RPC backend round-robin inconsistency).
		//
		// L1 mainnet is past BPO2; the sequencer is in Arsia && !Elysium so it
		// uses the MantleArsiaL1ChainConfigByChainID hardcoded mainnet config
		// (Cancun/Prague only) — recomputation MUST use the same hardcoded
		// config to match the on-chain value (consensus freeze).
		name:                 "mantle-mainnet@L2#95922093/L1#25194107",
		l1ChainID:            1,
		l1FallbackCfg:        params.MainnetChainConfig,
		l2BlockNumber:        95922093,
		l2BlockHash:          "0xb2f97acb3d13c290b3c4fa6bd416f5a2c99520ec6409f5ac0c367170783ff933",
		l1BlockNumber:        25194107,
		l1BlockTime:          1779974447,
		l1ExcessBlobGas:      192006450,
		onChainL2BlobBaseFee: "44850917468579629",
	},
	{
		// Mantle sepolia, captured 2026-05-28 with the same locked-L2-head
		// procedure. MantleArsiaL1ChainConfigByChainID(11155111) returns nil,
		// so derivation falls back to the caller-supplied l1ChainConfig
		// (params.SepoliaChainConfig with the full BPO schedule).
		name:                 "mantle-sepolia@L2#39214926/L1#10940310",
		l1ChainID:            11155111,
		l1FallbackCfg:        params.SepoliaChainConfig,
		l2BlockNumber:        39214926,
		l2BlockHash:          "0xa0c2715ccd0d35dbd7946527eede15921f2f7344ce28ab56fb1e1b6637b35359",
		l1BlockNumber:        10940310,
		l1BlockTime:          1779974472,
		l1ExcessBlobGas:      237972624,
		onChainL2BlobBaseFee: "699743045",
	},
}

// snapshotRollupCfg builds a minimal rollup.Config that pins the sequencer
// into the Arsia && !Elysium window for the given L1 chain id. All fork times
// except MantleElysiumTime are zero (active from L2 genesis), so any
// non-genesis L2 timestamp passed to L1InfoDeposit lands in the post-Arsia,
// pre-Elysium branch — the exact production path being asserted.
func snapshotRollupCfg(l1ChainID uint64) *rollup.Config {
	zero := uint64(0)
	return &rollup.Config{
		L1ChainID:       new(big.Int).SetUint64(l1ChainID),
		L2ChainID:       big.NewInt(5000),
		BlockTime:       2,
		Genesis:         rollup.Genesis{L2Time: 1},
		EcotoneTime:     &zero,
		IsthmusTime:     &zero,
		JovianTime:      &zero,
		MantleArsiaTime: &zero,
		// MantleElysiumTime intentionally nil — Arsia freeze branch active.
	}
}

// TestLiveNetworkSnapshot_BlobBaseFeeCompat replays each pinned (L1 origin,
// on-chain L2 blobBaseFee) snapshot in-process via the same end-to-end call
// path the production sequencer uses:
//
//	derive.L1InfoDeposit(...)           // build the L1 attributes deposit tx
//	derive.L1BlockInfoFromBytes(...)    // decode the on-wire L1 attributes
//	→ info.BlobBaseFee                  // value that lands in L2 L1Block contract
//
// This shares the exact MantleArsia hardcoded vs fallback selection, the
// PectraBlobScheduleTime fix, the MIN_BLOB_GASPRICE fallback, and the
// MantleArsia marshal/unmarshal round-trip — anything that could drift between
// production sequencer and verifier is caught here.
//
// Catches drift between:
//   - in-tree derivation logic (L1InfoDeposit + L1BlockInfoFromBytes)
//   - what the live Mantle network actually wrote on L2
//
// Failure means a refactor changed sequencer derivation in a way that would
// fork the L2 chain from history — the Arsia 2026-04-22 incident pattern.
//
// No network access during the test: snapshots are captured off-line and
// committed to source. Refresh them periodically (see liveSnapshotCase doc).
func TestLiveNetworkSnapshot_BlobBaseFeeCompat(t *testing.T) {
	for _, snap := range liveSnapshots {
		t.Run(snap.name, func(t *testing.T) {
			excess := snap.l1ExcessBlobGas
			l1Header := &types.Header{
				Number:        new(big.Int).SetUint64(snap.l1BlockNumber),
				Time:          snap.l1BlockTime,
				BaseFee:        big.NewInt(0),
				ExcessBlobGas: &excess,
			}

			// Build the same rollup.Config / inputs the production sequencer
			// would have when assembling the L1 attributes deposit tx for an
			// L2 block whose L1 origin equals l1Header.
			rollupCfg := snapshotRollupCfg(snap.l1ChainID)
			l2Timestamp := snap.l1BlockTime + 1 // any non-genesis L2 ts works
			sysCfg := eth.SystemConfig{}
			blockInfo := eth.HeaderBlockInfo(l1Header)

			// Step 1: build the L1 attributes deposit tx via the exact
			// production entry point.
			depositTx, err := derive.L1InfoDeposit(
				rollupCfg, snap.l1FallbackCfg, sysCfg, 1, blockInfo, l2Timestamp,
			)
			require.NoError(t, err, "%s: L1InfoDeposit", snap.name)

			// Step 2: decode it back through the verifier's inverse function;
			// info.BlobBaseFee is the value the L1Block predeploy receives.
			info, err := derive.L1BlockInfoFromBytes(rollupCfg, l2Timestamp, depositTx.Data)
			require.NoError(t, err, "%s: L1BlockInfoFromBytes", snap.name)
			require.NotNil(t, info.BlobBaseFee, "%s: BlobBaseFee unexpectedly nil", snap.name)

			got := info.BlobBaseFee.String()
			t.Logf("[%s] L2 #%d (%s) ⇄ L1 #%d  excessBlobGas=%d  "+
				"on-chain L2 blobBaseFee=%s  derived=%s",
				snap.name, snap.l2BlockNumber, snap.l2BlockHash,
				snap.l1BlockNumber, snap.l1ExcessBlobGas,
				snap.onChainL2BlobBaseFee, got)

			require.Equal(t, snap.onChainL2BlobBaseFee, got,
				"%s: derived blobBaseFee diverges from live L2 on-chain value "+
					"(L1 #%d excessBlobGas=%d). Sequencer derivation drift detected — "+
					"this is the Arsia 2026-04-22 incident pattern.",
				snap.name, snap.l1BlockNumber, snap.l1ExcessBlobGas)
		})
	}
}
