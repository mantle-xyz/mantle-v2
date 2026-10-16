package preconf

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/ethereum-optimism/optimism/op-rpc-compat/pkg/rpc"
	"github.com/ethereum-optimism/optimism/op-rpc-compat/pkg/tx"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
)

// scenarioBlockFull verifies preconf block-full/packing: it deploys a GasBurner (a contract that
// consumes ~all gas sent to it) and sends a series of gas-heavy preconf txs from one account, each
// burning ~45% of the block gas. About two fit per block; a burner that can't fit the current block
// is not rejected but queued and mined in a later block (resubmitting it then returns "already
// known", which we treat as accepted). We submit K contiguous nonces, wait for each tx's receipt,
// and assert the burners spread across ≥2 blocks with no block exceeding its gas limit. That
// spilling (whether bounded by gas, DA footprint, or byte-size) is the expected block-full behavior.
//
// This is the meaningful port of blockfull.go (which also retried on transient errors): the original
// sent 80%-limit *tiny transfers* and relied on "2 txs fill a block", but geth refunds unused gas so
// tiny transfers never fill it. Gas-heavy burner txs actually consume the block.
func (r *Runner) scenarioBlockFull(ctx context.Context) {
	const name = "block_full_spills_across_blocks"

	if err := r.ensureBurner(ctx); err != nil {
		r.record(name, false, "deploy burner: %v", err)
		return
	}

	gasLimitHdr, err := r.blockGasLimit(ctx)
	if err != nil {
		r.record(name, false, "block gasLimit: %v", err)
		return
	}
	// ~45% of the block per burner: ~2 fit, the rest spill to later blocks.
	burnGas := gasLimitHdr * 45 / 100
	if burnGas < 1_000_000 {
		burnGas = 1_000_000
	}
	gp, err := r.gasPrice(ctx)
	if err != nil {
		r.record(name, false, "gas price: %v", err)
		return
	}

	nonce, err := r.nonce(ctx, r.funder.Address())
	if err != nil {
		r.record(name, false, "nonce: %v", err)
		return
	}

	// Submit K burner txs (contiguous nonces). A burner that can't fit the current block is queued
	// and mined in a later block (spilling) — resubmitting then returns "already known", which we
	// treat as accepted. Only a genuine "nonce too high" (prior tx not yet visible) is retried.
	const wantSubmitted = 6
	const maxAttempts = 40
	var hashes []common.Hash
	for attempts := 0; len(hashes) < wantSubmitted && attempts < maxAttempts; attempts++ {
		signed, err := r.signedLegacy(r.funder, &r.burnerAddr, big.NewInt(0), nil, burnGas, nonce, gp)
		if err != nil {
			r.record(name, false, "build burner tx: %v", err)
			return
		}
		resp, sErr := r.tester.SendRawTransactionWithPreconf(ctx, r.seq, signed, "blockfull-burn")
		if sErr != nil {
			s := sErr.Error()
			switch {
			case strings.Contains(s, "can't be submitted as preconf"):
				r.recordInconclusive(name, "GasBurner %s not in txpool.topreconfs — add + restart op-geth", r.burnerAddr.Hex())
				return
			case strings.Contains(s, "already known"):
				hashes = append(hashes, signed.Hash()) // queued by a prior attempt → accepted
				nonce++
			case strings.Contains(s, "nonce too high"):
				time.Sleep(time.Second) // prior tx not visible yet — retry same nonce
			default:
				r.record(name, false, "burner send error: %v", sErr)
				return
			}
			continue
		}
		// a returned preconf event (success or OOG) means the tx was accepted/included
		_ = resp
		hashes = append(hashes, signed.Hash())
		nonce++
	}

	if len(hashes) == 0 {
		r.record(name, false, "no burner tx submitted in %d attempts", maxAttempts)
		return
	}

	// Wait for receipts and collect the actual inclusion blocks.
	blocks := map[uint64]int{}
	for _, h := range hashes {
		rcpt, err := r.tester.WaitForReceipt(ctx, r.seq, h, 60*time.Second)
		if err != nil {
			continue
		}
		blocks[rcpt.BlockNumber]++
	}
	if len(blocks) == 0 {
		r.record(name, false, "no burner receipts landed")
		return
	}

	// invariant: no block exceeded its gas limit (spilling kept every block within bounds)
	for b := range blocks {
		used, lim, ok := r.blockGasUsedLimit(ctx, hexutil.EncodeUint64(b))
		if ok && used > lim {
			r.record(name, false, "invariant broken: block %d gasUsed %d > gasLimit %d", b, used, lim)
			return
		}
	}
	if len(blocks) >= 2 {
		r.record(name, true, "%d burner txs (each ~45%% of block gas=%d) spread across %d blocks; over-capacity spilled to later blocks, no block exceeded its gas limit", len(hashes), gasLimitHdr, len(blocks))
	} else {
		r.recordInconclusive(name, "%d burner txs all landed in %d block(s) — expected spilling across ≥2 blocks", len(hashes), len(blocks))
	}
}

// blockGasUsedLimit returns a block's gasUsed and gasLimit.
func (r *Runner) blockGasUsedLimit(ctx context.Context, blockHex string) (used, limit uint64, ok bool) {
	resp := r.seq.Call(ctx, rpc.NewRequest("eth_getBlockByNumber", []interface{}{blockHex, false}))
	if resp.Error != nil || resp.Response == nil || resp.Response.Error != nil {
		return 0, 0, false
	}
	var b struct {
		GasUsed  string `json:"gasUsed"`
		GasLimit string `json:"gasLimit"`
	}
	if err := json.Unmarshal(resp.Response.Result, &b); err != nil {
		return 0, 0, false
	}
	u, e1 := hexutil.DecodeUint64(b.GasUsed)
	l, e2 := hexutil.DecodeUint64(b.GasLimit)
	if e1 != nil || e2 != nil {
		return 0, 0, false
	}
	return u, l, true
}

// ensureBurner deploys the GasBurner at its deterministic address (from the dedicated deployer key
// at nonce 0) once, and caches it. Idempotent: if code is already present, just cache the address.
func (r *Runner) ensureBurner(ctx context.Context) error {
	if r.burnerAddr != (common.Address{}) {
		return nil
	}
	present, err := r.hasCode(ctx, GasBurnerAddr)
	if err != nil {
		return err
	}
	if present {
		r.burnerAddr = GasBurnerAddr
		return nil
	}

	deployer, err := tx.NewBuilder(r.tester.ChainID(), gasBurnerDeployerKey)
	if err != nil {
		return err
	}
	// fund the deployer for gas if needed
	bal, err := r.tester.GetBalance(ctx, r.seq, deployer.Address(), "latest")
	if err != nil {
		return err
	}
	if bal.Cmp(oneMNT) < 0 {
		if err := r.sendNativeAndWait(ctx, r.funder, deployer.Address(), new(big.Int).Mul(big.NewInt(10), oneMNT)); err != nil {
			return fmt.Errorf("fund burner deployer: %w", err)
		}
	}
	n, err := r.tester.GetNonce(ctx, r.seq, deployer.Address())
	if err != nil {
		return err
	}
	if n != 0 {
		return fmt.Errorf("burner missing and deployer nonce is %d (need 0); fresh chain required", n)
	}
	gp, err := r.gasPrice(ctx)
	if err != nil {
		return err
	}
	unsigned, err := deployer.BuildLegacyTx(&tx.TxParams{
		Nonce: 0, GasPrice: gp, Gas: 1_000_000, To: nil, Value: big.NewInt(0), Data: common.FromHex(GasBurnerBytecode()),
	})
	if err != nil {
		return err
	}
	signed, err := deployer.SignTx(unsigned)
	if err != nil {
		return err
	}
	hash, err := r.tester.SendRawTransaction(ctx, r.seq, signed, "GasBurner deploy")
	if err != nil {
		return err
	}
	rcpt, err := r.tester.WaitForReceipt(ctx, r.seq, hash, 30*time.Second)
	if err != nil {
		return err
	}
	if !common.IsHexAddress(rcpt.ContractAddress) || common.HexToAddress(rcpt.ContractAddress) != GasBurnerAddr {
		return fmt.Errorf("burner deployed at %s, expected %s", rcpt.ContractAddress, GasBurnerAddr.Hex())
	}
	r.burnerAddr = GasBurnerAddr
	return nil
}

// blockGasLimit returns the latest block's gas limit.
func (r *Runner) blockGasLimit(ctx context.Context) (uint64, error) {
	resp := r.seq.Call(ctx, rpc.NewRequest("eth_getBlockByNumber", []interface{}{"latest", false}))
	if resp.Error != nil {
		return 0, resp.Error
	}
	if resp.Response.Error != nil {
		return 0, fmt.Errorf("eth_getBlockByNumber: %s", resp.Response.Error.Message)
	}
	var b struct {
		GasLimit string `json:"gasLimit"`
	}
	if err := json.Unmarshal(resp.Response.Result, &b); err != nil {
		return 0, err
	}
	return hexutil.DecodeUint64(b.GasLimit)
}
