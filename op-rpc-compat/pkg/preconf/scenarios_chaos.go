package preconf

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/ethereum-optimism/optimism/op-rpc-compat/pkg/rpc"

	"github.com/ethereum/go-ethereum/common"
)

// A17 scenarioSequencerStallStaleness exercises op-node stall via the sequencer's admin RPC
// (admin_stopSequencer / admin_startSequencer) — a clean, reversible freeze of block production with
// NO process/supervisord disruption (unlike killing op-node, which risks missing-trie-node corruption).
//
// Contract (geth, staleness gate): while the sequencer is stopped,
//   - within the tolerance window (MantleToleranceDuration=12s): preconf may optimistically return
//     Success (betting production resumes) — those must still land on-chain after restart;
//   - past the window (>12s stale): preconf must be DEFINITIVELY rejected ("preconf checker is not
//     ready"), NOT a hang and NOT a false Success.
//
// reth (PR80, code-confirmed §12.1): no tolerance window — no active build → timeout. Expected
// divergence, recorded in Part B.
//
// This scenario is DESTRUCTIVE (freezes block production ~15s) → NOT in the default --heavy set; run
// explicitly via `--only sequencer_stall_staleness`. Always restarts the sequencer, even on failure.
func (r *Runner) scenarioSequencerStallStaleness(ctx context.Context) {
	const name = "sequencer_stall_staleness"
	opNode := rpc.NewClient(r.cfg.OpNodeURL, "op-node", 15*time.Second)

	active, err := r.adminSequencerActive(ctx, opNode)
	if err != nil {
		r.recordInconclusive(name, "op-node admin RPC is unreachable (%s): %v", r.cfg.OpNodeURL, err)
		return
	}
	if !active {
		r.recordInconclusive(name, "sequencer is not active; restore it before running this scenario")
		return
	}

	// stop sequencer, capture the unsafe head hash needed to restart
	stopHash, err := r.adminStopSequencer(ctx, opNode)
	if err != nil {
		r.record(name, false, "admin_stopSequencer: %v", err)
		return
	}
	// guarantee restart no matter what happens below (idempotent: skip if already active, so it
	// doesn't double-fire after the explicit Phase-3 restart).
	restarted := false
	restart := func() {
		if restarted {
			return
		}
		if active, _ := r.adminSequencerActive(ctx, opNode); active {
			restarted = true
			return
		}
		if _, e := r.adminStartSequencer(ctx, opNode, stopHash); e != nil {
			r.record(name+"_restart", false, "admin_startSequencer failed; manual recovery required (hash=%s): %v", stopHash, e)
			return
		}
		restarted = true
	}
	defer restart()

	// Phase 1 — within tolerance window: optimistic Success is allowed, but must land after restart.
	inWindow, err := r.sendPreconfNative(ctx, r.seq, r.funder, Addr2, smallValue)
	var windowPromise *promise
	windowNote := "no Success response within the grace window (also valid)"
	if err == nil && inWindow != nil && inWindow.Status == "success" {
		p := promiseFrom(inWindow)
		windowPromise = &p
		windowNote = "optimistic Success within the grace window (verify after recovery)"
	}

	// Phase 2 — wait past the 12s tolerance window, then a preconf must be definitively rejected.
	time.Sleep(14 * time.Second)
	_, stErr := r.sendPreconfNative(ctx, r.seq, r.funder, Addr2, smallValue)
	if stErr == nil {
		r.record(name, false, "preconfirmation accepted after more than 12s of staleness; expected checker-not-ready rejection")
		return
	}
	if !strings.Contains(stErr.Error(), "not ready") {
		r.record(name, false, "stale preconfirmation rejected with unexpected error; expected checker not ready: %v", stErr)
		return
	}

	// restart now (also covered by defer) so Phase-1 optimistic tx can settle for verification
	if _, e := r.adminStartSequencer(ctx, opNode, stopHash); e != nil {
		r.record(name, false, "admin_startSequencer recovery failed: %v", e)
		return
	}
	restarted = true
	time.Sleep(3 * time.Second) // let production resume before checking inclusion

	// Phase 3 — if a Phase-1 optimistic Success was returned, it MUST be on-chain after resume.
	if windowPromise != nil {
		if _, ok := r.verifyOnChain(ctx, name, []promise{*windowPromise}); !ok {
			return // verifyOnChain already recorded the false-Success failure
		}
	}
	r.record(name, true, "staleness gate behaved correctly: %s; after 12s it rejected with checker-not-ready, and any grace-window promise landed after recovery (reth has no grace window and is expected to time out in Part B)", windowNote)
}

// ─── op-node admin helpers ───────────────────────────────────────────────────

func (r *Runner) adminSequencerActive(ctx context.Context, opNode *rpc.Client) (bool, error) {
	resp := opNode.Call(ctx, rpc.NewRequest("admin_sequencerActive", []interface{}{}))
	if resp.Error != nil {
		return false, resp.Error
	}
	if resp.Response == nil || resp.Response.Error != nil {
		return false, fmt.Errorf("admin_sequencerActive rpc error")
	}
	var active bool
	if err := json.Unmarshal(resp.Response.Result, &active); err != nil {
		return false, err
	}
	return active, nil
}

func (r *Runner) adminStopSequencer(ctx context.Context, opNode *rpc.Client) (string, error) {
	resp := opNode.Call(ctx, rpc.NewRequest("admin_stopSequencer", []interface{}{}))
	if resp.Error != nil {
		return "", resp.Error
	}
	if resp.Response == nil || resp.Response.Error != nil {
		return "", fmt.Errorf("admin_stopSequencer rpc error")
	}
	var hash string
	if err := json.Unmarshal(resp.Response.Result, &hash); err != nil {
		return "", err
	}
	return hash, nil
}

func (r *Runner) adminStartSequencer(ctx context.Context, opNode *rpc.Client, hash string) (bool, error) {
	resp := opNode.Call(ctx, rpc.NewRequest("admin_startSequencer", []interface{}{hash}))
	if resp.Error != nil {
		return false, resp.Error
	}
	if resp.Response == nil || resp.Response.Error != nil {
		return false, fmt.Errorf("admin_startSequencer rpc error")
	}
	return true, nil
}

// A18 scenarioRecoverModeNoFalseSuccess exercises the no_tx_pool / empty-block path via op-node's
// admin_setRecoverMode(true): the sequencer locks the L1 origin and produces EMPTY blocks with
// attrs.NoTxPool=true (op-node sequencer.go:593 — the exact #2-P0 condition). A preconf submitted
// during recover mode must NOT become a false Success: either it's rejected, or it's held and
// ultimately included once recover mode ends. The ONLY failure is "returned Success but never lands".
//
// Observed on geth (probed): preconf returns Success during recover mode, is held while blocks are
// no_tx_pool/empty, and lands with status=1 after recover mode is disabled — predicted block is off
// by the empty-block gap (a soft-guarantee slip, tolerated), but the HARD invariant holds.
//
// reth (#2 P0, PR80 Stage 3b ungated, §12.1): expected to seal the preconf into a non-canonical
// no_tx_pool block → reorged out → the false Success this test hunts. Part B divergence.
//
// DESTRUCTIVE (empty blocks while enabled) → opt-in via --only. Always disables recover mode.
func (r *Runner) scenarioRecoverModeNoFalseSuccess(ctx context.Context) {
	const name = "recover_mode_no_false_success"
	opNode := rpc.NewClient(r.cfg.OpNodeURL, "op-node", 15*time.Second)

	// preflight: admin reachable + sequencer active
	if active, err := r.adminSequencerActive(ctx, opNode); err != nil {
		r.recordInconclusive(name, "op-node admin RPC is unreachable: %v", err)
		return
	} else if !active {
		r.recordInconclusive(name, "sequencer is not active; skipping")
		return
	}

	// enter recover mode (empty / no_tx_pool blocks). Always disable on exit.
	if err := r.adminSetRecoverMode(ctx, opNode, true); err != nil {
		r.record(name, false, "admin_setRecoverMode(true): %v", err)
		return
	}
	recovered := false
	disable := func() {
		if recovered {
			return
		}
		if e := r.adminSetRecoverMode(ctx, opNode, false); e != nil {
			r.record(name+"_restore", false, "admin_setRecoverMode(false) failed; manual recovery required: %v", e)
			return
		}
		recovered = true
		// recover mode froze the L1 origin; the preconf checker stays "not ready" until its
		// L1-derivation view catches back up past the tolerance window (~30s observed). Wait so a
		// subsequent suite run isn't poisoned by a lagging checker.
		r.waitCheckerReady(ctx, 60*time.Second)
	}
	defer disable()

	time.Sleep(3 * time.Second) // let a couple of empty blocks form so we're squarely in recover mode

	// send a preconf while in recover mode (native transfer to whitelisted Addr2).
	resp, err := r.sendPreconfNative(ctx, r.seq, r.funder, Addr2, smallValue)
	if err != nil {
		// rejected outright — that's a safe outcome (no false Success). Record and finish.
		r.record(name, true, "preconfirmation rejected in recovery mode (no false Success): %v", err)
		return
	}
	if resp.Status != "success" {
		r.record(name, true, "preconfirmation returned non-Success status %q in recovery mode (no false Success)", resp.Status)
		return
	}
	// It returned Success → the HARD promise now applies: it MUST eventually land on-chain.
	preHash := common.HexToHash(resp.TxHash)

	// still in recover mode: expected NOT yet on-chain (empty blocks) — informational, not asserted.
	_, _, _, _, onChainDuring := r.onChainReceiptRaw(ctx, r.seq, preHash)

	// exit recover mode and require the Success'd preconf to land.
	if err := r.adminSetRecoverMode(ctx, opNode, false); err != nil {
		r.record(name, false, "admin_setRecoverMode(false) recovery failed: %v", err)
		return
	}
	recovered = true

	var status, blk uint64
	found := false
	for i := 0; i < 40; i++ {
		if st, b, _, _, f := r.onChainReceiptRaw(ctx, r.seq, preHash); f {
			status, blk, found = st, b, true
			break
		}
		time.Sleep(time.Second)
	}
	if !found {
		r.record(name, false, "false Success: preconfirmation returned Success in recovery mode but did not land within 40s after recovery")
		return
	}
	if status != 1 {
		r.record(name, false, "preconfirmation returned Success in recovery mode but landed with status=%d (expected success)", status)
		return
	}
	predicted := new(big.Int).SetBytes(common.FromHex(resp.BlockHeight)).Uint64()
	r.record(name, true, "no false Success: preconfirmation returned Success in recovery mode (landed during recovery=%v), then landed in block %d with status=1; predicted block %d is a soft guarantee. Reth case #2 is expected to fail", onChainDuring, blk, predicted)
}

// adminSetRecoverMode toggles op-node recover mode (empty / no_tx_pool blocks).
func (r *Runner) adminSetRecoverMode(ctx context.Context, opNode *rpc.Client, mode bool) error {
	resp := opNode.Call(ctx, rpc.NewRequest("admin_setRecoverMode", []interface{}{mode}))
	if resp.Error != nil {
		return resp.Error
	}
	if resp.Response == nil || resp.Response.Error != nil {
		return fmt.Errorf("admin_setRecoverMode rpc error")
	}
	return nil
}

// waitCheckerReady polls until the preconf checker accepts txs again (no "not ready"), up to timeout.
// Uses a tiny native preconf as the probe; a returned Success tx will simply be included normally.
func (r *Runner) waitCheckerReady(ctx context.Context, timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		_, err := r.sendPreconfNative(ctx, r.seq, r.funder, Addr2, smallValue)
		if err == nil || !strings.Contains(err.Error(), "not ready") {
			return
		}
		time.Sleep(3 * time.Second)
	}
}
