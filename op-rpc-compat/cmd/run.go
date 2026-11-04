package cmd

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"math/big"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/ethereum-optimism/optimism/op-rpc-compat/pkg/diff"
	"github.com/ethereum-optimism/optimism/op-rpc-compat/pkg/filters"
	"github.com/ethereum-optimism/optimism/op-rpc-compat/pkg/policy"
	"github.com/ethereum-optimism/optimism/op-rpc-compat/pkg/report"
	"github.com/ethereum-optimism/optimism/op-rpc-compat/pkg/rpc"
	"github.com/ethereum-optimism/optimism/op-rpc-compat/pkg/tx"
	"github.com/ethereum-optimism/optimism/op-rpc-compat/policies"
	"github.com/ethereum-optimism/optimism/op-rpc-compat/testcases"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/fatih/color"
)

var (
	testFile     string
	testcasesDir string
	excludeFiles []string // filenames to exclude, without paths
)

// runTests executes either the RPC corpus or transaction suite.
func runTests() error {
	if txStandardOnly && !txTest {
		return fmt.Errorf("--tx-standard-only requires --tx")
	}
	if diffPolicy != "accepted" && diffPolicy != "strict" {
		return fmt.Errorf("unknown diff policy %q", diffPolicy)
	}
	if suiteMode != "core" && suiteMode != "diagnostic" && suiteMode != "all" {
		return fmt.Errorf("unknown suite %q", suiteMode)
	}
	if txTest && suiteMode == "diagnostic" {
		return fmt.Errorf("--suite diagnostic cannot be combined with --tx")
	}
	registry, err := policy.Load(policies.FS, "accepted/registry.json")
	if err != nil {
		return fmt.Errorf("load accepted registry: %w", err)
	}
	ctx := context.Background()
	clients := rpc.NewClientPair(baselineURL, baselineName, targetURL, targetName, timeout)
	baselineMeta, targetMeta, err := preflightEndpoints(ctx, clients)
	if err != nil {
		return err
	}

	// Keep transaction reports separate unless the caller selected an output path.
	if outputFile == "report.json" && txTest {
		outputFile = "report.tx.json"
	}

	if txTest {
		fmt.Println(color.CyanString("============================================================"))
		fmt.Println(color.CyanString("Transaction tests"))
		fmt.Println(color.CyanString("============================================================"))
		if err := runTransactionTests(clients, baselineMeta, targetMeta, registry); err != nil {
			fmt.Fprintf(os.Stderr, "Transaction tests failed: %v\n", err)
			os.Exit(1)
		}
		return nil
	}

	var allTests []report.TestCase
	var testFiles []string
	var corpus fs.FS

	if testFile != "" {
		testFiles = []string{testFile}
	} else {
		corpus = testcaseFS(testcasesDir)
		files, err := findTestFiles(testcasesDir, excludeFiles)
		if err != nil {
			return fmt.Errorf("find testcase files: %w", err)
		}
		if len(files) == 0 {
			return fmt.Errorf("no testcase files found in %s", testcasesDir)
		}
		testFiles = files
	}

	for _, file := range testFiles {
		var tests []report.TestCase
		var err error
		if testFile != "" {
			tests, err = loadTestFile(file)
		} else {
			tests, err = loadTestFileFS(corpus, filepath.Base(file))
		}
		if err != nil {
			return fmt.Errorf("load testcase file %s: %w", file, err)
		}
		if testFile == "" && testcasesDir == "" {
			for i := range tests {
				tests[i].CorpusID = "embedded"
			}
		}
		allTests = append(allTests, tests...)
	}

	if len(allTests) == 0 {
		return fmt.Errorf("no testcases found")
	}
	selected, excluded := selectSuite(allTests, suiteMode)
	if len(selected) == 0 {
		return fmt.Errorf("no %s cases selected; use --suite all to run the full corpus", suiteMode)
	}
	allTests = selected

	if hasTemplateVars(allTests) {
		client := clients.Baseline
		vars, err := fetchTemplateVars(ctx, client)
		if err != nil {
			allTests = replaceTemplateVars(allTests, nil)
			for i := range allTests {
				if allTests[i].TemplateError != "" {
					allTests[i].TemplateError = fmt.Sprintf("template preflight failed: %v; %s", err, allTests[i].TemplateError)
				}
			}
		} else {
			allTests = replaceTemplateVars(allTests, vars)
			if verbose {
				fmt.Printf("Resolved template variables:\n")
				fmt.Printf("  latest_block_hash: %s\n", vars.LatestBlockHash)
				fmt.Printf("  latest_block_number: %s\n", vars.LatestBlockNumber)
				fmt.Printf("  latest_tx_hash: %s\n", vars.LatestTxHash)
				fmt.Printf("  latest_deposit_tx_hash: %s\n", vars.LatestDepositTxHash)
			}
		}
	}

	if testFile != "" {
		fmt.Printf("Running testcase file: %s (%d cases)\n", testFile, len(allTests))
	} else {
		fmt.Printf("Running all testcase files: %d files, %d cases\n", len(testFiles), len(allTests))
		for _, f := range testFiles {
			fmt.Printf("  - %s\n", filepath.Base(f))
		}
		if len(excludeFiles) > 0 {
			fmt.Printf("Excluded files: %v\n", excludeFiles)
		}
	}

	reporter := report.NewReporter(baselineMeta, targetMeta, verbose)
	if err := reporter.ConfigurePolicy(registry, diffPolicy); err != nil {
		return err
	}
	reporter.SetSuite(suiteMode)
	for _, tc := range excluded {
		reporter.AddExcludedCase(tc, "case belongs to "+caseSuite(tc)+" suite")
	}

	fmt.Printf("\n%s: %s\n", baselineName, baselineURL)
	fmt.Printf("%s: %s\n", targetName, targetURL)
	fmt.Println()

	for _, tc := range allTests {
		if tc.TemplateError != "" {
			reporter.AddInconclusiveResult(tc, nil, tc.TemplateError)
			continue
		}
		if tc.Method == "web3_clientVersion" {
			reporter.AddNotApplicableResult(tc, "client version is recorded during endpoint preflight")
			continue
		}
		req := rpc.NewRequest(tc.Method, tc.Params)
		if filters.IsCreateMethod(tc.Method) {
			responses, err := filters.Verify(ctx, clients, req)
			reporter.AddAssertionResult(tc, responses, err)
			continue
		}
		if tags := caseDynamicTags(tc); len(tags) > 0 {
			compared, differences, compareErr, reason := compareAtStableSnapshot(ctx, clients, req, tags, 3, 100*time.Millisecond)
			if reason != "" {
				reporter.AddInconclusiveResult(tc, compared, reason)
			} else {
				reporter.AddResult(tc, compared, differences, compareErr)
			}
			continue
		}

		compareResult := clients.CompareWithRetry(ctx, req, maxRetries, retryDelay)

		var diffResult *diff.CompareResult
		var compareErr error

		if rpc.SuccessfulResponse(compareResult.BaselineResponse) && rpc.SuccessfulResponse(compareResult.TargetResponse) {
			diffResult, compareErr = diff.Compare(
				compareResult.BaselineResponse.RawBody,
				compareResult.TargetResponse.RawBody,
				diff.DefaultOptions(),
			)
		}

		reporter.AddResult(tc, compareResult, diffResult, compareErr)
	}

	reporter.PrintSummary()

	if outputFile != "" {
		if err := reporter.SaveJSON(outputFile); err != nil {
			return fmt.Errorf("save report: %w", err)
		} else {
			fmt.Printf("\nReport saved to: %s\n", outputFile)
		}
	}

	// Return a failing exit status when any result failed.
	if reporter.HasFailures() {
		os.Exit(1)
	}

	return nil
}

// runTransactionTests executes the transaction and contract suites.
func runTransactionTests(clients *rpc.ClientPair, baselineMeta, targetMeta report.EndpointMetadata, registry *policy.Registry) error {
	ctx := context.Background()

	reporter := report.NewReporter(baselineMeta, targetMeta, verbose)
	if err := reporter.ConfigurePolicy(registry, diffPolicy); err != nil {
		return err
	}

	tester, err := tx.NewTester(clients, txPrivateKey, reporter)
	if err != nil {
		return fmt.Errorf("create transaction tester: %w", err)
	}

	var recipient common.Address
	if txRecipient != "" {
		recipient = common.HexToAddress(txRecipient)
	} else {
		randomBytes := make([]byte, 20)
		if _, err := rand.Read(randomBytes); err != nil {
			return fmt.Errorf("generate random recipient: %w", err)
		}
		recipient = common.BytesToAddress(randomBytes)
	}

	// Preconfirmation recipients must be allowlisted by the sequencer.
	var recipientPreconf common.Address
	if txRecipientPreconf != "" {
		recipientPreconf = common.HexToAddress(txRecipientPreconf)
	} else {
		recipientPreconf = common.HexToAddress("0x71920E3cb420fbD8Ba9a495E6f801c50375ea127")
	}

	amount, ok := new(big.Int).SetString(txAmount, 10)
	if !ok {
		return fmt.Errorf("invalid transfer amount: %s", txAmount)
	}

	balance, err := tester.GetBalance(ctx, tester.BaselineClient(), tester.Builder().Address(), "latest")
	if err != nil {
		return fmt.Errorf("get balance: %w", err)
	}

	fmt.Printf("Sender address: %s\n", tester.Builder().Address().Hex())
	fmt.Printf("Sender balance: %s wei\n", balance.String())
	fmt.Printf("Standard recipient: %s\n", recipient.Hex())
	if includePreconfTransactions(txStandardOnly) {
		fmt.Printf("Preconfirmation recipient: %s\n", recipientPreconf.Hex())
	}
	fmt.Printf("Transfer amount: %s wei\n", amount.String())
	fmt.Println()

	// The sender must fund several test transactions and their gas.
	minRequired := new(big.Int).Mul(amount, big.NewInt(20)) // EIP-7702 scenarios need additional gas headroom
	if balance.Cmp(minRequired) < 0 {
		return fmt.Errorf("insufficient balance: have %s wei, need at least %s wei", balance.String(), minRequired.String())
	}

	var results []*tx.TxTestResult

	txTypes := []tx.TxType{tx.TxTypeLegacy, tx.TxTypeEIP1559}

	for _, txType := range txTypes {
		fmt.Printf("Testing %s transaction (eth_sendRawTransaction)...\n", txType.String())
		result := tester.TestNativeTransfer(ctx, recipient, amount, txType, false)
		printTxTestResult(result)
		results = append(results, result)

		if includePreconfTransactions(txStandardOnly) {
			fmt.Printf("Testing %s transaction (eth_sendRawTransactionWithPreconf)...\n", txType.String())
			preconfResult := tester.TestNativeTransfer(ctx, recipientPreconf, amount, txType, true)
			printTxTestResult(preconfResult)
			results = append(results, preconfResult)
		}
	}

	fmt.Println()
	fmt.Println(color.YellowString("Testing EIP-7702 transactions..."))
	eip7702Results := testEIP7702Transfer(
		ctx,
		tester,
		amount,
		includePreconfTransactions(txStandardOnly),
	)
	for _, result := range eip7702Results {
		printTxTestResult(result)
		results = append(results, result)
	}

	fmt.Println()
	fmt.Println(color.CyanString("============================================================"))
	fmt.Println(color.CyanString("Contract tests"))
	fmt.Println(color.CyanString("============================================================"))
	contractResults := runContractTests(ctx, tester)
	results = append(results, contractResults...)

	fmt.Println()
	fmt.Println(color.CyanString("============================================================"))
	fmt.Println(color.CyanString("Trace tests (one transaction traced on both endpoints)"))
	fmt.Println(color.CyanString("============================================================"))
	for _, result := range tester.TestTraceTransfer(ctx, recipient, amount, tx.DefaultTraceTracerConfigs()) {
		printTxTestResult(result)
		results = append(results, result)
	}

	fmt.Println()
	fmt.Println(color.CyanString("============================================================"))
	fmt.Println(color.CyanString("Txpool rejection tests (MetaTx and EIP-155)"))
	fmt.Println(color.CyanString("============================================================"))
	rejectionResults := runRejectionTests(ctx, tester)
	for _, result := range rejectionResults {
		printTxTestResult(result)
		results = append(results, result)
	}

	fmt.Println()
	fmt.Println(color.CyanString("Checking eth_feeHistory..."))

	compareFeeHistory(ctx, clients, reporter)

	printTxTestSummary(results)
	failedTransactions := recordTransactionResults(reporter, results)

	reporter.PrintSummary()

	if outputFile != "" {
		if err := reporter.SaveJSON(outputFile); err != nil {
			return fmt.Errorf("save report: %w", err)
		} else {
			fmt.Printf("\nReport saved to: %s\n", outputFile)
		}
	}

	// Return a failing exit status when a transaction scenario failed.
	if failedTransactions > 0 || reporter.HasFailures() {
		os.Exit(1)
	}

	return nil
}

func compareFeeHistory(ctx context.Context, clients *rpc.ClientPair, reporter *report.Reporter) {
	params := []interface{}{"0x5", "latest", []float64{25, 75}}
	tc := report.TestCase{Name: "eth_feeHistory_check", Method: "eth_feeHistory", Params: params}
	request := rpc.NewRequest(tc.Method, tc.Params)
	compared, differences, compareErr, reason := compareAtStableSnapshot(ctx, clients, request,
		[]string{"latest"}, 3, 100*time.Millisecond)
	if reason != "" {
		reporter.AddInconclusiveResult(tc, compared, reason)
		return
	}
	reporter.AddResult(tc, compared, differences, compareErr)
}

func recordTransactionResults(reporter *report.Reporter, results []*tx.TxTestResult) int {
	failed := 0
	for _, result := range results {
		if result.Inconclusive {
			continue // the underlying comparison already recorded INCONCLUSIVE
		}
		passed := result.Passed && result.Error == ""
		if !passed {
			failed++
		}
		reporter.AddScenarioResult(result.TestName, result.TxType, passed, result.Error)
	}
	return failed
}

// testEIP7702Transfer checks standard and preconfirmed EIP-7702 transfers.
// EIP-7702 lets an EOA temporarily set code for account abstraction.
func testEIP7702Transfer(
	ctx context.Context,
	tester *tx.Tester,
	amount *big.Int,
	includePreconf bool,
) []*tx.TxTestResult {
	var results []*tx.TxTestResult

	result1 := testEIP7702BasicTransfer(ctx, tester, amount)
	results = append(results, result1)

	if includePreconf {
		result2 := testEIP7702BasicTransferPreconf(ctx, tester, amount)
		results = append(results, result2)
	}

	return results
}

// testEIP7702BasicTransfer checks a transfer with an empty authorization list.
func testEIP7702BasicTransfer(ctx context.Context, tester *tx.Tester, amount *big.Int) *tx.TxTestResult {
	result := &tx.TxTestResult{
		TestName: "EIP-7702 basic transfer",
		TxType:   "EIP-7702",
	}

	// Use Hardhat test account 2 as the recipient.
	recipient := common.HexToAddress("0x3C44CdDdB6a900fa2b585dd299e03d12FA4293BC")

	initialBalance, err := tester.GetBalance(ctx, tester.BaselineClient(), recipient, "latest")
	if err != nil {
		result.Error = fmt.Sprintf("get initial balance: %v", err)
		return result
	}

	transferResult := tester.TestNativeTransfer(ctx, recipient, amount, tx.TxTypeEIP7702, false)
	if transferResult.Error != "" {
		result.Inconclusive = transferResult.Inconclusive
		result.Error = fmt.Sprintf("baseline EIP-7702 transaction failed: %s", transferResult.Error)
		return result
	}

	finalBalance, err := tester.GetBalance(ctx, tester.BaselineClient(), recipient, "latest")
	if err != nil {
		result.Error = fmt.Sprintf("get final balance: %v", err)
		return result
	}

	expectedDelta := new(big.Int).Mul(amount, big.NewInt(2)) // one transfer through each client
	actualDelta := new(big.Int).Sub(finalBalance, initialBalance)

	result.BaselineTxHash = transferResult.BaselineTxHash
	result.TargetTxHash = transferResult.TargetTxHash
	result.BaselineReceipt = transferResult.BaselineReceipt
	result.TargetReceipt = transferResult.TargetReceipt
	result.StateComparison = transferResult.StateComparison

	if actualDelta.Cmp(expectedDelta) == 0 {
		result.Passed = true
	} else {
		result.Error = fmt.Sprintf("balance delta mismatch: expected %s, got %s", expectedDelta.String(), actualDelta.String())
	}

	return result
}

// testEIP7702BasicTransferPreconf checks an EIP-7702 transfer through preconfirmation.
func testEIP7702BasicTransferPreconf(ctx context.Context, tester *tx.Tester, amount *big.Int) *tx.TxTestResult {
	result := &tx.TxTestResult{
		TestName: "EIP-7702 preconfirmed transfer",
		TxType:   "EIP-7702",
	}

	// The recipient must be on the sequencer's preconfirmation allowlist.
	recipient := common.HexToAddress("0x71920E3cb420fbD8Ba9a495E6f801c50375ea127")

	initialBalance, err := tester.GetBalance(ctx, tester.BaselineClient(), recipient, "latest")
	if err != nil {
		result.Error = fmt.Sprintf("get initial balance: %v", err)
		return result
	}

	transferResult := tester.TestNativeTransfer(ctx, recipient, amount, tx.TxTypeEIP7702, true)
	if transferResult.Error != "" {
		result.Inconclusive = transferResult.Inconclusive
		result.Error = fmt.Sprintf("baseline EIP-7702 preconfirmation failed: %s", transferResult.Error)
		return result
	}

	finalBalance, err := tester.GetBalance(ctx, tester.BaselineClient(), recipient, "latest")
	if err != nil {
		result.Error = fmt.Sprintf("get final balance: %v", err)
		return result
	}

	expectedDelta := new(big.Int).Mul(amount, big.NewInt(2)) // one transfer through each client
	actualDelta := new(big.Int).Sub(finalBalance, initialBalance)

	result.BaselineTxHash = transferResult.BaselineTxHash
	result.TargetTxHash = transferResult.TargetTxHash
	result.BaselineReceipt = transferResult.BaselineReceipt
	result.TargetReceipt = transferResult.TargetReceipt
	result.BaselinePreconf = transferResult.BaselinePreconf
	result.TargetPreconf = transferResult.TargetPreconf
	result.StateComparison = transferResult.StateComparison

	if actualDelta.Cmp(expectedDelta) == 0 {
		result.Passed = true
	} else {
		result.Error = fmt.Sprintf("balance delta mismatch: expected %s, got %s", expectedDelta.String(), actualDelta.String())
	}

	return result
}

// printTxTestResult renders one transaction test result.
func printTxTestResult(result *tx.TxTestResult) {
	if result.Inconclusive {
		fmt.Printf("  %s %s: %s\n", color.YellowString("? INCONCLUSIVE"), result.TestName, result.Error)
		return
	}
	if result.Error != "" {
		fmt.Printf("  %s %s: %s\n", color.RedString("✗ FAIL"), result.TestName, result.Error)
		return
	}

	if result.Passed {
		fmt.Printf("  %s %s\n", color.GreenString("✓ PASS"), result.TestName)
	} else {
		fmt.Printf("  %s %s\n", color.RedString("✗ FAIL"), result.TestName)
	}

	if result.BaselineTxHash != "" {
		fmt.Printf("    Baseline TX: %s\n", result.BaselineTxHash)
	}
	if result.TargetTxHash != "" {
		fmt.Printf("    Target TX: %s\n", result.TargetTxHash)
	}

	if result.StateComparison != nil {
		sc := result.StateComparison
		if sc.BalanceMatch {
			fmt.Printf("    Balance delta: %s (matched)\n", color.GreenString(sc.BaselineBalanceDelta.String()))
		} else {
			fmt.Printf("    Balance delta: baseline=%s, target=%s %s\n",
				sc.BaselineBalanceDelta.String(), sc.TargetBalanceDelta.String(), color.RedString("(mismatch)"))
		}
		if sc.GasMatch {
			fmt.Printf("    Gas used: %d (matched)\n", sc.BaselineGasUsed)
		} else {
			fmt.Printf("    Gas used: baseline=%d, target=%d %s\n",
				sc.BaselineGasUsed, sc.TargetGasUsed, color.YellowString("(mismatch)"))
		}
	}

	if result.BaselinePreconf != nil && result.TargetPreconf != nil {
		fmt.Printf("    Preconfirmation status: baseline=%s, target=%s\n",
			result.BaselinePreconf.Status, result.TargetPreconf.Status)
	}
	fmt.Println()
}

// printTxTestSummary renders the transaction suite summary.
func printTxTestSummary(results []*tx.TxTestResult) {
	fmt.Println(color.CyanString("============================================================"))
	fmt.Println(color.CyanString("Transaction test summary"))
	fmt.Println(color.CyanString("============================================================"))

	total := len(results)
	passed := 0
	failed := 0
	inconclusive := 0

	for _, r := range results {
		if r.Inconclusive {
			inconclusive++
		} else if r.Passed && r.Error == "" {
			passed++
		} else {
			failed++
		}
	}

	fmt.Printf("Total: %d | Passed: %s | Failed: %s | Inconclusive: %s\n",
		total,
		color.GreenString("%d", passed),
		color.RedString("%d", failed),
		color.YellowString("%d", inconclusive))

	if failed > 0 {
		fmt.Println("\nFailed tests:")
		for _, r := range results {
			if !r.Inconclusive && (!r.Passed || r.Error != "") {
				fmt.Printf("  ✗ %s", r.TestName)
				if r.Error != "" {
					fmt.Printf(" - %s", r.Error)
				} else if r.StateComparison != nil && len(r.StateComparison.Differences) > 0 {
					fmt.Printf(" - %s", strings.Join(r.StateComparison.Differences, "; "))
				}
				fmt.Println()
			}
		}
	}
	if inconclusive > 0 {
		fmt.Println("\nInconclusive tests:")
		for _, r := range results {
			if r.Inconclusive {
				fmt.Printf("  ? %s - %s\n", r.TestName, r.Error)
			}
		}
	}
}

// runContractTests compares contract deployment, reads, and writes.
func runContractTests(ctx context.Context, tester *tx.Tester) []*tx.TxTestResult {
	var results []*tx.TxTestResult

	fmt.Println(color.CyanString("Testing SimpleStorage contract deployment..."))
	deployResult, err := tester.DeployContract(ctx, tx.SimpleStorageBytecode, "SimpleStorage deployment")
	if err != nil {
		result := &tx.TxTestResult{
			TestName: "SimpleStorage deployment",
			TxType:   "Contract Deploy",
			Error:    fmt.Sprintf("deployment failed: %v", err),
			Passed:   false,
		}
		fmt.Printf("  %s %s: %s\n", color.RedString("✗ FAIL"), result.TestName, result.Error)
		results = append(results, result)
		return results
	}

	deployTxResult := &tx.TxTestResult{
		TestName:        deployResult.TestName,
		TxType:          "Contract Deploy",
		Passed:          deployResult.Success,
		BaselineTxHash:  deployResult.BaselineTxHash,
		TargetTxHash:    deployResult.TargetTxHash,
		BaselineReceipt: deployResult.BaselineReceipt,
		TargetReceipt:   deployResult.TargetReceipt,
		Error:           deployResult.Error,
	}

	if deployResult.Success {
		fmt.Printf("  %s %s\n", color.GreenString("✓ PASS"), deployResult.TestName)
		fmt.Printf("    Baseline TX: %s\n", deployResult.BaselineTxHash)
		fmt.Printf("    Target TX: %s\n", deployResult.TargetTxHash)
		fmt.Printf("    Baseline contract address: %s\n", deployResult.BaselineContractAddress)
		fmt.Printf("    Target contract address: %s\n", deployResult.TargetContractAddress)
		if deployResult.BaselineReceipt != nil && deployResult.TargetReceipt != nil {
			if deployResult.BaselineReceipt.GasUsed == deployResult.TargetReceipt.GasUsed {
				fmt.Printf("    Gas used: %d (matched)\n", deployResult.BaselineReceipt.GasUsed)
			} else {
				fmt.Printf("    Gas used: baseline=%d, target=%d %s\n",
					deployResult.BaselineReceipt.GasUsed,
					deployResult.TargetReceipt.GasUsed,
					color.YellowString("(mismatch)"))
			}
		}
	} else {
		fmt.Printf("  %s %s: %s\n", color.RedString("✗ FAIL"), deployResult.TestName, deployResult.Error)
	}
	results = append(results, deployTxResult)

	if !deployResult.Success {
		return results
	}

	// Each client uses the address created in its own chain state.
	contractAddrBaseline := common.HexToAddress(deployResult.BaselineContractAddress)
	contractAddrTarget := common.HexToAddress(deployResult.TargetContractAddress)

	// The initial stored value should be zero.
	fmt.Println(color.CyanString("\nTesting SimpleStorage.get() (initial value)..."))
	// get() selector: 0x6d4ce63c.
	getCallData := common.FromHex("0x6d4ce63c")
	getResult1, err := tester.CallContract(ctx, contractAddrBaseline, contractAddrTarget, getCallData, "SimpleStorage.get() initial value")
	if err != nil {
		result := &tx.TxTestResult{
			TestName:     "SimpleStorage.get() initial value",
			TxType:       "Contract Call",
			Inconclusive: tx.IsInconclusive(err),
			Error:        fmt.Sprintf("call failed: %v", err),
			Passed:       false,
		}
		printTxTestResult(result)
		results = append(results, result)
	} else {
		result := &tx.TxTestResult{
			TestName: getResult1.TestName,
			TxType:   "Contract Call",
			Passed:   getResult1.Success,
			Error:    getResult1.Error,
		}
		if getResult1.Success && getResult1.ResultMatch {
			fmt.Printf("  %s %s\n", color.GreenString("✓ PASS"), getResult1.TestName)
			fmt.Printf("    Return value: %s (baseline and target match)\n", getResult1.BaselineResult)
		} else if getResult1.Success {
			fmt.Printf("  %s %s\n", color.YellowString("⚠ WARNING"), getResult1.TestName)
			fmt.Printf("    Baseline return value: %s\n", getResult1.BaselineResult)
			fmt.Printf("    Target return value: %s\n", getResult1.TargetResult)
		} else {
			fmt.Printf("  %s %s: %s\n", color.RedString("✗ FAIL"), getResult1.TestName, getResult1.Error)
		}
		results = append(results, result)
	}

	fmt.Println(color.CyanString("\nTesting SimpleStorage.set(42)..."))
	// set(uint256) selector: 0x60fe47b1; 42 is encoded as a 32-byte argument.
	setCallData := common.FromHex("0x60fe47b1000000000000000000000000000000000000000000000000000000000000002a")
	setResult, err := tester.SendContractTransaction(ctx, contractAddrBaseline, contractAddrTarget, setCallData, "SimpleStorage.set(42)")
	if err != nil {
		result := &tx.TxTestResult{
			TestName: "SimpleStorage.set(42)",
			TxType:   "Contract Transaction",
			Error:    fmt.Sprintf("transaction failed: %v", err),
			Passed:   false,
		}
		fmt.Printf("  %s %s: %s\n", color.RedString("✗ FAIL"), result.TestName, result.Error)
		results = append(results, result)
	} else {
		result := &tx.TxTestResult{
			TestName:        setResult.TestName,
			TxType:          "Contract Transaction",
			Passed:          setResult.Success,
			BaselineTxHash:  setResult.BaselineTxHash,
			TargetTxHash:    setResult.TargetTxHash,
			BaselineReceipt: setResult.BaselineReceipt,
			TargetReceipt:   setResult.TargetReceipt,
			Error:           setResult.Error,
		}
		if setResult.Success {
			fmt.Printf("  %s %s\n", color.GreenString("✓ PASS"), setResult.TestName)
			fmt.Printf("    Baseline TX: %s\n", setResult.BaselineTxHash)
			fmt.Printf("    Target TX: %s\n", setResult.TargetTxHash)
			if setResult.BaselineReceipt != nil && setResult.TargetReceipt != nil {
				if setResult.BaselineReceipt.GasUsed == setResult.TargetReceipt.GasUsed {
					fmt.Printf("    Gas used: %d (matched)\n", setResult.BaselineReceipt.GasUsed)
				} else {
					fmt.Printf("    Gas used: baseline=%d, target=%d %s\n",
						setResult.BaselineReceipt.GasUsed,
						setResult.TargetReceipt.GasUsed,
						color.YellowString("(mismatch)"))
				}
			}
		} else {
			fmt.Printf("  %s %s: %s\n", color.RedString("✗ FAIL"), setResult.TestName, setResult.Error)
		}
		results = append(results, result)
	}

	// The value should now be 42.
	fmt.Println(color.CyanString("\nTesting SimpleStorage.get() (after set)..."))
	getResult2, err := tester.CallContract(ctx, contractAddrBaseline, contractAddrTarget, getCallData, "SimpleStorage.get() after set")
	if err != nil {
		result := &tx.TxTestResult{
			TestName:     "SimpleStorage.get() after set",
			TxType:       "Contract Call",
			Inconclusive: tx.IsInconclusive(err),
			Error:        fmt.Sprintf("call failed: %v", err),
			Passed:       false,
		}
		printTxTestResult(result)
		results = append(results, result)
	} else {
		result := &tx.TxTestResult{
			TestName: getResult2.TestName,
			TxType:   "Contract Call",
			Passed:   getResult2.Success && getResult2.ResultMatch,
			Error:    getResult2.Error,
		}

		expectedValue := "0x000000000000000000000000000000000000000000000000000000000000002a"
		if getResult2.Success && getResult2.ResultMatch {
			if getResult2.BaselineResult == expectedValue {
				fmt.Printf("  %s %s\n", color.GreenString("✓ PASS"), getResult2.TestName)
				fmt.Printf("    Return value: 42 (0x2a) - baseline and target match\n")
			} else {
				result.Passed = false
				result.Error = fmt.Sprintf("unexpected return value: expected %s, got %s", expectedValue, getResult2.BaselineResult)
				fmt.Printf("  %s %s: %s\n", color.RedString("✗ FAIL"), getResult2.TestName, result.Error)
			}
		} else if getResult2.Success {
			fmt.Printf("  %s %s\n", color.YellowString("⚠ WARNING"), getResult2.TestName)
			fmt.Printf("    Baseline return value: %s\n", getResult2.BaselineResult)
			fmt.Printf("    Target return value: %s\n", getResult2.TargetResult)
		} else {
			fmt.Printf("  %s %s: %s\n", color.RedString("✗ FAIL"), getResult2.TestName, getResult2.Error)
		}
		results = append(results, result)
	}

	fmt.Println()
	return results
}

// Configuration files are excluded from the testcase corpus.
const knownDiffsFileName = "known_diffs.json"

func testcaseFS(dir string) fs.FS {
	if dir == "" {
		return testcases.FS
	}
	return os.DirFS(dir)
}

// findTestFiles returns JSON cases from a directory, excluding configuration and named files.
func findTestFiles(dir string, excludes []string) ([]string, error) {
	var files []string
	filesystem := testcaseFS(dir)

	excludeSet := make(map[string]bool)
	for _, e := range excludes {
		excludeSet[e] = true
	}
	excludeSet[knownDiffsFileName] = true

	entries, err := fs.ReadDir(filesystem, ".")
	if err != nil {
		return nil, err
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		if excludeSet[entry.Name()] {
			continue
		}
		files = append(files, filepath.Join(dir, entry.Name()))
	}

	// Sort filenames for stable execution order.
	sort.Strings(files)
	return files, nil
}

// loadTestFile decodes one JSON testcase file.
func loadTestFile(file string) ([]report.TestCase, error) {
	return loadTestFileFS(os.DirFS(filepath.Dir(file)), filepath.Base(file))
}

func loadTestFileFS(filesystem fs.FS, file string) ([]report.TestCase, error) {
	data, err := fs.ReadFile(filesystem, file)
	if err != nil {
		return nil, err
	}

	var tests []report.TestCase
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(&tests); err != nil {
		return nil, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, fmt.Errorf("testcase file has trailing JSON: %v", err)
	}
	sum := sha256.Sum256(data)
	for i := range tests {
		tests[i].CorpusID = fmt.Sprintf("sha256:%x", sum)
		tests[i].RequestTemplateSHA256 = policy.RequestDigest(tests[i].Method, tests[i].Params)
	}

	return tests, nil
}

// TemplateVars contains runtime values substituted into testcases.
type TemplateVars struct {
	LatestBlockHash   string `json:"latest_block_hash"`
	LatestBlockNumber string `json:"latest_block_number"`
	LatestTxHash      string `json:"latest_tx_hash"`
	BlockOneRLP       string `json:"block_one_rlp"`
	// LatestDepositTxHash is the first type-0x7e deposit hash in the latest block.
	// It is used to compare deposit receipts between clients.
	LatestDepositTxHash string `json:"latest_deposit_tx_hash"`
}

// fetchTemplateVars obtains testcase substitutions from the reference RPC endpoint.
func fetchTemplateVars(ctx context.Context, client *rpc.Client) (*TemplateVars, error) {
	vars := &TemplateVars{}

	req := rpc.NewRequest("eth_getBlockByNumber", []interface{}{"latest", false})
	resp := client.Call(ctx, req)
	if resp.Error != nil {
		return nil, fmt.Errorf("get latest block: %w", resp.Error)
	}

	var block map[string]interface{}
	if err := json.Unmarshal(resp.RawBody, &block); err != nil {
		return nil, fmt.Errorf("decode block response: %w", err)
	}

	if result, ok := block["result"].(map[string]interface{}); ok {
		if hash, ok := result["hash"].(string); ok {
			vars.LatestBlockHash = hash
		}
		if number, ok := result["number"].(string); ok {
			vars.LatestBlockNumber = number
		}
		if txs, ok := result["transactions"].([]interface{}); ok && len(txs) > 0 {
			if txHash, ok := txs[0].(string); ok {
				vars.LatestTxHash = txHash
			}
		}
	}

	// Fall back to block one when the latest block has no transactions.
	if vars.LatestTxHash == "" {
		req := rpc.NewRequest("eth_getBlockByNumber", []interface{}{"0x1", false})
		resp := client.Call(ctx, req)
		if resp.Error == nil {
			var block map[string]interface{}
			if err := json.Unmarshal(resp.RawBody, &block); err == nil {
				if result, ok := block["result"].(map[string]interface{}); ok {
					if txs, ok := result["transactions"].([]interface{}); ok && len(txs) > 0 {
						if txHash, ok := txs[0].(string); ok {
							vars.LatestTxHash = txHash
						}
					}
				}
			}
		}
	}

	// Find a type-0x7e deposit for receipt comparison, falling back to block one.
	if h := fetchFirstDepositTxHash(ctx, client, "latest"); h != "" {
		vars.LatestDepositTxHash = h
	} else if h := fetchFirstDepositTxHash(ctx, client, "0x1"); h != "" {
		vars.LatestDepositTxHash = h
	}

	// debug_traceBlock consumes raw block RLP. Fetch block 1 from the active devnet instead of
	// pinning bytes whose parent hash changes every time the genesis timestamp changes.
	if rawBlock := fetchRawBlock(ctx, client, "0x1"); rawBlock != "" {
		vars.BlockOneRLP = rawBlock
	}

	return vars, nil
}

func fetchRawBlock(ctx context.Context, client *rpc.Client, blockTag string) string {
	resp := client.Call(ctx, rpc.NewRequest("debug_getRawBlock", []interface{}{blockTag}))
	if resp.Error != nil || resp.Response == nil || resp.Response.Error != nil {
		return ""
	}
	var result struct {
		Result string `json:"result"`
	}
	if err := json.Unmarshal(resp.RawBody, &result); err != nil {
		return ""
	}
	return result.Result
}

// fetchFirstDepositTxHash returns the first type-0x7e deposit hash in a block, or an empty string.
func fetchFirstDepositTxHash(ctx context.Context, client *rpc.Client, blockTag string) string {
	req := rpc.NewRequest("eth_getBlockByNumber", []interface{}{blockTag, true})
	resp := client.Call(ctx, req)
	if resp.Error != nil {
		return ""
	}
	var block map[string]interface{}
	if err := json.Unmarshal(resp.RawBody, &block); err != nil {
		return ""
	}
	result, ok := block["result"].(map[string]interface{})
	if !ok {
		return ""
	}
	txs, ok := result["transactions"].([]interface{})
	if !ok {
		return ""
	}
	for _, t := range txs {
		txObj, ok := t.(map[string]interface{})
		if !ok {
			continue
		}
		if txType, _ := txObj["type"].(string); txType == "0x7e" {
			if h, ok := txObj["hash"].(string); ok {
				return h
			}
		}
	}
	return ""
}

var templatePattern = regexp.MustCompile(`\{\{[a-z_]+\}\}`)

// replaceTemplateVars changes only parameter strings, retaining case identity and numeric types.
func replaceTemplateVars(tests []report.TestCase, vars *TemplateVars) []report.TestCase {
	if vars == nil {
		vars = &TemplateVars{}
	}
	replacements := map[string]string{
		"{{latest_block_hash}}":      vars.LatestBlockHash,
		"{{latest_block_number}}":    vars.LatestBlockNumber,
		"{{latest_tx_hash}}":         vars.LatestTxHash,
		"{{latest_deposit_tx_hash}}": vars.LatestDepositTxHash,
		"{{block_one_rlp}}":          vars.BlockOneRLP,
	}
	resolved := make([]report.TestCase, len(tests))
	for i, tc := range tests {
		resolved[i] = tc
		resolved[i].TemplateError = ""
		if resolved[i].RequestTemplateSHA256 == "" {
			resolved[i].RequestTemplateSHA256 = policy.RequestDigest(tc.Method, tc.Params)
		}
		if containsLatestTemplate(tc.Params) {
			resolved[i].SnapshotTags = append(resolved[i].SnapshotTags, "latest")
		}
		missing := make(map[string]bool)
		resolved[i].Params = replaceParamTemplates(tc.Params, replacements, missing)
		if len(missing) > 0 {
			labels := make([]string, 0, len(missing))
			for placeholder := range missing {
				labels = append(labels, placeholder)
			}
			sort.Strings(labels)
			resolved[i].TemplateError = "missing template variables: " + strings.Join(labels, ", ")
		}
	}
	return resolved
}

func replaceParamTemplates(value any, replacements map[string]string, missing map[string]bool) any {
	switch v := value.(type) {
	case string:
		return templatePattern.ReplaceAllStringFunc(v, func(placeholder string) string {
			if replacement := replacements[placeholder]; replacement != "" {
				return replacement
			}
			missing[placeholder] = true
			return placeholder
		})
	case []any:
		result := make([]any, len(v))
		for i, item := range v {
			result[i] = replaceParamTemplates(item, replacements, missing)
		}
		return result
	case map[string]any:
		result := make(map[string]any, len(v))
		for key, item := range v {
			result[key] = replaceParamTemplates(item, replacements, missing)
		}
		return result
	default:
		return value
	}
}

func containsLatestTemplate(value any) bool {
	switch v := value.(type) {
	case string:
		for _, placeholder := range templatePattern.FindAllString(v, -1) {
			if strings.HasPrefix(placeholder, "{{latest_") {
				return true
			}
		}
	case []any:
		for _, item := range v {
			if containsLatestTemplate(item) {
				return true
			}
		}
	case map[string]any:
		for _, item := range v {
			if containsLatestTemplate(item) {
				return true
			}
		}
	}
	return false
}

func hasTemplateVars(tests []report.TestCase) bool {
	for _, tc := range tests {
		if containsTemplate(tc.Params) {
			return true
		}
	}
	return false
}

func containsTemplate(value any) bool {
	switch v := value.(type) {
	case string:
		return templatePattern.MatchString(v)
	case []any:
		for _, item := range v {
			if containsTemplate(item) {
				return true
			}
		}
	case map[string]any:
		for _, item := range v {
			if containsTemplate(item) {
				return true
			}
		}
	}
	return false
}

func includePreconfTransactions(standardOnly bool) bool {
	return !standardOnly
}

// MantleMetaTxPrefix is the 32-byte prefix used by Mantle MetaTx.
// It contains 14 zero bytes followed by the 18 ASCII bytes of "MantleMetaTxPrefix".
var mantleMetaTxPrefix = [32]byte{
	0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
	0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
	'M', 'a', 'n', 't', 'l', 'e',
	'M', 'e', 't', 'a', 'T', 'x',
	'P', 'r', 'e', 'f', 'i', 'x',
}

// runRejectionTests checks txpool rejection parity between the two clients.
//
// Both should reject unprotected legacy and MetaTx-prefixed transactions,
// while accepting a valid EIP-155 legacy transaction.
func runRejectionTests(ctx context.Context, tester *tx.Tester) []*tx.TxTestResult {
	var results []*tx.TxTestResult

	result := tester.TestTxpoolRejection(
		ctx,
		"txpool_rejects_unprotected_legacy_tx",
		func(nonce uint64) (*types.Transaction, error) {
			recipient := common.HexToAddress("0x0000000000000000000000000000000000000001")
			params := &tx.TxParams{
				Nonce:    nonce,
				GasPrice: big.NewInt(1e9), // 1 gwei
				Gas:      21000,
				To:       &recipient,
				Value:    big.NewInt(1),
			}
			unsignedTx, err := tester.Builder().BuildLegacyTx(params)
			if err != nil {
				return nil, err
			}
			return tester.Builder().SignTxUnprotected(unsignedTx)
		},
		"replay-protected",
	)
	results = append(results, result)

	result = tester.TestTxpoolRejection(
		ctx,
		"txpool_rejects_metatx",
		func(nonce uint64) (*types.Transaction, error) {
			// MetaTx prefix (32 bytes) + 0xF8 (1 byte payload) = 33 bytes
			data := make([]byte, 33)
			copy(data[:32], mantleMetaTxPrefix[:])
			data[32] = 0xF8

			recipient := common.HexToAddress("0x0000000000000000000000000000000000000001")
			params := &tx.TxParams{
				Nonce:                nonce,
				MaxFeePerGas:         big.NewInt(20e9),
				MaxPriorityFeePerGas: big.NewInt(1e9),
				Gas:                  100000,
				To:                   &recipient,
				Value:                big.NewInt(0),
				Data:                 data,
			}
			return tester.Builder().BuildAndSign(tx.TxTypeEIP1559, params)
		},
		"meta tx is disabled",
	)
	results = append(results, result)

	result = tester.TestTxpoolAcceptance(
		ctx,
		"txpool_accepts_eip155_legacy_tx",
		func(nonce uint64) (*types.Transaction, error) {
			recipient := common.HexToAddress("0x0000000000000000000000000000000000000001")
			params := &tx.TxParams{
				Nonce:    nonce,
				GasPrice: big.NewInt(1e9),
				Gas:      21000,
				To:       &recipient,
				Value:    big.NewInt(1),
			}
			// BuildAndSign with TxTypeLegacy uses LatestSignerForChainID → EIP-155
			return tester.Builder().BuildAndSign(tx.TxTypeLegacy, params)
		},
	)
	results = append(results, result)

	// Forwarding nodes should agree on local txpool admission after forwarding a transaction.
	// This scenario requires follower endpoints configured with a sequencer; it does not apply to the sequencer itself.
	result = tester.TestTxpoolForwardedRetention(
		ctx,
		"txpool_forwarded_tx_retention_parity",
		func(nonce uint64) (*types.Transaction, error) {
			recipient := common.HexToAddress("0x000000000000000000000000000000000000dEaD")
			params := &tx.TxParams{
				Nonce:                nonce,
				MaxFeePerGas:         big.NewInt(20e9),
				MaxPriorityFeePerGas: big.NewInt(1e9),
				Gas:                  21000,
				To:                   &recipient,
				Value:                big.NewInt(1),
			}
			return tester.Builder().BuildAndSign(tx.TxTypeEIP1559, params)
		},
	)
	results = append(results, result)

	return results
}
