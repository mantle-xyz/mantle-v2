package preconf

import (
	"context"
	"fmt"
	"math/big"
	"time"

	"github.com/ethereum-optimism/optimism/op-rpc-compat/pkg/rpc"
	"github.com/ethereum-optimism/optimism/op-rpc-compat/pkg/tx"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
)

// Config configures a preconf test run.
type Config struct {
	SequencerURL        string // sequencer preconfirmation endpoint
	TargetVerifierURL   string // target forwarding verifier endpoint
	BaselineVerifierURL string // baseline forwarding verifier endpoint
	OpNodeURL           string // sequencer op-node admin RPC for opt-in scenarios
	L1URL               string // L1 RPC for deposit ordering scenarios
	FunderKey           string // funded account (also a preconf-whitelisted sender): 0xf39F...2266
	Addr1Key            string // preconf-whitelisted sender used for TestPay calls: 0x6F18...BDc5
	Addr3Key            string // ERC20 token owner (approves TestPay, holds minted balance): 0x918a...EC29
	Heavy               bool   // run heavier throughput suites (currently: stress)
	StressCount         int    // number of preconf txs for the stress scenario (0 → default 200)
	Only                string // if non-empty, run only the scenario with this name (setup still runs)
}

// Runner holds the shared harness for the scenarios.
type Runner struct {
	cfg              Config
	tester           *tx.Tester
	funder           *tx.Builder
	addr1            *tx.Builder
	addr3            *tx.Builder
	seq              *rpc.Client
	targetVerifier   *rpc.Client
	baselineVerifier *rpc.Client

	burnerAddr common.Address // GasBurner, deployed lazily by the block-full scenario

	results []result
}

type result struct {
	name   string
	status resultStatus
	detail string
}

type resultStatus string

const (
	resultPassed       resultStatus = "PASS"
	resultFailed       resultStatus = "FAIL"
	resultInconclusive resultStatus = "INCONCLUSIVE"
)

// NewRunner wires the sequencer, verifiers, and per-account signers.
// This suite tracks its own pass/fail outcomes, so the transaction tester has no reporter.
func NewRunner(cfg Config) (*Runner, error) {
	pair := rpc.NewClientPair(cfg.SequencerURL, "sequencer", cfg.TargetVerifierURL, "target-verifier", 30*time.Second)
	tester, err := tx.NewTester(pair, cfg.FunderKey, nil)
	if err != nil {
		return nil, fmt.Errorf("new tester: %w", err)
	}
	addr1, err := tx.NewBuilder(tester.ChainID(), cfg.Addr1Key)
	if err != nil {
		return nil, fmt.Errorf("new addr1 builder: %w", err)
	}
	addr3, err := tx.NewBuilder(tester.ChainID(), cfg.Addr3Key)
	if err != nil {
		return nil, fmt.Errorf("new addr3 builder: %w", err)
	}
	return &Runner{
		cfg:              cfg,
		tester:           tester,
		funder:           tester.Builder(),
		addr1:            addr1,
		addr3:            addr3,
		seq:              tester.BaselineClient(),
		targetVerifier:   tester.TargetClient(),
		baselineVerifier: rpc.NewClient(cfg.BaselineVerifierURL, "baseline-verifier", 30*time.Second),
	}, nil
}

// record logs and stores a scenario outcome.
func (r *Runner) record(name string, pass bool, format string, args ...interface{}) {
	status := resultFailed
	if pass {
		status = resultPassed
	}
	r.recordStatus(name, status, format, args...)
}

func (r *Runner) recordInconclusive(name, format string, args ...interface{}) {
	r.recordStatus(name, resultInconclusive, format, args...)
}

func (r *Runner) recordStatus(name string, status resultStatus, format string, args ...interface{}) {
	detail := fmt.Sprintf(format, args...)
	r.results = append(r.results, result{name: name, status: status, detail: detail})
	mark := "✓ PASS"
	switch status {
	case resultFailed:
		mark = "✗ FAIL"
	case resultInconclusive:
		mark = "? INCONCLUSIVE"
	}
	fmt.Printf("[%s] %s — %s\n", mark, name, detail)
}

func (r *Runner) resultCounts() (passed, failed, inconclusive int) {
	for _, res := range r.results {
		switch res.status {
		case resultPassed:
			passed++
		case resultInconclusive:
			inconclusive++
		default:
			failed++
		}
	}
	return passed, failed, inconclusive
}

// Run executes setup then all default scenarios; returns an error if any scenario failed.
func (r *Runner) Run(ctx context.Context) error {
	fmt.Println("=== preconf: setup ===")
	// Contracts first: TestERC20/TestPay deploy at deterministic addresses from funder nonce 0/1
	// (TestPayAddr is whitelisted in topreconfs at that exact address). ensureFunded sends funder
	// txs too, so on a fresh chain it must run AFTER the deterministic deploys, not before.
	if err := r.ensureContracts(ctx); err != nil {
		return fmt.Errorf("setup contracts: %w", err)
	}
	if err := r.ensureFunded(ctx); err != nil {
		return fmt.Errorf("setup funding: %w", err)
	}
	if err := r.ensureERC20State(ctx); err != nil {
		return fmt.Errorf("setup erc20 state: %w", err)
	}

	fmt.Println("=== preconf: scenarios ===")
	run := func(name string, fn func()) {
		if r.cfg.Only == "" || r.cfg.Only == name {
			fn()
		}
	}
	// destructive scenarios: opt-in via --only ONLY (never in the default/--heavy sweep).
	runOptIn := func(name string, fn func()) {
		if r.cfg.Only == name {
			fn()
		}
	}
	run("valid_native_success", func() { r.scenarioValidNativeSuccess(ctx) })
	run("valid_1559_preconf", func() { r.scenarioValid1559Preconf(ctx) })
	run("valid_7702_preconf", func() { r.scenarioValid7702Preconf(ctx) })
	run("reasons", func() { r.scenarioReasons(ctx) })
	run("whitelist_identification", func() { r.scenarioWhitelistIdentification(ctx) })
	run("preconf_create_rejected", func() { r.scenarioPreconfCreateRejected(ctx) })
	run("predicted_block_matches_actual", func() { r.scenarioPredictedBlockMatches(ctx) })
	run("geth_reth_parity_null_logs", func() { r.scenarioGethRethParity(ctx) })
	if r.cfg.Heavy {
		run("success_onchain_consistency", func() { r.scenarioSuccessOnchainConsistency(ctx, 20) })
		run("success_receipt_full_parity", func() { r.scenarioSuccessReceiptFullParity(ctx) })
		run("worst_case_exec_within_timeout", func() { r.scenarioWorstCaseExecWithinTimeout(ctx) })
		run("preconf_nonce_gap", func() { r.scenarioPreconfNonceGap(ctx) })
		run("preconf_gas_cap_over_2m", func() { r.scenarioPreconfGasCapOver2M(ctx) })
		run("verifier_forward_parity", func() { r.scenarioVerifierForwardParity(ctx) })
		run("stress_throughput", func() { r.scenarioStress(ctx, r.cfg.StressCount) })
		run("concurrent_burst", func() { r.scenarioConcurrentBurst(ctx, 5, 10) })
		run("multi_preconf_same_slot_all_included", func() { r.scenarioMultiPreconfSameSlot(ctx) })
		run("replacement_protection", func() { r.scenarioReplacementProtection(ctx) })
		run("tight_timeout_never_false_success", func() { r.scenarioTightTimeoutEviction(ctx) })
		run("preconf_ordered_before_regular", func() { r.scenarioPreconfOrdering(ctx) })
		run("deposit_ordered_before_user", func() { r.scenarioDepositOrdering(ctx) })
		run("block_full_spills_across_blocks", func() { r.scenarioBlockFull(ctx) })
	}
	// destructive, opt-in only: freezes block production ~15s via op-node admin RPC.
	runOptIn("sequencer_stall_staleness", func() { r.scenarioSequencerStallStaleness(ctx) })
	runOptIn("recover_mode_no_false_success", func() { r.scenarioRecoverModeNoFalseSuccess(ctx) })

	// summary
	passed, failed, inconclusive := r.resultCounts()
	fmt.Printf("\n=== preconf summary: %d passed, %d failed, %d inconclusive ===\n", passed, failed, inconclusive)
	if failed > 0 {
		return fmt.Errorf("%d preconf scenario(s) failed", failed)
	}
	return nil
}

// ─── shared tx / preconf helpers ─────────────────────────────────────────────

func (r *Runner) nonce(ctx context.Context, addr common.Address) (uint64, error) {
	return r.tester.GetNonce(ctx, r.seq, addr)
}

func (r *Runner) gasPrice(ctx context.Context) (*big.Int, error) {
	return r.tester.GetGasPrice(ctx, r.seq)
}

// signedLegacy builds and signs a legacy tx with an explicit gas cap and nonce.
func (r *Runner) signedLegacy(b *tx.Builder, to *common.Address, value *big.Int, data []byte, gas, nonce uint64, gasPrice *big.Int) (*types.Transaction, error) {
	if value == nil {
		value = big.NewInt(0)
	}
	unsigned, err := b.BuildLegacyTx(&tx.TxParams{
		Nonce:    nonce,
		GasPrice: gasPrice,
		Gas:      gas,
		To:       to,
		Value:    value,
		Data:     data,
	})
	if err != nil {
		return nil, err
	}
	return b.SignTx(unsigned)
}

// waitReceiptStatus waits for a receipt on the sequencer and returns its status (1 success / 0 revert).
func (r *Runner) waitReceiptStatus(ctx context.Context, hash common.Hash, timeout time.Duration) (uint64, uint64, error) {
	rcpt, err := r.tester.WaitForReceipt(ctx, r.seq, hash, timeout)
	if err != nil {
		return 0, 0, err
	}
	return rcpt.Status, rcpt.BlockNumber, nil
}
