package cmd

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
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

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/fatih/color"
)

var (
	testFile     string
	testcasesDir string
	excludeFiles []string // 要排除的文件名（不含路径）
)

// runTests 运行测试的主函数
func runTests() error {
	if txStandardOnly && !txTest {
		return fmt.Errorf("--tx-standard-only 需与 --tx 同时使用")
	}

	// 设置默认输出文件
	// 如果用户没有指定 -o（即 outputFile 为默认值 "report.json"）
	// 且开启了 --tx，则默认输出到 report.tx.json
	if outputFile == "report.json" && txTest {
		outputFile = "report.tx.json"
	}

	// 如果只运行交易测试，跳过 RPC 查询测试
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

	// 运行 RPC 查询测试
	var allTests []report.TestCase
	var testFiles []string

	// 确定要运行的测试文件
	if testFile != "" {
		// -f 参数指定单个文件
		testFiles = []string{testFile}
	} else {
		// 无参数，运行 testcases 目录下所有 json 文件（排除指定的文件）
		files, err := findTestFiles(testcasesDir, excludeFiles)
		if err != nil {
			return fmt.Errorf("查找测试文件失败: %w", err)
		}
		if len(files) == 0 {
			return fmt.Errorf("在 %s 目录下未找到测试文件", testcasesDir)
		}
		testFiles = files
	}

	// 加载所有测试用例
	for _, file := range testFiles {
		tests, err := loadTestFile(file)
		if err != nil {
			return fmt.Errorf("加载测试文件 %s 失败: %w", file, err)
		}
		allTests = append(allTests, tests...)
	}

	if len(allTests) == 0 {
		return fmt.Errorf("没有测试用例")
	}

	// 检查是否需要模板变量替换
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

	// 打印测试信息
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

	// 创建客户端对
	clients := rpc.NewClientPair(gethURL, rethURL, timeout)

	// 创建报告器
	reporter := report.NewReporter(gethURL, rethURL, verbose)

	// 加载已知差异配置（从 testcases 目录下的 known_diffs.json）
	knownDiffsPath := filepath.Join(testcasesDir, "known_diffs.json")
	if _, err := os.Stat(knownDiffsPath); err == nil {
		if err := reporter.LoadKnownDiffs(knownDiffsPath); err == nil {
			fmt.Printf("已加载已知差异配置: %s\n", knownDiffsPath)
		}
	}

	fmt.Printf("\ngeth: %s\n", gethURL)
	fmt.Printf("reth: %s\n", rethURL)
	fmt.Println()

	// 执行测试
	ctx := context.Background()
	for _, tc := range allTests {
		// 创建请求
		req := rpc.NewRequest(tc.Method, tc.Params)

		// 执行对比
		compareResult := clients.CompareWithRetry(ctx, req, maxRetries, retryDelay)

		// 对比结果
		var diffResult *diff.CompareResult
		var compareErr error

		if compareResult.PrimaryResponse.Error == nil && compareResult.SecondaryResponse.Error == nil {
			diffResult, compareErr = diff.Compare(
				compareResult.PrimaryResponse.RawBody,
				compareResult.SecondaryResponse.RawBody,
				diff.DefaultOptions(),
			)
		}

		// 添加结果
		reporter.AddResult(tc, compareResult, diffResult, compareErr)
	}

	// 打印摘要
	reporter.PrintSummary()

	// 保存报告
	if outputFile != "" {
		if err := reporter.SaveJSON(outputFile); err != nil {
			fmt.Fprintf(os.Stderr, "保存报告失败: %v\n", err)
		} else {
			fmt.Printf("\n报告已保存到: %s\n", outputFile)
		}
	}

	// 返回退出码
	if reporter.HasFailures() {
		os.Exit(1)
	}

	return nil
}

// runTransactionTests 运行交易测试
func runTransactionTests() error {
	ctx := context.Background()

	// 创建 Reporter 用于记录所有 RPC 调用
	reporter := report.NewReporter(gethURL, rethURL, verbose)

	// 加载已知差异配置
	knownDiffsPath := filepath.Join(testcasesDir, "known_diffs.json")
	if err := reporter.LoadKnownDiffs(knownDiffsPath); err != nil {
		fmt.Printf("警告: 加载已知差异配置失败: %v\n", err)
	}

	// 创建交易测试器
	tester, err := tx.NewTester(gethURL, rethURL, txPrivateKey, reporter)
	if err != nil {
		return fmt.Errorf("创建交易测试器失败: %w", err)
	}

	// 确定接收地址（标准方式）
	var recipient common.Address
	if txRecipient != "" {
		recipient = common.HexToAddress(txRecipient)
	} else {
		// 生成随机地址
		randomBytes := make([]byte, 20)
		rand.Read(randomBytes)
		recipient = common.BytesToAddress(randomBytes)
	}

	// 确定预确认交易接收地址（必须在白名单中）
	var recipientPreconf common.Address
	if txRecipientPreconf != "" {
		recipientPreconf = common.HexToAddress(txRecipientPreconf)
	} else {
		// 如果未指定，使用默认白名单地址
		recipientPreconf = common.HexToAddress("0x71920E3cb420fbD8Ba9a495E6f801c50375ea127")
	}

	// 解析转账金额
	amount, ok := new(big.Int).SetString(txAmount, 10)
	if !ok {
		return fmt.Errorf("无效的转账金额: %s", txAmount)
	}

	// 检查发送者余额
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

	// 检查余额是否足够（需要足够支付多次交易）
	minRequired := new(big.Int).Mul(amount, big.NewInt(20)) // 预留 20 倍（EIP-7702 需要更多）
	if balance.Cmp(minRequired) < 0 {
		return fmt.Errorf("余额不足: 当前 %s wei, 需要至少 %s wei", balance.String(), minRequired.String())
	}

	var results []*tx.TxTestResult

	// 测试交易类型：Legacy 和 EIP-1559
	txTypes := []tx.TxType{tx.TxTypeLegacy, tx.TxTypeEIP1559}

	for _, txType := range txTypes {
		// 标准方式
		fmt.Printf("📤 测试 %s 交易 (eth_sendRawTransaction)...\n", txType.String())
		result := tester.TestNativeTransfer(ctx, recipient, amount, txType, false)
		printTxTestResult(result)
		results = append(results, result)

		if includePreconfTransactions(txStandardOnly) {
			// 预确认方式（使用白名单中的接收地址）
			fmt.Printf("📤 测试 %s 交易 (eth_sendRawTransactionWithPreconf)...\n", txType.String())
			preconfResult := tester.TestNativeTransfer(ctx, recipientPreconf, amount, txType, true)
			printTxTestResult(preconfResult)
			results = append(results, preconfResult)
		}
	}

	// EIP-7702 测试
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

	// 合约测试
	fmt.Println()
	fmt.Println(color.CyanString("============================================================"))
	fmt.Println(color.CyanString("合约测试"))
	fmt.Println(color.CyanString("============================================================"))
	contractResults := runContractTests(ctx, tester)
	for _, result := range contractResults {
		results = append(results, result)
	}

	// Txpool 拒绝测试
	fmt.Println()
	fmt.Println(color.CyanString("============================================================"))
	fmt.Println(color.CyanString("Txpool 拒绝测试（MetaTx + EIP-155）"))
	fmt.Println(color.CyanString("============================================================"))
	rejectionResults := runRejectionTests(ctx, tester)
	for _, result := range rejectionResults {
		printTxTestResult(result)
		results = append(results, result)
	}

	// 检查 eth_feeHistory
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

	// 打印交易测试摘要
	printTxTestSummary(results)

	// 打印 RPC 测试摘要
	reporter.PrintSummary()

	// 保存报告
	if outputFile != "" {
		if err := reporter.SaveJSON(outputFile); err != nil {
			fmt.Fprintf(os.Stderr, "保存报告失败: %v\n", err)
		} else {
			fmt.Printf("\n报告已保存到: %s\n", outputFile)
		}
	}

	// 返回退出码
	if reporter.HasFailures() {
		os.Exit(1)
	}

	return nil
}

// testEIP7702Transfer 测试 EIP-7702 授权转账
// EIP-7702 允许 EOA 账户临时设置代码，实现账户抽象功能
func testEIP7702Transfer(
	ctx context.Context,
	tester *tx.Tester,
	amount *big.Int,
	includePreconf bool,
) []*tx.TxTestResult {
	var results []*tx.TxTestResult

	// 测试 1: 基本 EIP-7702 交易（带空授权列表）
	result1 := testEIP7702BasicTransfer(ctx, tester, amount)
	results = append(results, result1)

	if includePreconf {
		// 测试 2: EIP-7702 交易（带预确认）
		result2 := testEIP7702BasicTransferPreconf(ctx, tester, amount)
		results = append(results, result2)
	}

	return results
}

// testEIP7702BasicTransfer 测试基本 EIP-7702 交易
func testEIP7702BasicTransfer(ctx context.Context, tester *tx.Tester, amount *big.Int) *tx.TxTestResult {
	result := &tx.TxTestResult{
		TestName: "EIP-7702 基本转账",
		TxType:   "EIP-7702",
	}

	// 使用 Hardhat 第三个默认账户作为接收者
	recipient := common.HexToAddress("0x3C44CdDdB6a900fa2b585dd299e03d12FA4293BC")

	// 获取初始余额
	initialBalance, err := tester.GetBalance(ctx, tester.GethClient(), recipient, "latest")
	if err != nil {
		result.Error = fmt.Sprintf("获取初始余额失败: %v", err)
		return result
	}

	// 使用 EIP-7702 交易类型发送（带空授权列表）
	gethResult := tester.TestNativeTransfer(ctx, recipient, amount, tx.TxTypeEIP7702, false)
	if gethResult.Error != "" {
		result.Error = fmt.Sprintf("Geth EIP-7702 交易失败: %s", gethResult.Error)
		return result
	}

	// 获取交易后余额
	finalBalance, err := tester.GetBalance(ctx, tester.GethClient(), recipient, "latest")
	if err != nil {
		result.Error = fmt.Sprintf("获取最终余额失败: %v", err)
		return result
	}

	// 验证余额变化
	expectedDelta := new(big.Int).Mul(amount, big.NewInt(2)) // Geth + Reth 两次转账
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

// testEIP7702BasicTransferPreconf 测试 EIP-7702 预确认交易
func testEIP7702BasicTransferPreconf(ctx context.Context, tester *tx.Tester, amount *big.Int) *tx.TxTestResult {
	result := &tx.TxTestResult{
		TestName: "EIP-7702 预确认转账",
		TxType:   "EIP-7702",
	}

	// 使用预确认白名单地址作为接收者
	recipient := common.HexToAddress("0x71920E3cb420fbD8Ba9a495E6f801c50375ea127")

	// 获取初始余额
	initialBalance, err := tester.GetBalance(ctx, tester.GethClient(), recipient, "latest")
	if err != nil {
		result.Error = fmt.Sprintf("获取初始余额失败: %v", err)
		return result
	}

	// 使用 EIP-7702 交易类型发送（带空授权列表，预确认方式）
	gethResult := tester.TestNativeTransfer(ctx, recipient, amount, tx.TxTypeEIP7702, true)
	if gethResult.Error != "" {
		result.Error = fmt.Sprintf("Geth EIP-7702 预确认交易失败: %s", gethResult.Error)
		return result
	}

	// 获取交易后余额
	finalBalance, err := tester.GetBalance(ctx, tester.GethClient(), recipient, "latest")
	if err != nil {
		result.Error = fmt.Sprintf("获取最终余额失败: %v", err)
		return result
	}

	// 验证余额变化
	expectedDelta := new(big.Int).Mul(amount, big.NewInt(2)) // Geth + Reth 两次转账
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

// printTxTestResult 打印单个交易测试结果
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

// printTxTestSummary 打印交易测试摘要
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

// runContractTests 运行合约测试
func runContractTests(ctx context.Context, tester *tx.Tester) []*tx.TxTestResult {
	var results []*tx.TxTestResult

	// 1. 测试 SimpleStorage 合约部署
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

	// 转换为 TxTestResult
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

	// 使用各自的合约地址进行后续测试
	contractAddrGeth := common.HexToAddress(deployResult.GethContractAddress)
	contractAddrReth := common.HexToAddress(deployResult.RethContractAddress)

	// 2. 测试 get() 方法（初始值应该是 0）
	fmt.Println(color.CyanString("\n📞 测试 SimpleStorage.get() 方法（初始值）..."))
	// get() 方法的 function selector: 0x6d4ce63c
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

	// 3. 测试 set(42) 方法
	fmt.Println(color.CyanString("\n📝 测试 SimpleStorage.set(42) 方法..."))
	// set(uint256) 方法的 function selector: 0x60fe47b1
	// 参数: 42 (0x2a) 编码为 32 字节: 0x000000000000000000000000000000000000000000000000000000000000002a
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

	// 4. 再次调用 get() 方法（应该返回 42）
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

		// 验证返回值是否为 42 (0x2a)
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

// 配置文件名（不是测试用例，会被自动跳过）
const knownDiffsFileName = "known_diffs.json"

// findTestFiles 查找目录下所有 json 测试文件（排除配置文件和指定的文件）
func findTestFiles(dir string, excludes []string) ([]string, error) {
	var files []string

	// 构建排除文件的 set
	excludeSet := make(map[string]bool)
	for _, e := range excludes {
		excludeSet[e] = true
	}
	// 配置文件不是测试用例，自动排除
	excludeSet[knownDiffsFileName] = true

	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		// 检查是否为 json 文件
		if !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		// 检查是否被排除
		if excludeSet[entry.Name()] {
			continue
		}
		files = append(files, filepath.Join(dir, entry.Name()))
	}

	// 按文件名排序
	sort.Strings(files)
	return files, nil
}

// loadTestFile 加载单个测试文件
func loadTestFile(file string) ([]report.TestCase, error) {
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}

	var tests []report.TestCase
	if err := json.Unmarshal(data, &tests); err != nil {
		return nil, err
	}

	return tests, nil
}

// TemplateVars 存储模板变量的值
type TemplateVars struct {
	LatestBlockHash   string `json:"latest_block_hash"`
	LatestBlockNumber string `json:"latest_block_number"`
	LatestTxHash      string `json:"latest_tx_hash"`
	BlockOneRLP       string `json:"block_one_rlp"`
	// LatestDepositTxHash 是 latest block 中第一个 deposit 交易(type 0x7e)的 hash。
	// OP 每个区块第一笔即 L1 attributes deposit,用于对比 geth/reth 的 deposit 收据。
	LatestDepositTxHash string `json:"latest_deposit_tx_hash"`
}

// fetchTemplateVars 从 RPC 获取模板变量值
func fetchTemplateVars(ctx context.Context, client *rpc.Client) (*TemplateVars, error) {
	vars := &TemplateVars{}

	// 获取 latest block
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
		// 获取区块中的第一个交易 hash（如果有）
		if txs, ok := result["transactions"].([]interface{}); ok && len(txs) > 0 {
			if txHash, ok := txs[0].(string); ok {
				vars.LatestTxHash = txHash
			}
		}
	}

	// 如果 latest block 没有交易，尝试从更早的区块获取
	if vars.LatestTxHash == "" {
		// 尝试从区块 1 获取交易
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

	// 获取 latest block 中第一个 deposit 交易(type 0x7e)的 hash,用于对比 deposit 收据。
	// OP 每个区块第一笔即 L1 attributes deposit,故 latest block 必有 deposit 交易;
	// 极端情况下回退到区块 1。
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

// fetchFirstDepositTxHash 返回指定区块中第一个 deposit 交易(type 0x7e)的 hash,没有则返回 ""。
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

// replaceTemplateVars 替换测试用例中的模板变量
func replaceTemplateVars(tests []report.TestCase, vars *TemplateVars) []report.TestCase {
	if vars == nil {
		return tests
	}

	// 将测试用例转换为 JSON，替换模板变量，再转回
	data, err := json.Marshal(tests)
	if err != nil {
		return tests
	}

	jsonStr := string(data)

	// 替换模板变量
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

	// 转回测试用例
	var result []report.TestCase
	if err := json.Unmarshal([]byte(jsonStr), &result); err != nil {
		return tests
	}

	return result
}

// hasTemplateVars 检查测试用例是否包含模板变量
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

// MantleMetaTxPrefix 是 Mantle MetaTx 的 32 字节前缀常量。
// 14 个零字节 + "MantleMetaTxPrefix" ASCII (18 字节) = 32 字节。
var mantleMetaTxPrefix = [32]byte{
	0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
	0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
	'M', 'a', 'n', 't', 'l', 'e',
	'M', 'e', 't', 'a', 'T', 'x',
	'P', 'r', 'e', 'f', 'i', 'x',
}

// runRejectionTests 运行 txpool 拒绝测试。
//
// 验证 op-geth 和 op-reth 对以下交易的拒绝行为一致：
// 1. 非 EIP-155（unprotected）legacy 交易 — 两边都应拒绝
// 2. MetaTx prefix 交易 — 两边都应拒绝
// 3. EIP-155 legacy 交易（正向） — 应被接受（不被误拒）
func runRejectionTests(ctx context.Context, tester *tx.Tester) []*tx.TxTestResult {
	var results []*tx.TxTestResult

	// 测试 1: 非 EIP-155 legacy 交易应被拒绝
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

	// 测试 2: MetaTx prefix 交易应被拒绝
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

	// 测试 3: 正常 EIP-155 legacy 交易不应被误拒
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

	// 测试 4: 转发型节点对"已转发交易"的本地 txpool 准入行为一致（geth vs reth）。
	// 前提：本套件打的 geth/reth 端点为转发型节点（配了 sequencer）；对 sequencer 本身此测试无意义。
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
