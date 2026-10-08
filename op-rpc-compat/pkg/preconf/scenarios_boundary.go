package preconf

import (
	"context"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/ethereum-optimism/optimism/op-rpc-compat/pkg/rpc"
	"github.com/ethereum-optimism/optimism/op-rpc-compat/pkg/tx"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
)

// A1 scenarioWhitelistIdentification: a preconf from a NON-whitelisted sender must be rejected
// immediately (not silently buffered until the preconf timeout). Addr3 (0x918a…EC29) is a funded
// account that is NOT in txpool.frompreconfs, so sending a preconf from it exercises the reject path.
func (r *Runner) scenarioWhitelistIdentification(ctx context.Context) {
	const name = "whitelist_identification"
	gp, err := r.gasPrice(ctx)
	if err != nil {
		r.record(name, false, "gas price: %v", err)
		return
	}
	n, err := r.tester.GetNonce(ctx, r.seq, r.addr3.Address())
	if err != nil {
		r.record(name, false, "addr3 nonce: %v", err)
		return
	}
	signed, err := r.signedLegacy(r.addr3, &Addr2, smallValue, nil, 21000, n, gp)
	if err != nil {
		r.record(name, false, "build: %v", err)
		return
	}
	resp, err := r.tester.SendRawTransactionWithPreconf(ctx, r.seq, signed, "whitelist")

	if err == nil {
		r.record(name, false, "non-allowlisted sender (Addr3) was accepted: status=%s (expected immediate rejection)", resp.Status)
		return
	}
	// Contract: a definitive rejection (whitelist error), NOT a buffered Timeout status. op-geth
	// returns "can't be submitted as preconf tx". (Wall-time isn't asserted: the RPC harness retries
	// errors 3× at 1s, which inflates the measured latency independent of the sequencer.)
	if !strings.Contains(err.Error(), "can't be submitted as preconf") {
		r.record(name, false, "non-allowlisted sender was rejected with an unexpected error; expected whitelist rejection: %v", err)
		return
	}
	r.record(name, true, "non-allowlisted sender received a specific rejection rather than a Timeout status: %v", err)
}

// A4 scenarioSuccessOnchainConsistency: the HARD invariant as a batch post-check — every preconf that
// returned success must be on-chain with status==1 and a matching receipt (logs / gasUsed-if-present).
func (r *Runner) scenarioSuccessOnchainConsistency(ctx context.Context, count int) {
	const name = "success_onchain_consistency"
	if count <= 0 {
		count = 20
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
	var ps []promise
	for i := 0; i < count; i++ {
		signed, err := r.signedLegacy(r.funder, &Addr2, smallValue, nil, 21000, base+uint64(i), gp)
		if err != nil {
			r.record(name, false, "build %d: %v", i, err)
			return
		}
		resp, err := r.tester.SendRawTransactionWithPreconf(ctx, r.seq, signed, "a4")
		if err != nil {
			r.record(name, false, "tx %d (nonce %d) send: %v", i, base+uint64(i), err)
			return
		}
		if resp.Status != "success" {
			r.record(name, false, "tx %d status=%s reason=%q", i, resp.Status, derefReason(resp))
			return
		}
		ps = append(ps, promiseFrom(resp))
		time.Sleep(20 * time.Millisecond)
	}
	if _, ok := r.verifyOnChain(ctx, name, ps); ok {
		r.record(name, true, "all %d Success responses landed on-chain with status=1 and matching logs", len(ps))
	}
}

// A7 scenarioMultiPreconfSameSlot: many preconf txs from one account (contiguous nonce) landing in
// the same building slot — all Success must be included, and the sender's nonce / recipient balance
// must reflect every one (not a subset). On reth this is where a with_cached_prestate state-carry bug
// would drop a tx; on geth it's the reference. If the sequencer gap-rejects contiguous nonces (reth
// nonce-gap precheck), admit<N → INCONCLUSIVE rather than a silent partial pass.
func (r *Runner) scenarioMultiPreconfSameSlot(ctx context.Context) {
	const name = "multi_preconf_same_slot_all_included"
	const N = 10
	gp, err := r.gasPrice(ctx)
	if err != nil {
		r.record(name, false, "gas price: %v", err)
		return
	}
	// Quiesce first: wait for prior scenarios' in-flight funder txs to mine, so the Addr2 balance
	// snapshot isolates A7's own transfers (Addr2 is the shared whitelisted recipient).
	r.waitQuiesce(ctx, r.funder.Address())
	base, err := r.nonce(ctx, r.funder.Address())
	if err != nil {
		r.record(name, false, "nonce: %v", err)
		return
	}
	beforeBal, err := r.tester.GetBalance(ctx, r.seq, Addr2, "latest")
	if err != nil {
		r.record(name, false, "read Addr2 balance: %v", err)
		return
	}
	var ps []promise
	for i := 0; i < N; i++ {
		signed, err := r.signedLegacy(r.funder, &Addr2, smallValue, nil, 21000, base+uint64(i), gp)
		if err != nil {
			r.record(name, false, "build %d: %v", i, err)
			return
		}
		resp, err := r.tester.SendRawTransactionWithPreconf(ctx, r.seq, signed, "a7")
		if err != nil {
			if strings.Contains(err.Error(), "nonce") || strings.Contains(err.Error(), "gap") {
				r.recordInconclusive(name, "transaction %d was rejected (%v); sequencer may not accept concurrent consecutive nonces (reth gap behavior), admitted=%d<%d", i, err, len(ps), N)
				return
			}
			r.record(name, false, "tx %d send: %v", i, err)
			return
		}
		if resp.Status != "success" {
			r.record(name, false, "tx %d status=%s reason=%q", i, resp.Status, derefReason(resp))
			return
		}
		ps = append(ps, promiseFrom(resp))
	}
	blocks, ok := r.verifyOnChain(ctx, name, ps)
	if !ok {
		return
	}
	// state dimension (isolated by the earlier quiesce): Addr2 balance grew by exactly N × value.
	// verifyOnChain already proved every hash is on-chain with status==1; this additionally guards
	// against a dropped state-delta (the #1 failure mode) that wouldn't show in inclusion alone.
	afterBal, err := r.tester.GetBalance(ctx, r.seq, Addr2, "latest")
	if err != nil {
		r.record(name, false, "read Addr2 balance after: %v", err)
		return
	}
	wantBal := new(big.Int).Mul(smallValue, big.NewInt(int64(N)))
	if delta := new(big.Int).Sub(afterBal, beforeBal); delta.Cmp(wantBal) != 0 {
		r.record(name, false, "Addr2 balance delta=%s differs from N*value=%s; a transaction may be missing or state may be contaminated", delta, wantBal)
		return
	}
	// slot-sharing: how many landed in the busiest block.
	perBlock := map[uint64]int{}
	for _, b := range blocks {
		perBlock[b]++
	}
	maxShare := 0
	for _, c := range perBlock {
		if c > maxShare {
			maxShare = c
		}
	}
	if maxShare >= 2 {
		r.record(name, true, "all %d transactions landed with nonce and balance conservation; up to %d shared one slot across %d blocks", N, maxShare, len(perBlock))
	} else {
		r.recordInconclusive(name, "all %d transactions landed with conservation, but each landed in a separate block; block production was too fast to test a shared slot", N)
	}
}

// A10 scenarioReplacementProtection: while a preconf at (from,nonce) is active, a second preconf at
// the same nonce must be rejected (not silently replace the committed one). Either "in process"
// (still Waiting) or "nonce too low" (already included) counts as "not replaced".
func (r *Runner) scenarioReplacementProtection(ctx context.Context) {
	const name = "replacement_protection"
	gp, err := r.gasPrice(ctx)
	if err != nil {
		r.record(name, false, "gas price: %v", err)
		return
	}
	n, err := r.nonce(ctx, r.funder.Address())
	if err != nil {
		r.record(name, false, "nonce: %v", err)
		return
	}
	first, err := r.signedLegacy(r.funder, &Addr2, smallValue, nil, 21000, n, gp)
	if err != nil {
		r.record(name, false, "build first: %v", err)
		return
	}
	if _, err := r.tester.SendRawTransactionWithPreconf(ctx, r.seq, first, "a10-1"); err != nil {
		r.record(name, false, "first send: %v", err)
		return
	}
	// same nonce, different value → replacement attempt.
	second, err := r.signedLegacy(r.funder, &Addr2, big.NewInt(1), nil, 21000, n, gp)
	if err != nil {
		r.record(name, false, "build second: %v", err)
		return
	}
	_, err = r.tester.SendRawTransactionWithPreconf(ctx, r.seq, second, "a10-2")
	if err == nil {
		r.record(name, false, "second transaction with the same nonce was accepted; expected in-process or nonce-too-low rejection")
		return
	}
	r.record(name, true, "second transaction with the same nonce was rejected: %v", err)
}

// A6 scenarioSuccessReceiptFullParity: a successful, log-emitting preconf call's receipt matches the
// on-chain receipt field-by-field (status / blockHeight==predicted / logs / gasUsed-if-present).
func (r *Runner) scenarioSuccessReceiptFullParity(ctx context.Context) {
	const name = "success_receipt_full_parity"
	// addr1 → TestPay.transferTo(Addr3, Addr2, 1): Addr3 has allowance+balance (ensureERC20State),
	// so it succeeds and emits an ERC20 Transfer log.
	data := PayTransferToCalldata(Addr3, Addr2, big.NewInt(1))
	resp, err := r.sendPreconfCall(ctx, r.seq, r.addr1, TestPayAddr, data, 500_000)
	if err != nil {
		r.record(name, false, "send: %v", err)
		return
	}
	if resp.Status != "success" {
		r.record(name, false, "status=%s reason=%q", resp.Status, derefReason(resp))
		return
	}
	predicted, err := hexutil.DecodeUint64(resp.BlockHeight)
	if err != nil {
		r.record(name, false, "bad blockHeight %q: %v", resp.BlockHeight, err)
		return
	}
	hash := common.HexToHash(resp.TxHash)
	var status, blk, gasUsed uint64
	var chainLogs string
	found := false
	for i := 0; i < 30; i++ {
		status, blk, gasUsed, chainLogs, found = r.onChainReceiptRaw(ctx, r.seq, hash)
		if found {
			break
		}
		time.Sleep(time.Second)
	}
	if !found {
		r.record(name, false, "no on-chain receipt (false Success)")
		return
	}
	if status != 1 {
		r.record(name, false, "on-chain status=%d but preconfirmation reported success", status)
		return
	}
	if blk != predicted {
		r.record(name, false, "predicted blockHeight %d differs from actual %d", predicted, blk)
		return
	}
	preLogs := receiptLogsRaw(resp)
	// Compare log CONTENT (address/topics/data), ignoring block-position metadata that only exists
	// on-chain (blockNumber/txHash/logIndex/…) and legitimately isn't in the preconf response.
	preN, chainN := normalizeLogs(preLogs), normalizeLogs(chainLogs)
	if preN != chainN {
		r.record(name, false, "log contents (address/topics/data) differ: pre=%s chain=%s", preN, chainN)
		return
	}
	r.record(name, true, "receipt matches: status=1, blockHeight equals predicted %d, log contents match (ignoring block position metadata), gasUsed=%d", blk, gasUsed)
}

// A8 scenarioTightTimeoutEviction: preconfs that return Timeout must be evicted — NOT on-chain and
// NOT left in the mempool (the timeout cleanup invariant). Needs a preconftimeout below the slow
// tx's exec time. Native/sstore txs are too fast; we use a WorstCase (bn256-ECMUL) preconf whose
// ~190ms exec reliably exceeds a mid-range timeout (e.g. 100ms). If none time out at the current
// timeout, records INCONCLUSIVE (not a false pass). One tx at a time (distinct gas → distinct hash,
// fresh nonce after eviction) avoids nonce gaps.
func (r *Runner) scenarioTightTimeoutEviction(ctx context.Context) {
	const name = "tight_timeout_never_false_success"
	// WorstCase (ECMUL loop, ~4.9 ns/gas) at ~38M gas ≈ 190ms — exceeds a 100ms preconftimeout with
	// margin, unlike GasBurner (sstore, ~3ms) which never reaches a mid-range timeout.
	if err := r.ensureWorstCase(ctx); err != nil {
		r.recordInconclusive(name, "could not deploy WorstCase (%v); skipping", err)
		return
	}
	gp, err := r.gasPrice(ctx)
	if err != nil {
		r.record(name, false, "gas price: %v", err)
		return
	}
	const K = 5
	const slowGas = 38_000_000
	timedOut, evictedOK := 0, 0
	for k := 0; k < K; k++ {
		r.waitQuiesce(ctx, r.funder.Address())
		n, err := r.nonce(ctx, r.funder.Address())
		if err != nil {
			r.record(name, false, "nonce: %v", err)
			return
		}
		// vary gas per iteration → distinct tx hash (so a re-send after eviction isn't "already known")
		signed, err := r.signedLegacy(r.funder, &WorstCaseAddr, big.NewInt(0), nil, uint64(slowGas+k), n, gp)
		if err != nil {
			r.record(name, false, "build %d: %v", k, err)
			return
		}
		resp, err := r.tester.SendRawTransactionWithPreconf(ctx, r.seq, signed, "a8")
		if err != nil {
			continue // transient / already-known — skip
		}
		if resp.Status != "timeout" {
			// executed within the timeout (success or OOG) — let it settle, not a timeout sample
			_, _ = r.tester.WaitForReceipt(ctx, r.seq, common.HexToHash(resp.TxHash), 10*time.Second)
			continue
		}
		timedOut++
		hash := signed.Hash()
		onChain := false
		for i := 0; i < 6; i++ {
			if _, _, _, _, f := r.onChainReceiptRaw(ctx, r.seq, hash); f {
				onChain = true
				break
			}
			time.Sleep(time.Second)
		}
		if onChain {
			r.record(name, false, "timed-out tx %s landed on-chain; it was not evicted as required", hash.Hex())
			return
		}
		if r.txInPool(ctx, r.funder.Address(), hash) {
			r.record(name, false, "timed-out tx %s remains in txpool; it was not evicted as required", hash.Hex())
			return
		}
		evictedOK++
	}
	if timedOut == 0 {
		r.recordInconclusive(name, "none of %d transactions timed out (preconftimeout exceeds WorstCase ~190ms); a 100ms profile is needed to verify eviction", K)
		return
	}
	r.record(name, true, "all %d/%d timed-out transactions were evicted (not on-chain and not in txpool)", evictedOK, timedOut)
}

// A11 scenarioWorstCaseExecWithinTimeout: the worst gas-per-walltime single tx (bn256 ECMUL loop at
// ~block-gas-limit) must execute within the preconf timeout; otherwise cleanup without charging is a
// free-resource DoS. Measured via eth_call latency (≈ sequencer exec time; no whitelist needed).
func (r *Runner) scenarioWorstCaseExecWithinTimeout(ctx context.Context) {
	const name = "worst_case_exec_within_timeout"
	if err := r.ensureWorstCase(ctx); err != nil {
		r.record(name, false, "deploy WorstCase: %v", err)
		return
	}
	addr := WorstCaseAddr
	lim, err := r.blockGasLimit(ctx)
	if err != nil {
		r.record(name, false, "block gaslimit: %v", err)
		return
	}
	gas := lim * 95 / 100 // geth permits nearly the full block gas limit per transaction
	el, ok := r.ethCallElapsed(ctx, addr, nil, gas)
	if !ok {
		r.record(name, false, "eth_call failed (gas=%d)", gas)
		return
	}
	// Anchored to the PRODUCTION preconf timeout (480ms), not whatever test profile is loaded — A11
	// answers "is the production config DoS-safe". (The devnet may run a shorter profile for A8; if
	// worst-case then exceeds it, that tx simply times out + gets evicted — safe, see A8 — not a
	// free-execution DoS.) reth = 200ms but 2M/tx cap → ~10ms.
	const timeoutMs = 480
	usedPct := int(el.Milliseconds()) * 100 / timeoutMs
	if el >= timeoutMs*time.Millisecond {
		r.record(name, false, "worst-case ECMUL transaction (gas=%d) took %v, meeting or exceeding the production timeout of %dms (DoS risk)", gas, el.Round(time.Millisecond), timeoutMs)
		return
	}
	r.record(name, true, "worst-case ECMUL transaction (gas=%d) took %v, below the %dms production timeout (%d%% used); reth's 2M per-transaction cap limits execution to roughly 10ms", gas, el.Round(time.Millisecond), timeoutMs, usedPct)
}

// ─── boundary helpers ────────────────────────────────────────────────────────

// txInPool reports whether `hash` appears in the sender's txpool_contentFrom (pending or queued).
func (r *Runner) txInPool(ctx context.Context, addr common.Address, hash common.Hash) bool {
	resp := r.seq.Call(ctx, rpc.NewRequest("txpool_contentFrom", []interface{}{addr.Hex()}))
	if resp.Error != nil || resp.Response == nil || resp.Response.Error != nil {
		return false
	}
	return strings.Contains(strings.ToLower(string(resp.Response.Result)), strings.ToLower(hash.Hex()))
}

// ethCallElapsed runs eth_call and returns the round-trip latency (≈ sequencer execution time) and
// whether the call returned without error.
func (r *Runner) ethCallElapsed(ctx context.Context, to common.Address, data []byte, gas uint64) (time.Duration, bool) {
	arg := map[string]interface{}{"to": to.Hex(), "gas": hexutil.EncodeUint64(gas)}
	if len(data) > 0 {
		arg["data"] = hexutil.Encode(data)
	}
	t0 := time.Now()
	resp := r.seq.Call(ctx, rpc.NewRequest("eth_call", []interface{}{arg, "latest"}))
	el := time.Since(t0)
	ok := resp.Error == nil && resp.Response != nil && resp.Response.Error == nil
	return el, ok
}

// ensureWorstCase deploys WorstCase at its deterministic address (dedicated deployer key nonce 0),
// idempotent. That fixed address is in txpool.topreconfs, so A8 can send it as a preconf. Also
// reused by A11 (eth_call doesn't need whitelisting, but a stable address keeps setup shared).
func (r *Runner) ensureWorstCase(ctx context.Context) error {
	present, err := r.hasCode(ctx, WorstCaseAddr)
	if err != nil {
		return err
	}
	if present {
		return nil
	}
	deployer, err := tx.NewBuilder(r.tester.ChainID(), worstCaseDeployerKey)
	if err != nil {
		return err
	}
	bal, err := r.tester.GetBalance(ctx, r.seq, deployer.Address(), "latest")
	if err != nil {
		return err
	}
	if bal.Cmp(oneMNT) < 0 {
		if err := r.sendNativeAndWait(ctx, r.funder, deployer.Address(), new(big.Int).Mul(big.NewInt(10), oneMNT)); err != nil {
			return fmt.Errorf("fund worstcase deployer: %w", err)
		}
	}
	n, err := r.tester.GetNonce(ctx, r.seq, deployer.Address())
	if err != nil {
		return err
	}
	if n != 0 {
		return fmt.Errorf("worstcase missing and deployer nonce is %d (need 0); fresh chain required", n)
	}
	gp, err := r.gasPrice(ctx)
	if err != nil {
		return err
	}
	unsigned, err := deployer.BuildLegacyTx(&tx.TxParams{
		Nonce: 0, GasPrice: gp, Gas: 1_000_000, To: nil, Value: big.NewInt(0), Data: common.FromHex(WorstCaseBytecode()),
	})
	if err != nil {
		return err
	}
	signed, err := deployer.SignTx(unsigned)
	if err != nil {
		return err
	}
	hash, err := r.tester.SendRawTransaction(ctx, r.seq, signed, "WorstCase deploy")
	if err != nil {
		return err
	}
	rcpt, err := r.tester.WaitForReceipt(ctx, r.seq, hash, 30*time.Second)
	if err != nil {
		return err
	}
	if !common.IsHexAddress(rcpt.ContractAddress) || common.HexToAddress(rcpt.ContractAddress) != WorstCaseAddr {
		return fmt.Errorf("worstcase deployed at %s, expected %s", rcpt.ContractAddress, WorstCaseAddr.Hex())
	}
	return nil
}
