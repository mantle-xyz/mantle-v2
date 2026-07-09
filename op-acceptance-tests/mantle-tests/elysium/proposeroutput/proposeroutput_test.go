package proposeroutput

import (
	"math/big"
	"testing"
	"time"

	"github.com/ethereum-optimism/optimism/op-chain-ops/devkeys"
	opforks "github.com/ethereum-optimism/optimism/op-core/forks"
	"github.com/ethereum-optimism/optimism/op-devstack/devtest"
	"github.com/ethereum-optimism/optimism/op-devstack/presets"
	"github.com/ethereum-optimism/optimism/op-service/eth"
	gethtypes "github.com/ethereum/go-ethereum/core/types"
)

// TestProposer_OutputRootSubmission proves the L2 output proposer keeps landing its output-root
// submissions on the L1 after the L1 upgrades to Glamsterdam (Amsterdam EL). The Mantle L2 stays
// on Arsia while the L1 runs Glamsterdam; op-proposer (legacy mode -> L2OutputOracle) must keep
// proposing outputs on a post-Amsterdam L1.
//
// After the L1 activates Amsterdam and the L2 advances past the boundary, it scans recent L1
// blocks for a tx FROM the proposer's known address and asserts that tx was mined SUCCESSFULLY on
// a genuinely post-Amsterdam L1 block (IsAmsterdam + the new EIP-7928 BlockAccessListHash /
// EIP-7843 SlotNumber header fields present). So the proposer's output submission survives the L1
// upgrade and lands on a real Glamsterdam L1.
//
// Flips red if: the proposer stops submitting after the boundary (no proposer tx on a
// post-Amsterdam L1 within the deadline), or its submission reverts/fails on the Glamsterdam L1.
func TestProposer_OutputRootSubmission(gt *testing.T) {
	t := devtest.SerialT(gt)
	sys := presets.NewMantleMinimal(t)
	require := t.Require()
	ctx := t.Ctx()

	rollupCfg := sys.L2Chain.Escape().RollupConfig()
	l1Config := sys.L1Network.Escape().ChainConfig()
	require.True(sys.L2Chain.IsMantleForkActive(opforks.MantleElysium), "L2 must run with Mantle Elysium active")
	require.NotNil(l1Config.AmsterdamTime, "L1 AmsterdamTime must be configured")

	// op-proposer submits output-root txs to L1 from its own EOA (a known devkey scoped to the L2 chain).
	proposerAddr := sys.L2Chain.Escape().Keys().Address(devkeys.ProposerRole.Key(rollupCfg.L2ChainID))
	t.Log("proposer L1 address", "addr", proposerAddr)

	// 1) Drive the L1 across the Glamsterdam (Amsterdam) boundary.
	sys.L1EL.WaitForTime(*l1Config.AmsterdamTime)
	t.Log("L1 Amsterdam activated")

	// 2) Let the L2 advance well past the boundary so the proposer has post-Amsterdam L2 blocks to
	//    propose outputs for.
	start := sys.L2EL.BlockRefByLabel(eth.Unsafe).Number
	sys.L2EL.WaitForUnsafe(func(bi eth.BlockInfo) (bool, error) {
		return bi.NumberU64() >= start+10, nil
	})

	// 3) Scan recent L1 blocks for the proposer's output-root submission on a post-Amsterdam L1
	//    block: recover each tx's sender, match the proposer, require a successful receipt, and
	//    require the L1 block itself to be genuinely Glamsterdam.
	l1Eth := sys.L1EL.EthClient()
	signer := gethtypes.LatestSignerForChainID(l1Config.ChainID)
	var (
		found   bool
		l1Block uint64
		txHash  string
	)
	deadline := time.Now().Add(240 * time.Second)
	for !found && time.Now().Before(deadline) {
		head := sys.L1EL.BlockRefByLabel(eth.Unsafe)
		floor := uint64(1)
		if head.Number > 64 {
			floor = head.Number - 64
		}
		for n := head.Number; n >= floor && !found; n-- {
			info, txs, err := l1Eth.InfoAndTxsByNumber(ctx, n)
			require.NoError(err, "read L1 block %d", n)
			// Only trust submissions seen on a genuinely post-Amsterdam L1 block.
			if !l1Config.IsAmsterdam(new(big.Int).SetUint64(info.NumberU64()), info.Time()) {
				continue
			}
			for _, tx := range txs {
				from, err := gethtypes.Sender(signer, tx)
				if err != nil || from != proposerAddr {
					continue
				}
				// The proposer's output-root submission is a contract call carrying calldata (an
				// L2OutputOracle proposeL2Output call), not a plain value transfer.
				if tx.To() == nil || len(tx.Data()) == 0 {
					continue
				}
				rcpt, err := l1Eth.TransactionReceipt(ctx, tx.Hash())
				require.NoErrorf(err, "proposer tx on L1 #%d must have a receipt", info.NumberU64())
				require.Equalf(gethtypes.ReceiptStatusSuccessful, rcpt.Status,
					"proposer output submission on L1 #%d must be mined successfully under Glamsterdam", info.NumberU64())
				hdr := info.Header()
				require.NotNilf(hdr.BlockAccessListHash,
					"proposer submission L1 #%d must carry an EIP-7928 BlockAccessListHash", info.NumberU64())
				require.NotNilf(hdr.SlotNumber,
					"proposer submission L1 #%d must carry an EIP-7843 SlotNumber", info.NumberU64())
				found = true
				l1Block = info.NumberU64()
				txHash = tx.Hash().Hex()
				break
			}
		}
		if !found {
			time.Sleep(time.Second)
		}
	}
	require.True(found, "the proposer must land an output-root submission on a post-Amsterdam L1 block within the deadline")
	t.Log("proposer landed an output-root submission on a Glamsterdam L1", "l1Block", l1Block, "tx", txHash)
}
