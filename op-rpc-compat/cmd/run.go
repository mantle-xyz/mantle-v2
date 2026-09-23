package cmd

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io/fs"
	"math/big"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/ethereum-optimism/optimism/op-rpc-compat/pkg/diff"
	"github.com/ethereum-optimism/optimism/op-rpc-compat/pkg/report"
	"github.com/ethereum-optimism/optimism/op-rpc-compat/pkg/rpc"
	"github.com/ethereum-optimism/optimism/op-rpc-compat/pkg/tx"
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
		return fmt.Errorf("--tx-standard-only 需与 --tx 同时使用")
	}

	// Keep transaction reports separate unless the caller selected an output path.
	if outputFile == "report.json" && txTest {
		outputFile = "report.tx.json"
	}

	if txTest {
		fmt.Println(color.CyanString("============================================================"))
		fmt.Println(color.CyanString("交易测试"))
		fmt.Println(color.CyanString("============================================================"))
		if err := runTransactionTests(); err != nil {
			fmt.Fprintf(os.Stderr, "交易测试失败: %v\n", err)
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
			return fmt.Errorf("查找测试文件失败: %w", err)
		}
		if len(files) == 0 {
			return fmt.Errorf("在 %s 目录下未找到测试文件", testcasesDir)
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
			return fmt.Errorf("加载测试文件 %s 失败: %w", file, err)
		}
		allTests = append(allTests, tests...)
	}

	if len(allTests) == 0 {
		return fmt.Errorf("没有测试用例")
	}

	if hasTemplateVars(allTests) {
		ctx := context.Background()
		client := rpc.NewClient(gethURL, "geth", timeout)
		vars, err := fetchTemplateVars(ctx, client)
		if err != nil {
			fmt.Printf("警告: 获取模板变量失败: %v\n", err)
		} else {
			allTests = replaceTemplateVars(allTests, vars)
			if verbose {
				fmt.Printf("已替换模板变量:\n")
				fmt.Printf("  latest_block_hash: %s\n", vars.LatestBlockHash)
				fmt.Printf("  latest_block_number: %s\n", vars.LatestBlockNumber)
				fmt.Printf("  latest_tx_hash: %s\n", vars.LatestTxHash)
				fmt.Printf("  latest_deposit_tx_hash: %s\n", vars.LatestDepositTxHash)
			}
		}
	}

	if testFile != "" {
		fmt.Printf("运行测试文件: %s (%d 个测试)\n", testFile, len(allTests))
	} else {
		fmt.Printf("运行所有测试文件: %d 个文件, %d 个测试\n", len(testFiles), len(allTests))
		for _, f := range testFiles {
			fmt.Printf("  - %s\n", filepath.Base(f))
		}
		if len(excludeFiles) > 0 {
			fmt.Printf("用户排除的文件: %v\n", excludeFiles)
		}
	}

	clients := rpc.NewClientPair(gethURL, rethURL, timeout)

	reporter := report.NewReporter(gethURL, rethURL, verbose)

	// Load known differences from the selected testcase directory.
	knownDiffsPath := filepath.Join(testcasesDir, "known_diffs.json")
	if err := loadKnownDiffsFS(reporter, testcaseFS(testcasesDir)); err == nil {
		fmt.Printf("已加载已知差异配置: %s\n", knownDiffsPath)
	}

	fmt.Printf("\ngeth: %s\n", gethURL)
	fmt.Printf("reth: %s\n", rethURL)
	fmt.Println()

	ctx := context.Background()
	for _, tc := range allTests {
		req := rpc.NewRequest(tc.Method, tc.Params)

		compareResult := clients.CompareWithRetry(ctx, req, maxRetries, retryDelay)

		var diffResult *diff.CompareResult
		var compareErr error

		if compareResult.PrimaryResponse.Error == nil && compareResult.SecondaryResponse.Error == nil {
			diffResult, compareErr = diff.Compare(
				compareResult.PrimaryResponse.RawBody,
				compareResult.SecondaryResponse.RawBody,
				diff.DefaultOptions(),
			)
		}

		reporter.AddResult(tc, compareResult, diffResult, compareErr)
	}

	reporter.PrintSummary()

	if outputFile != "" {
		if err := reporter.SaveJSON(outputFile); err != nil {
			fmt.Fprintf(os.Stderr, "保存报告失败: %v\n", err)
		} else {
			fmt.Printf("\n报告已保存到: %s\n", outputFile)
		}
	}

	// Return a failing exit status when any result failed.
	if reporter.HasFailures() {
		os.Exit(1)
	}

	return nil
}

// runTransactionTests executes the transaction and contract suites.
func runTransactionTests() error {
	ctx := context.Background()

	reporter := report.NewReporter(gethURL, rethURL, verbose)

	if err := loadKnownDiffsFS(reporter, testcaseFS(testcasesDir)); err != nil {
		fmt.Printf("警告: 加载已知差异配置失败: %v\n", err)
	}

	tester, err := tx.NewTester(gethURL, rethURL, txPrivateKey, reporter)
	if err != nil {
		return fmt.Errorf("创建交易测试器失败: %w", err)
	}

	var recipient common.Address
	if txRecipient != "" {
		recipient = common.HexToAddress(txRecipient)
	} else {
		randomBytes := make([]byte, 20)
		rand.Read(randomBytes)
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
		return fmt.Errorf("无效的转账金额: %s", txAmount)
	}

	balance, err := tester.GetBalance(ctx, tester.GethClient(), tester.Builder().Address(), "latest")
	if err != nil {
		return fmt.Errorf("获取余额失败: %w", err)
	}

	fmt.Printf("发送者地址: %s\n", tester.Builder().Address().Hex())
	fmt.Printf("发送者余额: %s wei\n", balance.String())
	fmt.Printf("接收者地址（标准）: %s\n", recipient.Hex())
	if includePreconfTransactions(txStandardOnly) {
		fmt.Printf("接收者地址（预确认）: %s\n", recipientPreconf.Hex())
	}
	fmt.Printf("转账金额: %s wei\n", amount.String())
	fmt.Println()

	// The sender must fund several test transactions and their gas.
	minRequired := new(big.Int).Mul(amount, big.NewInt(20)) // EIP-7702 scenarios need additional gas headroom
	if balance.Cmp(minRequired) < 0 {
		return fmt.Errorf("余额不足: 当前 %s wei, 需要至少 %s wei", balance.String(), minRequired.String())
	}

	var results []*tx.TxTestResult

	txTypes := []tx.TxType{tx.TxTypeLegacy, tx.TxTypeEIP1559}

	for _, txType := range txTypes {
		fmt.Printf("📤 测试 %s 交易 (eth_sendRawTransaction)...\n", txType.String())
		result := tester.TestNativeTransfer(ctx, recipient, amount, txType, false)
		printTxTestResult(result)
		results = append(results, result)

		if includePreconfTransactions(txStandardOnly) {
			fmt.Printf("📤 测试 %s 交易 (eth_sendRawTransactionWithPreconf)...\n", txType.String())
			preconfResult := tester.TestNativeTransfer(ctx, recipientPreconf, amount, txType, true)
			printTxTestResult(preconfResult)
			results = append(results, preconfResult)
		}
	}

	fmt.Println()
	fmt.Println(color.YellowString("📜 测试 EIP-7702 交易..."))
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
	fmt.Println(color.CyanString("合约测试"))
	fmt.Println(color.CyanString("============================================================"))
	contractResults := runContractTests(ctx, tester)
	for _, result := range contractResults {
		results = append(results, result)
	}

	fmt.Println()
	fmt.Println(color.CyanString("============================================================"))
	fmt.Println(color.CyanString("Txpool 拒绝测试（MetaTx + EIP-155）"))
	fmt.Println(color.CyanString("============================================================"))
	rejectionResults := runRejectionTests(ctx, tester)
	for _, result := range rejectionResults {
		printTxTestResult(result)
		results = append(results, result)
	}

	fmt.Println()
	fmt.Println(color.CyanString("🔍 检查 eth_feeHistory..."))

	fhReq := rpc.NewRequest("eth_feeHistory", []interface{}{"0x5", "latest", []float64{25, 75}})
	fhGethResp := tester.GethClient().Call(ctx, fhReq)
	fhRethResp := tester.RethClient().Call(ctx, fhReq)

	fhCompareResult := &rpc.CompareResult{
		Request:           fhReq,
		PrimaryResponse:   fhGethResp,
		SecondaryResponse: fhRethResp,
	}

	var fhDiffResult *diff.CompareResult
	var fhCompareErr error
	if fhGethResp.Response.Error == nil && fhRethResp.Response.Error == nil {
		fhDiffResult, fhCompareErr = diff.Compare(
			fhGethResp.RawBody,
			fhRethResp.RawBody,
			diff.DefaultOptions(),
		)
	}

	fhTC := report.TestCase{
		Name:   "eth_feeHistory_check",
		Method: "eth_feeHistory",
		Params: []interface{}{"0x5", "latest", []float64{25, 75}},
	}
	reporter.AddResult(fhTC, fhCompareResult, fhDiffResult, fhCompareErr)

	printTxTestSummary(results)

	reporter.PrintSummary()

	if outputFile != "" {
		if err := reporter.SaveJSON(outputFile); err != nil {
			fmt.Fprintf(os.Stderr, "保存报告失败: %v\n", err)
		} else {
			fmt.Printf("\n报告已保存到: %s\n", outputFile)
		}
	}

	// Return a failing exit status when a transaction scenario failed.
	if reporter.HasFailures() {
		os.Exit(1)
	}

	return nil
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
		TestName: "EIP-7702 基本转账",
		TxType:   "EIP-7702",
	}

	// Use Hardhat test account 2 as the recipient.
	recipient := common.HexToAddress("0x3C44CdDdB6a900fa2b585dd299e03d12FA4293BC")

	initialBalance, err := tester.GetBalance(ctx, tester.GethClient(), recipient, "latest")
	if err != nil {
		result.Error = fmt.Sprintf("获取初始余额失败: %v", err)
		return result
	}

	gethResult := tester.TestNativeTransfer(ctx, recipient, amount, tx.TxTypeEIP7702, false)
	if gethResult.Error != "" {
		result.Error = fmt.Sprintf("Geth EIP-7702 交易失败: %s", gethResult.Error)
		return result
	}

	finalBalance, err := tester.GetBalance(ctx, tester.GethClient(), recipient, "latest")
	if err != nil {
		result.Error = fmt.Sprintf("获取最终余额失败: %v", err)
		return result
	}

	expectedDelta := new(big.Int).Mul(amount, big.NewInt(2)) // one transfer through each client
	actualDelta := new(big.Int).Sub(finalBalance, initialBalance)

	result.GethTxHash = gethResult.GethTxHash
	result.RethTxHash = gethResult.RethTxHash
	result.GethReceipt = gethResult.GethReceipt
	result.RethReceipt = gethResult.RethReceipt
	result.StateComparison = gethResult.StateComparison

	if actualDelta.Cmp(expectedDelta) == 0 {
		result.Passed = true
	} else {
		result.Error = fmt.Sprintf("余额变化不一致: 期望 %s, 实际 %s", expectedDelta.String(), actualDelta.String())
	}

	return result
}

// testEIP7702BasicTransferPreconf checks an EIP-7702 transfer through preconfirmation.
func testEIP7702BasicTransferPreconf(ctx context.Context, tester *tx.Tester, amount *big.Int) *tx.TxTestResult {
	result := &tx.TxTestResult{
		TestName: "EIP-7702 预确认转账",
		TxType:   "EIP-7702",
	}

	// The recipient must be on the sequencer's preconfirmation allowlist.
	recipient := common.HexToAddress("0x71920E3cb420fbD8Ba9a495E6f801c50375ea127")

	initialBalance, err := tester.GetBalance(ctx, tester.GethClient(), recipient, "latest")
	if err != nil {
		result.Error = fmt.Sprintf("获取初始余额失败: %v", err)
		return result
	}

	gethResult := tester.TestNativeTransfer(ctx, recipient, amount, tx.TxTypeEIP7702, true)
	if gethResult.Error != "" {
		result.Error = fmt.Sprintf("Geth EIP-7702 预确认交易失败: %s", gethResult.Error)
		return result
	}

	finalBalance, err := tester.GetBalance(ctx, tester.GethClient(), recipient, "latest")
	if err != nil {
		result.Error = fmt.Sprintf("获取最终余额失败: %v", err)
		return result
	}

	expectedDelta := new(big.Int).Mul(amount, big.NewInt(2)) // one transfer through each client
	actualDelta := new(big.Int).Sub(finalBalance, initialBalance)

	result.GethTxHash = gethResult.GethTxHash
	result.RethTxHash = gethResult.RethTxHash
	result.GethReceipt = gethResult.GethReceipt
	result.RethReceipt = gethResult.RethReceipt
	result.GethPreconf = gethResult.GethPreconf
	result.RethPreconf = gethResult.RethPreconf
	result.StateComparison = gethResult.StateComparison

	if actualDelta.Cmp(expectedDelta) == 0 {
		result.Passed = true
	} else {
		result.Error = fmt.Sprintf("余额变化不一致: 期望 %s, 实际 %s", expectedDelta.String(), actualDelta.String())
	}

	return result
}

// printTxTestResult renders one transaction test result.
func printTxTestResult(result *tx.TxTestResult) {
	if result.Error != "" {
		fmt.Printf("  %s %s: %s\n", color.RedString("✗ FAIL"), result.TestName, result.Error)
		return
	}

	if result.Passed {
		fmt.Printf("  %s %s\n", color.GreenString("✓ PASS"), result.TestName)
	} else {
		fmt.Printf("  %s %s\n", color.RedString("✗ FAIL"), result.TestName)
	}

	if result.GethTxHash != "" {
		fmt.Printf("    Geth TX: %s\n", result.GethTxHash)
	}
	if result.RethTxHash != "" {
		fmt.Printf("    Reth TX: %s\n", result.RethTxHash)
	}

	if result.StateComparison != nil {
		sc := result.StateComparison
		if sc.BalanceMatch {
			fmt.Printf("    余额变化: %s (一致)\n", color.GreenString(sc.GethBalanceDelta.String()))
		} else {
			fmt.Printf("    余额变化: Geth=%s, Reth=%s %s\n",
				sc.GethBalanceDelta.String(), sc.RethBalanceDelta.String(), color.RedString("(不一致)"))
		}
		if sc.GasMatch {
			fmt.Printf("    Gas 使用: %d (一致)\n", sc.GethGasUsed)
		} else {
			fmt.Printf("    Gas 使用: Geth=%d, Reth=%d %s\n",
				sc.GethGasUsed, sc.RethGasUsed, color.YellowString("(不一致)"))
		}
	}

	if result.GethPreconf != nil && result.RethPreconf != nil {
		fmt.Printf("    Preconf 状态: Geth=%s, Reth=%s\n",
			result.GethPreconf.Status, result.RethPreconf.Status)
	}
	fmt.Println()
}

// printTxTestSummary renders the transaction suite summary.
func printTxTestSummary(results []*tx.TxTestResult) {
	fmt.Println(color.CyanString("============================================================"))
	fmt.Println(color.CyanString("交易测试摘要"))
	fmt.Println(color.CyanString("============================================================"))

	total := len(results)
	passed := 0
	failed := 0

	for _, r := range results {
		if r.Passed && r.Error == "" {
			passed++
		} else {
			failed++
		}
	}

	fmt.Printf("总计: %d | 通过: %s | 失败: %s\n",
		total,
		color.GreenString("%d", passed),
		color.RedString("%d", failed))

	if failed > 0 {
		fmt.Println("\n失败的测试:")
		for _, r := range results {
			if !r.Passed || r.Error != "" {
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
}

// runContractTests compares contract deployment, reads, and writes.
func runContractTests(ctx context.Context, tester *tx.Tester) []*tx.TxTestResult {
	var results []*tx.TxTestResult

	fmt.Println(color.CyanString("📦 测试 SimpleStorage 合约部署..."))
	deployResult, err := tester.DeployContract(ctx, tx.SimpleStorageBytecode, "SimpleStorage部署")
	if err != nil {
		result := &tx.TxTestResult{
			TestName: "SimpleStorage部署",
			TxType:   "Contract Deploy",
			Error:    fmt.Sprintf("部署失败: %v", err),
			Passed:   false,
		}
		fmt.Printf("  %s %s: %s\n", color.RedString("✗ FAIL"), result.TestName, result.Error)
		results = append(results, result)
		return results
	}

	deployTxResult := &tx.TxTestResult{
		TestName:    deployResult.TestName,
		TxType:      "Contract Deploy",
		Passed:      deployResult.Success,
		GethTxHash:  deployResult.GethTxHash,
		RethTxHash:  deployResult.RethTxHash,
		GethReceipt: deployResult.GethReceipt,
		RethReceipt: deployResult.RethReceipt,
		Error:       deployResult.Error,
	}

	if deployResult.Success {
		fmt.Printf("  %s %s\n", color.GreenString("✓ PASS"), deployResult.TestName)
		fmt.Printf("    Geth TX: %s\n", deployResult.GethTxHash)
		fmt.Printf("    Reth TX: %s\n", deployResult.RethTxHash)
		fmt.Printf("    Geth 合约地址: %s\n", deployResult.GethContractAddress)
		fmt.Printf("    Reth 合约地址: %s\n", deployResult.RethContractAddress)
		if deployResult.GethReceipt != nil && deployResult.RethReceipt != nil {
			if deployResult.GethReceipt.GasUsed == deployResult.RethReceipt.GasUsed {
				fmt.Printf("    Gas 使用: %d (一致)\n", deployResult.GethReceipt.GasUsed)
			} else {
				fmt.Printf("    Gas 使用: Geth=%d, Reth=%d %s\n",
					deployResult.GethReceipt.GasUsed,
					deployResult.RethReceipt.GasUsed,
					color.YellowString("(不一致)"))
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
	contractAddrGeth := common.HexToAddress(deployResult.GethContractAddress)
	contractAddrReth := common.HexToAddress(deployResult.RethContractAddress)

	// The initial stored value should be zero.
	fmt.Println(color.CyanString("\n📞 测试 SimpleStorage.get() 方法（初始值）..."))
	// get() selector: 0x6d4ce63c.
	getCallData := common.FromHex("0x6d4ce63c")
	getResult1, err := tester.CallContract(ctx, contractAddrGeth, contractAddrReth, getCallData, "SimpleStorage.get()_初始值")
	if err != nil {
		result := &tx.TxTestResult{
			TestName: "SimpleStorage.get()_初始值",
			TxType:   "Contract Call",
			Error:    fmt.Sprintf("调用失败: %v", err),
			Passed:   false,
		}
		fmt.Printf("  %s %s: %s\n", color.RedString("✗ FAIL"), result.TestName, result.Error)
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
			fmt.Printf("    返回值: %s (Geth 和 Reth 一致)\n", getResult1.GethResult)
		} else if getResult1.Success {
			fmt.Printf("  %s %s\n", color.YellowString("⚠ WARNING"), getResult1.TestName)
			fmt.Printf("    Geth 返回: %s\n", getResult1.GethResult)
			fmt.Printf("    Reth 返回: %s\n", getResult1.RethResult)
		} else {
			fmt.Printf("  %s %s: %s\n", color.RedString("✗ FAIL"), getResult1.TestName, getResult1.Error)
		}
		results = append(results, result)
	}

	fmt.Println(color.CyanString("\n📝 测试 SimpleStorage.set(42) 方法..."))
	// set(uint256) selector: 0x60fe47b1; 42 is encoded as a 32-byte argument.
	setCallData := common.FromHex("0x60fe47b1000000000000000000000000000000000000000000000000000000000000002a")
	setResult, err := tester.SendContractTransaction(ctx, contractAddrGeth, contractAddrReth, setCallData, "SimpleStorage.set(42)")
	if err != nil {
		result := &tx.TxTestResult{
			TestName: "SimpleStorage.set(42)",
			TxType:   "Contract Transaction",
			Error:    fmt.Sprintf("交易失败: %v", err),
			Passed:   false,
		}
		fmt.Printf("  %s %s: %s\n", color.RedString("✗ FAIL"), result.TestName, result.Error)
		results = append(results, result)
	} else {
		result := &tx.TxTestResult{
			TestName:    setResult.TestName,
			TxType:      "Contract Transaction",
			Passed:      setResult.Success,
			GethTxHash:  setResult.GethTxHash,
			RethTxHash:  setResult.RethTxHash,
			GethReceipt: setResult.GethReceipt,
			RethReceipt: setResult.RethReceipt,
			Error:       setResult.Error,
		}
		if setResult.Success {
			fmt.Printf("  %s %s\n", color.GreenString("✓ PASS"), setResult.TestName)
			fmt.Printf("    Geth TX: %s\n", setResult.GethTxHash)
			fmt.Printf("    Reth TX: %s\n", setResult.RethTxHash)
			if setResult.GethReceipt != nil && setResult.RethReceipt != nil {
				if setResult.GethReceipt.GasUsed == setResult.RethReceipt.GasUsed {
					fmt.Printf("    Gas 使用: %d (一致)\n", setResult.GethReceipt.GasUsed)
				} else {
					fmt.Printf("    Gas 使用: Geth=%d, Reth=%d %s\n",
						setResult.GethReceipt.GasUsed,
						setResult.RethReceipt.GasUsed,
						color.YellowString("(不一致)"))
				}
			}
		} else {
			fmt.Printf("  %s %s: %s\n", color.RedString("✗ FAIL"), setResult.TestName, setResult.Error)
		}
		results = append(results, result)
	}

	// The value should now be 42.
	fmt.Println(color.CyanString("\n📞 测试 SimpleStorage.get() 方法（设置后）..."))
	getResult2, err := tester.CallContract(ctx, contractAddrGeth, contractAddrReth, getCallData, "SimpleStorage.get()_设置后")
	if err != nil {
		result := &tx.TxTestResult{
			TestName: "SimpleStorage.get()_设置后",
			TxType:   "Contract Call",
			Error:    fmt.Sprintf("调用失败: %v", err),
			Passed:   false,
		}
		fmt.Printf("  %s %s: %s\n", color.RedString("✗ FAIL"), result.TestName, result.Error)
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
			if getResult2.GethResult == expectedValue {
				fmt.Printf("  %s %s\n", color.GreenString("✓ PASS"), getResult2.TestName)
				fmt.Printf("    返回值: 42 (0x2a) - Geth 和 Reth 一致\n")
			} else {
				result.Passed = false
				result.Error = fmt.Sprintf("返回值不正确: 期望 %s, 实际 %s", expectedValue, getResult2.GethResult)
				fmt.Printf("  %s %s: %s\n", color.RedString("✗ FAIL"), getResult2.TestName, result.Error)
			}
		} else if getResult2.Success {
			fmt.Printf("  %s %s\n", color.YellowString("⚠ WARNING"), getResult2.TestName)
			fmt.Printf("    Geth 返回: %s\n", getResult2.GethResult)
			fmt.Printf("    Reth 返回: %s\n", getResult2.RethResult)
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

func loadKnownDiffsFS(reporter *report.Reporter, filesystem fs.FS) error {
	file, err := filesystem.Open(knownDiffsFileName)
	if err != nil {
		return err
	}
	defer file.Close()
	return reporter.LoadKnownDiffs(file)
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
	if err := json.Unmarshal(data, &tests); err != nil {
		return nil, err
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
		return nil, fmt.Errorf("获取 latest block 失败: %v", resp.Error)
	}

	var block map[string]interface{}
	if err := json.Unmarshal(resp.RawBody, &block); err != nil {
		return nil, fmt.Errorf("解析 block 响应失败: %v", err)
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

// replaceTemplateVars substitutes runtime values into testcase parameters.
func replaceTemplateVars(tests []report.TestCase, vars *TemplateVars) []report.TestCase {
	if vars == nil {
		return tests
	}

	// Round-trip through JSON so substitutions also reach nested parameters.
	data, err := json.Marshal(tests)
	if err != nil {
		return tests
	}

	jsonStr := string(data)

	replacements := map[string]string{
		"{{latest_block_hash}}":      vars.LatestBlockHash,
		"{{latest_block_number}}":    vars.LatestBlockNumber,
		"{{latest_tx_hash}}":         vars.LatestTxHash,
		"{{latest_deposit_tx_hash}}": vars.LatestDepositTxHash,
		"{{block_one_rlp}}":          vars.BlockOneRLP,
	}

	for placeholder, value := range replacements {
		if value != "" {
			jsonStr = strings.ReplaceAll(jsonStr, placeholder, value)
		}
	}

	var result []report.TestCase
	if err := json.Unmarshal([]byte(jsonStr), &result); err != nil {
		return tests
	}

	return result
}

// hasTemplateVars reports whether any testcase needs runtime substitution.
func hasTemplateVars(tests []report.TestCase) bool {
	data, err := json.Marshal(tests)
	if err != nil {
		return false
	}
	pattern := regexp.MustCompile(`\{\{[a-z_]+\}\}`)
	return pattern.Match(data)
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
