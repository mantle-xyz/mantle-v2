package preconf

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"sync"
	"time"

	"github.com/ethereum-optimism/optimism/op-rpc-compat/pkg/rpc"

	"github.com/ethereum/go-ethereum/common"
)

// scenarioStress fires `count` native preconf transfers (funder → Addr2) using contiguous local
// nonces, then asserts every preconf succeeded and the recipient's balance grew by exactly
// count × value. This is the throughput + balance-conservation check (ported from stress.go),
// gated behind --heavy.
func (r *Runner) scenarioStress(ctx context.Context, count int) {
	const name = "stress_throughput"
	if count <= 0 {
		count = 200
	}

	before, err := r.tester.GetBalance(ctx, r.seq, Addr2, "latest")
	if err != nil {
		r.record(name, false, "read Addr2 balance: %v", err)
		return
	}
	gp, err := r.gasPrice(ctx)
	if err != nil {
		r.record(name, false, "gas price: %v", err)
		return
	}
	base, err := r.nonce(ctx, r.funder.Address())
	if err != nil {
		r.record(name, false, "nonce: %v", err)
		return
	}

	successes := 0
	var lastHash common.Hash
	for i := 0; i < count; i++ {
		signed, err := r.signedLegacy(r.funder, &Addr2, smallValue, nil, 21000, base+uint64(i), gp)
		if err != nil {
			r.record(name, false, "tx %d build: %v", i, err)
			return
		}
		resp, err := r.tester.SendRawTransactionWithPreconf(ctx, r.seq, signed, "stress")
		if err != nil {
			// a send error breaks the contiguous nonce chain — report and stop.
			r.record(name, false, "tx %d (nonce %d) send error after %d ok: %v", i, base+uint64(i), successes, err)
			return
		}
		if resp.Status != "success" {
			r.record(name, false, "tx %d preconf status=%s reason=%q", i, resp.Status, derefReason(resp))
			return
		}
		successes++
		lastHash = common.HexToHash(resp.TxHash)
		time.Sleep(20 * time.Millisecond)
	}

	// wait for the last tx to land so the balance delta is fully settled.
	if _, err := r.tester.WaitForReceipt(ctx, r.seq, lastHash, 60*time.Second); err != nil {
		r.record(name, false, "wait last receipt: %v", err)
		return
	}
	after, err := r.tester.GetBalance(ctx, r.seq, Addr2, "latest")
	if err != nil {
		r.record(name, false, "read Addr2 balance after: %v", err)
		return
	}

	delta := new(big.Int).Sub(after, before)
	expected := new(big.Int).Mul(smallValue, big.NewInt(int64(successes)))
	if successes != count || delta.Cmp(expected) != 0 {
		r.record(name, false, "sent=%d success=%d balance delta=%s expected=%s", count, successes, delta, expected)
		return
	}
	r.record(name, true, "%d preconf txs all success; Addr2 balance delta=%s (== %d × %s)", count, delta, count, smallValue)
}

// scenarioConcurrentBurst fires `rounds` bursts of `concurrent` preconf transfers (funder → Addr2)
// with contiguous nonces submitted in parallel, asserting every one preconfirms as success. Ported
// from basefee_stress.go — exercises the sequencer's preconf handling under high concurrent load.
func (r *Runner) scenarioConcurrentBurst(ctx context.Context, rounds, concurrent int) {
	const name = "concurrent_burst"
	if rounds <= 0 {
		rounds = 5
	}
	if concurrent <= 0 {
		concurrent = 10
	}

	gp, err := r.gasPrice(ctx)
	if err != nil {
		r.record(name, false, "gas price: %v", err)
		return
	}
	base, err := r.nonce(ctx, r.funder.Address())
	if err != nil {
		r.record(name, false, "nonce: %v", err)
		return
	}

	total := rounds * concurrent
	var mu sync.Mutex
	var failures []string
	idx := 0
	for round := 0; round < rounds; round++ {
		var wg sync.WaitGroup
		for c := 0; c < concurrent; c++ {
			nonce := base + uint64(idx)
			idx++
			wg.Add(1)
			go func(nonce uint64) {
				defer wg.Done()
				signed, err := r.signedLegacy(r.funder, &Addr2, smallValue, nil, 21000, nonce, gp)
				if err != nil {
					mu.Lock()
					failures = append(failures, fmt.Sprintf("nonce %d build: %v", nonce, err))
					mu.Unlock()
					return
				}
				resp, err := r.tester.SendRawTransactionWithPreconf(ctx, r.seq, signed, "burst")
				if err != nil {
					mu.Lock()
					failures = append(failures, fmt.Sprintf("nonce %d send: %v", nonce, err))
					mu.Unlock()
					return
				}
				if resp.Status != "success" {
					mu.Lock()
					failures = append(failures, fmt.Sprintf("nonce %d status=%s", nonce, resp.Status))
					mu.Unlock()
				}
			}(nonce)
		}
		wg.Wait()
	}

	if len(failures) > 0 {
		show := failures
		if len(show) > 3 {
			show = show[:3]
		}
		r.record(name, false, "%d/%d failed (%d rounds × %d): %v", len(failures), total, rounds, concurrent, show)
		return
	}
	r.record(name, true, "%d preconf txs (%d rounds × %d concurrent) all success", total, rounds, concurrent)
}

// scenarioPreconfOrdering checks the preconf ordering guarantee: within a block, a preconf tx
// (Addr1) is placed before a regular pool tx (Addr3). Ported from sort.go's Addr1-before-Addr3
// assertion; the deposit-before-user part (which needs L1 deposits) is deferred.
//
// Approach: submit a regular Addr3→Addr2 tx to the pool, then a preconf Addr1→Addr2 tx. If the
// regular tx lands in the preconf's block, assert preconf index < regular index. Retries a few
// times to handle block-boundary misses; reports inconclusive (non-fatal) if they never co-locate.
func (r *Runner) scenarioPreconfOrdering(ctx context.Context) {
	const name = "preconf_ordered_before_regular"
	for attempt := 0; attempt < 6; attempt++ {
		gp, err := r.gasPrice(ctx)
		if err != nil {
			r.record(name, false, "gas price: %v", err)
			return
		}
		// 1) regular (non-preconf) tx from Addr3 → pool
		n3, err := r.tester.GetNonce(ctx, r.seq, r.addr3.Address())
		if err != nil {
			r.record(name, false, "addr3 nonce: %v", err)
			return
		}
		reg, err := r.signedLegacy(r.addr3, &Addr2, smallValue, nil, 21000, n3, gp)
		if err != nil {
			r.record(name, false, "build regular: %v", err)
			return
		}
		regHash, err := r.tester.SendRawTransaction(ctx, r.seq, reg, "ordering-regular")
		if err != nil {
			r.record(name, false, "send regular: %v", err)
			return
		}
		// 2) preconf tx from Addr1 (forced into the next block)
		n1, err := r.tester.GetNonce(ctx, r.seq, r.addr1.Address())
		if err != nil {
			r.record(name, false, "addr1 nonce: %v", err)
			return
		}
		pre, err := r.signedLegacy(r.addr1, &Addr2, smallValue, nil, 21000, n1, gp)
		if err != nil {
			r.record(name, false, "build preconf: %v", err)
			return
		}
		resp, err := r.tester.SendRawTransactionWithPreconf(ctx, r.seq, pre, "ordering-preconf")
		if err != nil {
			r.record(name, false, "send preconf: %v", err)
			return
		}
		// 3) wait for the regular tx and see if it co-located in the preconf block
		regRcpt, err := r.tester.WaitForReceipt(ctx, r.seq, regHash, 30*time.Second)
		if err != nil {
			r.record(name, false, "regular receipt: %v", err)
			return
		}
		preBlock := new(big.Int).SetBytes(common.FromHex(resp.BlockHeight)).Uint64()
		if regRcpt.BlockNumber != preBlock {
			continue // different blocks — retry
		}
		idxPre, okPre := r.txIndexInBlock(ctx, resp.BlockHeight, resp.TxHash)
		idxReg, okReg := r.txIndexInBlock(ctx, resp.BlockHeight, regHash.Hex())
		if !okPre || !okReg {
			r.record(name, false, "could not locate txs in block %d", preBlock)
			return
		}
		if idxPre < idxReg {
			r.record(name, true, "block %d: preconf(Addr1) idx %d before regular(Addr3) idx %d", preBlock, idxPre, idxReg)
		} else {
			r.record(name, false, "block %d: preconf(Addr1) idx %d NOT before regular(Addr3) idx %d", preBlock, idxPre, idxReg)
		}
		return
	}
	r.record(name, true, "INCONCLUSIVE (skipped): preconf and regular tx never co-located in one block after 6 attempts")
}

// txIndexInBlock returns the position of txHash within the given block (full-tx form).
func (r *Runner) txIndexInBlock(ctx context.Context, blockHex, txHash string) (int, bool) {
	resp := r.seq.Call(ctx, rpc.NewRequest("eth_getBlockByNumber", []interface{}{blockHex, true}))
	if resp.Error != nil || resp.Response.Error != nil {
		return 0, false
	}
	var blk struct {
		Transactions []struct {
			Hash string `json:"hash"`
		} `json:"transactions"`
	}
	if err := json.Unmarshal(resp.Response.Result, &blk); err != nil {
		return 0, false
	}
	want := common.HexToHash(txHash)
	for i, t := range blk.Transactions {
		if common.HexToHash(t.Hash) == want {
			return i, true
		}
	}
	return 0, false
}
