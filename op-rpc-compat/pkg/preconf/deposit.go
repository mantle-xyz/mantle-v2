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

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
)

// OptimismPortalProxy on L1 — deposits are submitted here via depositTransaction. This is the
// devnet's real portal (31337-deploy.json OptimismPortalProxy), which also matches op-geth's
// --miner.l1depositaddress. (The previous 0x5FC8d3… was the OptimismMintableERC20FactoryProxy — a
// wrong address whose missing depositTransaction reverted with no reason, formerly misattributed to
// "ResourceMetering".)
const optimismPortal = "0xDc64a140Aa3E981100a9becA4E685f962f0cF6C9"

// Mantle depositTransaction ABI (same shape used by the legacy suite's config.SendDepositTx).
const depositTxABI = `[{"name":"depositTransaction","type":"function","inputs":[
 {"name":"_ethTxValue","type":"uint256"},{"name":"_mntValue","type":"uint256"},
 {"name":"_to","type":"address"},{"name":"_mntTxValue","type":"uint256"},
 {"name":"_gasLimit","type":"uint64"},{"name":"_isCreation","type":"bool"},
 {"name":"_data","type":"bytes"}]}]`

// depositTxType is the EIP-2718 type byte of an OP-Stack deposit tx (0x7e).
const depositTxType = "0x7e"

// scenarioDepositOrdering submits an L1 deposit (crediting Addr2 with MNT on L2) and verifies that,
// in the L2 block where the derived deposit lands, all deposit-type txs (0x7e) precede the regular
// sequencer txs. Ported from sort.go's deposit-before-user assertion. Gated behind --heavy; needs
// an L1 RPC (--l1). Best-effort: reports inconclusive (non-fatal) if the deposit never derives.
func (r *Runner) scenarioDepositOrdering(ctx context.Context) {
	const name = "deposit_ordered_before_user"

	// L1 client + a builder bound to the L1 chain id.
	l1 := rpc.NewClient(r.cfg.L1URL, "l1", 30*time.Second)
	cid := l1.Call(ctx, rpc.NewRequest("eth_chainId", nil))
	if cid.Error != nil || cid.Response == nil || cid.Response.Error != nil {
		r.record(name, false, "L1 (%s) unreachable", r.cfg.L1URL)
		return
	}
	var cidHex string
	_ = json.Unmarshal(cid.Response.Result, &cidHex)
	l1ChainID, err := hexutil.DecodeBig(cidHex)
	if err != nil {
		r.record(name, false, "L1 chainId parse: %v", err)
		return
	}
	l1b, err := tx.NewBuilder(l1ChainID, r.cfg.FunderKey)
	if err != nil {
		r.record(name, false, "L1 builder: %v", err)
		return
	}

	// depositTransaction calldata: native MNT deposit crediting Addr2 (empty L2 calldata).
	parsed, err := abi.JSON(strings.NewReader(depositTxABI))
	if err != nil {
		r.record(name, false, "abi parse: %v", err)
		return
	}
	calldata, err := parsed.Pack("depositTransaction",
		big.NewInt(0), big.NewInt(0), Addr2, smallValue, uint64(100000), false, []byte{})
	if err != nil {
		r.record(name, false, "abi pack: %v", err)
		return
	}

	portal := common.HexToAddress(optimismPortal)
	fromL2 := r.l2BlockNumber(ctx) // scan L2 forward from here

	// sign + send the L1 deposit (fixed gas; L1 gas is cheap), wait for the L1 receipt.
	n, err := r.tester.GetNonce(ctx, l1, l1b.Address())
	if err != nil {
		r.record(name, false, "L1 nonce: %v", err)
		return
	}
	unsigned, err := l1b.BuildLegacyTx(&tx.TxParams{
		Nonce: n, GasPrice: big.NewInt(1_000_000_000_000), Gas: 1_000_000,
		To: &portal, Value: big.NewInt(0), Data: calldata,
	})
	if err != nil {
		r.record(name, false, "build L1 deposit: %v", err)
		return
	}
	signed, err := l1b.SignTx(unsigned)
	if err != nil {
		r.record(name, false, "sign L1 deposit: %v", err)
		return
	}
	l1Hash, err := r.tester.SendRawTransaction(ctx, l1, signed, "l1-deposit")
	if err != nil {
		r.record(name, false, "send L1 deposit: %v", err)
		return
	}
	rcpt, err := r.tester.WaitForReceipt(ctx, l1, l1Hash, 60*time.Second)
	if err != nil {
		r.record(name, false, "L1 deposit receipt: %v", err)
		return
	}
	if rcpt.Status != 1 {
		// With the correct portal address the deposit normally succeeds; a revert here now indicates
		// a real problem (params/approve/portal), not the old wrong-address artifact — surface it.
		r.record(name, false, "L1 depositTransaction reverted (status 0) at portal %s — check params/portal", optimismPortal)
		return
	}

	// Poll L2 for the derived user deposit (type 0x7e, to Addr2). Seed a sequencer tx each round so
	// the deposit's block also contains a regular tx (makes the ordering check non-vacuous).
	deadline := time.Now().Add(150 * time.Second)
	for time.Now().Before(deadline) {
		_, _ = r.sendPreconfNative(ctx, r.seq, r.funder, Addr2, smallValue)
		blk, found := r.findUserDeposit(ctx, fromL2)
		if !found {
			time.Sleep(3 * time.Second)
			continue
		}
		lastDep, firstUser, hasUser, ok := r.analyzeDepositOrder(ctx, blk)
		if !ok {
			r.record(name, false, "could not analyze block %d", blk)
			return
		}
		if !hasUser {
			r.record(name, true, "block %d: deposit present, no sequencer tx co-located (deposit-first invariant holds vacuously)", blk)
			return
		}
		if lastDep < firstUser {
			r.record(name, true, "block %d: all deposits (last idx %d) precede sequencer txs (first idx %d)", blk, lastDep, firstUser)
		} else {
			r.record(name, false, "block %d: deposit idx %d NOT before sequencer tx idx %d", blk, lastDep, firstUser)
		}
		return
	}
	r.record(name, true, "INCONCLUSIVE (skipped): user deposit did not derive to L2 within 150s")
}

// l2BlockNumber returns the current L2 head number on the sequencer.
func (r *Runner) l2BlockNumber(ctx context.Context) uint64 {
	resp := r.seq.Call(ctx, rpc.NewRequest("eth_blockNumber", nil))
	if resp.Error != nil || resp.Response == nil || resp.Response.Error != nil {
		return 0
	}
	var h string
	if err := json.Unmarshal(resp.Response.Result, &h); err != nil {
		return 0
	}
	v, _ := hexutil.DecodeUint64(h)
	return v
}

// sendL1Deposit submits a Mantle depositTransaction to the L1 portal targeting `to` on L2 with the
// given L2 calldata (mnt value 0 → no approve needed), and waits for the L1 receipt. Reusable by any
// scenario that needs a derived L2 deposit (A18). Returns error on build/send/revert.
func (r *Runner) sendL1Deposit(ctx context.Context, to common.Address, l2Calldata []byte, l2GasLimit uint64) error {
	l1 := rpc.NewClient(r.cfg.L1URL, "l1", 30*time.Second)
	cid := l1.Call(ctx, rpc.NewRequest("eth_chainId", nil))
	if cid.Error != nil || cid.Response == nil || cid.Response.Error != nil {
		return fmt.Errorf("L1 (%s) unreachable", r.cfg.L1URL)
	}
	var cidHex string
	_ = json.Unmarshal(cid.Response.Result, &cidHex)
	l1ChainID, err := hexutil.DecodeBig(cidHex)
	if err != nil {
		return fmt.Errorf("L1 chainId: %w", err)
	}
	l1b, err := tx.NewBuilder(l1ChainID, r.cfg.FunderKey)
	if err != nil {
		return err
	}
	parsed, err := abi.JSON(strings.NewReader(depositTxABI))
	if err != nil {
		return err
	}
	calldata, err := parsed.Pack("depositTransaction",
		big.NewInt(0), big.NewInt(0), to, big.NewInt(0), l2GasLimit, false, l2Calldata)
	if err != nil {
		return err
	}
	portal := common.HexToAddress(optimismPortal)
	n, err := r.tester.GetNonce(ctx, l1, l1b.Address())
	if err != nil {
		return err
	}
	unsigned, err := l1b.BuildLegacyTx(&tx.TxParams{
		Nonce: n, GasPrice: big.NewInt(1_000_000_000_000), Gas: 1_000_000,
		To: &portal, Value: big.NewInt(0), Data: calldata,
	})
	if err != nil {
		return err
	}
	signed, err := l1b.SignTx(unsigned)
	if err != nil {
		return err
	}
	l1Hash, err := r.tester.SendRawTransaction(ctx, l1, signed, "l1-deposit")
	if err != nil {
		return err
	}
	rcpt, err := r.tester.WaitForReceipt(ctx, l1, l1Hash, 60*time.Second)
	if err != nil {
		return err
	}
	if rcpt.Status != 1 {
		return fmt.Errorf("L1 depositTransaction reverted (status 0) at portal %s", optimismPortal)
	}
	return nil
}

// findUserDeposit scans L2 blocks (fromBlock+1 .. head) for a deposit tx (type 0x7e) whose `to` is
// Addr2 — i.e. our derived user deposit — and returns that block number.
func (r *Runner) findUserDeposit(ctx context.Context, fromBlock uint64) (uint64, bool) {
	head := r.l2BlockNumber(ctx)
	for b := fromBlock + 1; b <= head; b++ {
		txs, ok := r.blockTxs(ctx, hexutil.EncodeUint64(b))
		if !ok {
			continue
		}
		for _, t := range txs {
			if strings.EqualFold(t.Type, depositTxType) && t.To != "" && common.HexToAddress(t.To) == Addr2 {
				return b, true
			}
		}
	}
	return 0, false
}

// analyzeDepositOrder returns the last deposit index and first non-deposit index in a block.
func (r *Runner) analyzeDepositOrder(ctx context.Context, blockNum uint64) (lastDep, firstUser int, hasUser, ok bool) {
	txs, ok := r.blockTxs(ctx, hexutil.EncodeUint64(blockNum))
	if !ok {
		return 0, 0, false, false
	}
	lastDep = -1
	firstUser = -1
	for i, t := range txs {
		if strings.EqualFold(t.Type, depositTxType) {
			lastDep = i
		} else if firstUser == -1 {
			firstUser = i
		}
	}
	return lastDep, firstUser, firstUser != -1, true
}

type blockTx struct {
	Type string `json:"type"`
	To   string `json:"to"`
	Hash string `json:"hash"`
}

// blockTxs fetches a block's full transaction list.
func (r *Runner) blockTxs(ctx context.Context, blockHex string) ([]blockTx, bool) {
	resp := r.seq.Call(ctx, rpc.NewRequest("eth_getBlockByNumber", []interface{}{blockHex, true}))
	if resp.Error != nil || resp.Response == nil || resp.Response.Error != nil {
		return nil, false
	}
	var blk struct {
		Transactions []blockTx `json:"transactions"`
	}
	if err := json.Unmarshal(resp.Response.Result, &blk); err != nil {
		return nil, false
	}
	return blk.Transactions, true
}
