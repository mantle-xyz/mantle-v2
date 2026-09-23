// Package report 提供测试报告生成功能
package report

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/ethereum-optimism/optimism/op-rpc-compat/pkg/diff"
	"github.com/ethereum-optimism/optimism/op-rpc-compat/pkg/rpc"

	"github.com/fatih/color"
)

// TestStatus 测试状态
type TestStatus string

const (
	StatusPass       TestStatus = "PASS"       // 完全一致
	StatusCompatible TestStatus = "COMPATIBLE" // 兼容（已知差异验证通过，或双方都报错方法不存在）
	StatusWarning    TestStatus = "WARNING"    // 有差异但不严重（如额外字段）
	StatusFail       TestStatus = "FAIL"       // 真正的失败
)

// KnownDiff 已知差异配置
type KnownDiff struct {
	TestName    string          `json:"test_name"`              // 测试用例名称
	Reason      string          `json:"reason,omitempty"`       // 差异原因说明
	GethExample json.RawMessage `json:"geth_example,omitempty"` // geth 预期响应示例
	RethExample json.RawMessage `json:"reth_example,omitempty"` // reth 预期响应示例
}

// KnownDiffsConfig 已知差异配置文件
type KnownDiffsConfig struct {
	KnownDiffs []KnownDiff `json:"known_diffs"`
}

// TestCase 测试用例
type TestCase struct {
	Name        string      `json:"name"`
	Method      string      `json:"method"`
	Params      interface{} `json:"params,omitempty"`
	Description string      `json:"description,omitempty"`
}

// TestResult 单个测试结果
type TestResult struct {
	TestCase     TestCase          `json:"test_case"`
	Status       TestStatus        `json:"status"`
	Passed       bool              `json:"passed"` // 兼容旧代码，Status != FAIL 时为 true
	GethResponse json.RawMessage   `json:"geth_response,omitempty"`
	RethResponse json.RawMessage   `json:"reth_response,omitempty"`
	GethError    string            `json:"geth_error,omitempty"`
	RethError    string            `json:"reth_error,omitempty"`
	GethDuration time.Duration     `json:"geth_duration"`
	RethDuration time.Duration     `json:"reth_duration"`
	Differences  []diff.Difference `json:"differences,omitempty"`
	CompareError string            `json:"compare_error,omitempty"`
	SkipReason   string            `json:"skip_reason,omitempty"` // 跳过原因
}

// Report 完整测试报告
type Report struct {
	Timestamp       time.Time    `json:"timestamp"`
	GethURL         string       `json:"geth_url"`
	RethURL         string       `json:"reth_url"`
	TotalTests      int          `json:"total_tests"`
	PassedTests     int          `json:"passed_tests"`
	CompatibleTests int          `json:"compatible_tests"`
	WarningTests    int          `json:"warning_tests"`
	FailedTests     int          `json:"failed_tests"`
	Results         []TestResult `json:"results"`
	Summary         string       `json:"summary"`
}

// Reporter 报告生成器
type Reporter struct {
	results    []TestResult
	gethURL    string
	rethURL    string
	startTime  time.Time
	verbose    bool
	knownDiffs map[string]KnownDiff // key: test_name
}

// NewReporter 创建报告生成器
func NewReporter(gethURL, rethURL string, verbose bool) *Reporter {
	return &Reporter{
		results:    []TestResult{},
		gethURL:    gethURL,
		rethURL:    rethURL,
		startTime:  time.Now(),
		verbose:    verbose,
		knownDiffs: make(map[string]KnownDiff),
	}
}

// LoadKnownDiffs 加载已知差异配置
func (r *Reporter) LoadKnownDiffs(filename string) error {
	data, err := os.ReadFile(filename)
	if err != nil {
		return err
	}

	var config KnownDiffsConfig
	if err := json.Unmarshal(data, &config); err != nil {
		return err
	}

	for _, kd := range config.KnownDiffs {
		r.knownDiffs[kd.TestName] = kd
	}

	return nil
}

// GetKnownDiff 获取已知差异配置
func (r *Reporter) GetKnownDiff(testName string) *KnownDiff {
	if kd, ok := r.knownDiffs[testName]; ok {
		return &kd
	}
	return nil
}

func newTestResult(tc TestCase, compareResult *rpc.CompareResult) TestResult {
	result := TestResult{
		TestCase: tc,
		Status:   StatusPass,
	}
	if compareResult == nil {
		return result
	}

	// 处理 geth 响应
	if compareResult.PrimaryResponse != nil {
		result.GethDuration = compareResult.PrimaryResponse.Duration
		// HTTP/网络错误
		if compareResult.PrimaryResponse.Error != nil {
			result.GethError = compareResult.PrimaryResponse.Error.Error()
		}
		if compareResult.PrimaryResponse.RawBody != nil {
			result.GethResponse = compareResult.PrimaryResponse.RawBody
		}
	}

	// 处理 reth 响应
	if compareResult.SecondaryResponse != nil {
		result.RethDuration = compareResult.SecondaryResponse.Duration
		// HTTP/网络错误
		if compareResult.SecondaryResponse.Error != nil {
			result.RethError = compareResult.SecondaryResponse.Error.Error()
		}
		if compareResult.SecondaryResponse.RawBody != nil {
			result.RethResponse = compareResult.SecondaryResponse.RawBody
		}
	}
	return result
}

// AddCompatibleResult records an expected, scenario-specific RPC response while preserving the
// actual response in the report. The caller must perform the later behavioral assertion.
func (r *Reporter) AddCompatibleResult(tc TestCase, compareResult *rpc.CompareResult, reason string) {
	result := newTestResult(tc, compareResult)
	result.Status = StatusCompatible
	result.Passed = true
	result.SkipReason = reason
	r.results = append(r.results, result)
	r.printResult(result)
}

// AddResult 添加测试结果
func (r *Reporter) AddResult(tc TestCase, compareResult *rpc.CompareResult, diffResult *diff.CompareResult, compareErr error) {
	result := newTestResult(tc, compareResult)

	// 检查是否为已知差异
	if knownDiff := r.GetKnownDiff(tc.Name); knownDiff != nil {
		// 验证实际响应是否与预期的已知差异匹配
		if r.matchesKnownDiff(&result, knownDiff) {
			result.Status = StatusCompatible
			result.SkipReason = knownDiff.Reason // 复用 SkipReason 字段存储原因
			result.Passed = true
		} else {
			result.Status = StatusFail
			result.CompareError = fmt.Sprintf("已知差异验证失败: 实际响应与预期不符 (预期: %s)", knownDiff.Reason)
			result.Passed = false
		}
		r.results = append(r.results, result)
		r.printResult(result)
		return
	}

	// 判断状态
	result.Status = r.determineStatus(&result, compareResult, diffResult, compareErr)

	// 设置 Passed 标志（非 FAIL 状态都算通过）
	result.Passed = result.Status != StatusFail

	r.results = append(r.results, result)

	// 实时输出
	r.printResult(result)
}

// matchesKnownDiff 检查实际响应是否与已知差异的预期匹配。
//
// 收紧策略（原先只比 error/success 类型，会永久掩盖后续 code/message 漂移）：
//   - geth 侧（参照端）：仅比 type + code。geth 的错误措辞随输入/版本变化，且记录里的
//     example 多为范例/截断，不能当精确 key。
//   - reth 侧（被测端）：比 type + code + 规范化 message（一方包含另一方，容忍截断）。
//     reth 才是我们要认证"已知不同"的对象——它一旦漂移（如
//     "insufficient funds for transfer" → "EVM error: OutOfFunds"）就不再匹配，
//     该差异重新在报告里暴露，提示更新 known_diffs。
func (r *Reporter) matchesKnownDiff(result *TestResult, knownDiff *KnownDiff) bool {
	return shapeMatch(parseShape(knownDiff.GethExample), parseShape(result.GethResponse), false) &&
		shapeMatch(parseShape(knownDiff.RethExample), parseShape(result.RethResponse), true)
}

var (
	hexRe = regexp.MustCompile(`0x[0-9a-fA-F]+`)
	numRe = regexp.MustCompile(`\d+`)
)

// normMsg 规范化错误消息：抹掉易变的 hex/数字（地址、gas、区块号等），便于跨调用比较。
func normMsg(s string) string {
	s = hexRe.ReplaceAllString(s, "0xX")
	s = numRe.ReplaceAllString(s, "N")
	return strings.ToLower(strings.TrimSpace(s))
}

// respShape 响应的可比形态。
type respShape struct {
	typ  responseType
	code int
	msg  string // 规范化后的错误 message（仅 error 有意义）
}

func parseShape(raw json.RawMessage) respShape {
	if raw == nil {
		return respShape{typ: responseTypeUnknown}
	}
	var resp map[string]json.RawMessage
	if err := json.Unmarshal(raw, &resp); err != nil {
		return respShape{typ: responseTypeUnknown}
	}
	if e, ok := resp["error"]; ok {
		var eo struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		}
		_ = json.Unmarshal(e, &eo)
		return respShape{typ: responseTypeError, code: eo.Code, msg: normMsg(eo.Message)}
	}
	if _, ok := resp["result"]; ok {
		return respShape{typ: responseTypeSuccess}
	}
	return respShape{typ: responseTypeUnknown}
}

// shapeMatch 判定 actual 是否落在 expected 记录的形态内。
// matchMsg=true 时对错误响应额外比对规范化 message（用于 reth 被测端）。
func shapeMatch(expected, actual respShape, matchMsg bool) bool {
	if expected.typ != actual.typ {
		return false
	}
	if expected.typ == responseTypeError {
		if expected.code != actual.code {
			return false
		}
		if !matchMsg {
			return true
		}
		// 容忍记录截断：任一方规范化 message 包含另一方即认为一致。
		return strings.Contains(actual.msg, expected.msg) || strings.Contains(expected.msg, actual.msg)
	}
	// 成功/未知：result 常含易变量（版本、filter ID），只比类型。
	return true
}

// responseType 响应类型
type responseType int

const (
	responseTypeUnknown responseType = iota
	responseTypeSuccess              // 有 result 字段
	responseTypeError                // 有 error 字段
)

// determineStatus 确定测试状态
func (r *Reporter) determineStatus(result *TestResult, compareResult *rpc.CompareResult, diffResult *diff.CompareResult, compareErr error) TestStatus {
	// 如果有 HTTP/网络错误，视为失败
	if result.GethError != "" || result.RethError != "" {
		return StatusFail
	}

	// 如果有比较错误，视为失败
	if compareErr != nil {
		result.CompareError = compareErr.Error()
		return StatusFail
	}

	// 检查 RPC 响应中的错误
	gethHasRPCError := compareResult.PrimaryResponse != nil &&
		compareResult.PrimaryResponse.Response != nil &&
		compareResult.PrimaryResponse.Response.Error != nil
	rethHasRPCError := compareResult.SecondaryResponse != nil &&
		compareResult.SecondaryResponse.Response != nil &&
		compareResult.SecondaryResponse.Response.Error != nil

	// 如果有一方有 RPC 错误
	if gethHasRPCError || rethHasRPCError {
		status, diffs, reason := r.checkRPCErrorCompatibility(compareResult, gethHasRPCError, rethHasRPCError)
		if len(diffs) > 0 {
			result.Differences = diffs
		}
		if reason != "" {
			result.SkipReason = reason
		}
		return status
	}

	// 检查 diff 结果
	if diffResult != nil {
		result.Differences = diffResult.Differences

		// 没有任何差异
		if len(diffResult.Differences) == 0 {
			return StatusPass
		}

		// 只有警告级别差异（额外字段等）
		if diffResult.FailCount == 0 && diffResult.WarningCount > 0 {
			// 统计 reth 额外字段和 geth 额外字段（reth 缺少）的数量
			rethExtraCount := 0
			gethExtraCount := 0
			for _, d := range diffResult.Differences {
				if d.Severity == diff.SeverityWarning {
					if d.Type == diff.DiffTypeExtra {
						rethExtraCount++
					} else if d.Type == diff.DiffTypeMissing {
						gethExtraCount++
					}
				}
			}

			// 生成详细的原因说明
			var reasonParts []string
			if rethExtraCount > 0 {
				reasonParts = append(reasonParts, fmt.Sprintf("reth 有 %d 处额外字段", rethExtraCount))
			}
			if gethExtraCount > 0 {
				reasonParts = append(reasonParts, fmt.Sprintf("geth 有 %d 处额外字段", gethExtraCount))
			}
			if len(reasonParts) > 0 {
				result.SkipReason = fmt.Sprintf("%s（不影响通过）", strings.Join(reasonParts, "，"))
			} else {
				result.SkipReason = fmt.Sprintf("发现 %d 处额外字段差异（不影响通过）", diffResult.WarningCount)
			}
			return StatusWarning
		}

		// 有失败级别差异
		if diffResult.FailCount > 0 {
			return StatusFail
		}
	}

	return StatusPass
}

// checkRPCErrorCompatibility 检查 RPC 错误兼容性，返回状态、差异列表和兼容原因
func (r *Reporter) checkRPCErrorCompatibility(compareResult *rpc.CompareResult, gethHasError, rethHasError bool) (TestStatus, []diff.Difference, string) {
	var diffs []diff.Difference

	// 一方有 RPC 错误，一方没有 -> 失败
	if gethHasError != rethHasError {
		if gethHasError {
			gethErr := compareResult.PrimaryResponse.Response.Error
			diffs = append(diffs, diff.Difference{
				Path:     "error",
				Type:     diff.DiffTypeExtra,
				Expected: fmt.Sprintf("code=%d, message=%s", gethErr.Code, gethErr.Message),
				Actual:   nil,
				Message:  "geth 返回错误，reth 返回成功",
			})
		} else {
			rethErr := compareResult.SecondaryResponse.Response.Error
			diffs = append(diffs, diff.Difference{
				Path:     "error",
				Type:     diff.DiffTypeMissing,
				Expected: nil,
				Actual:   fmt.Sprintf("code=%d, message=%s", rethErr.Code, rethErr.Message),
				Message:  "geth 返回成功，reth 返回错误",
			})
		}
		return StatusFail, diffs, ""
	}

	// 双方都有 RPC 错误，检查兼容性
	if gethHasError && rethHasError {
		gethErr := compareResult.PrimaryResponse.Response.Error
		rethErr := compareResult.SecondaryResponse.Response.Error

		// 构造 map 用于兼容性检查
		gethErrObj := map[string]interface{}{
			"code":    gethErr.Code,
			"message": gethErr.Message,
		}
		rethErrObj := map[string]interface{}{
			"code":    rethErr.Code,
			"message": rethErr.Message,
		}

		compat := diff.CheckErrorCompatibility(gethErrObj, rethErrObj)
		switch compat {
		case diff.ErrorCompatIdentical:
			return StatusPass, nil, ""
		case diff.ErrorCompatMethodNotFound:
			reason := fmt.Sprintf("双方都返回方法不存在/未实现错误 (geth: %d, reth: %d)", gethErr.Code, rethErr.Code)
			return StatusCompatible, nil, reason
		}

		// 错误不兼容，记录差异
		if gethErr.Code != rethErr.Code {
			diffs = append(diffs, diff.Difference{
				Path:     "error.code",
				Type:     diff.DiffTypeValue,
				Expected: gethErr.Code,
				Actual:   rethErr.Code,
				Message:  "错误码不同",
			})
		}
		if gethErr.Message != rethErr.Message {
			diffs = append(diffs, diff.Difference{
				Path:     "error.message",
				Type:     diff.DiffTypeValue,
				Expected: gethErr.Message,
				Actual:   rethErr.Message,
				Message:  "错误消息不同",
			})
		}
		return StatusFail, diffs, ""
	}

	return StatusFail, diffs, ""
}

// printResult 打印单个测试结果
func (r *Reporter) printResult(result TestResult) {
	green := color.New(color.FgGreen).SprintFunc()
	red := color.New(color.FgRed).SprintFunc()
	yellow := color.New(color.FgYellow).SprintFunc()
	cyan := color.New(color.FgCyan).SprintFunc()
	blue := color.New(color.FgBlue).SprintFunc()

	var statusStr string
	switch result.Status {
	case StatusPass:
		statusStr = green("✓ PASS")
	case StatusCompatible:
		statusStr = blue("≈ COMPATIBLE")
	case StatusWarning:
		statusStr = yellow("⚠ WARNING")
	case StatusFail:
		statusStr = red("✗ FAIL")
	default:
		statusStr = red("✗ FAIL")
	}

	fmt.Printf("%s %s [%s]\n", statusStr, cyan(result.TestCase.Method), result.TestCase.Name)

	if r.verbose {
		fmt.Printf("  geth: %v, reth: %v\n", result.GethDuration, result.RethDuration)
	}

	// 显示兼容信息（已知差异或自动检测的兼容）
	if result.Status == StatusCompatible {
		if result.SkipReason != "" {
			// 已知差异验证通过
			fmt.Printf("  %s 已知差异: %s\n", blue("→"), result.SkipReason)
		} else {
			// 自动检测的兼容（双方都返回方法不存在错误）
			fmt.Printf("  %s 双方都返回错误（方法不存在/未实现），视为兼容\n", blue("→"))
		}
	}

	// 显示警告信息
	if result.Status == StatusWarning {
		if result.SkipReason != "" {
			fmt.Printf("  %s %s\n", yellow("→"), result.SkipReason)
		} else {
			fmt.Printf("  %s 发现 %d 处额外字段差异（不影响通过）:\n", yellow("→"), len(result.Differences))
		}
		if len(result.Differences) > 0 {
			r.printDifferences(result.Differences, 5)
		}
	}

	// 显示失败信息
	if result.Status == StatusFail {
		if result.GethError != "" && result.RethError == "" {
			fmt.Printf("  %s geth 错误: %s\n", yellow("→"), result.GethError)
		}
		if result.RethError != "" && result.GethError == "" {
			fmt.Printf("  %s reth 错误: %s\n", yellow("→"), result.RethError)
		}
		if result.GethError != "" && result.RethError != "" {
			fmt.Printf("  %s 双方错误不兼容:\n", yellow("→"))
			fmt.Printf("    geth: %s\n", truncateValue(result.GethError, 60))
			fmt.Printf("    reth: %s\n", truncateValue(result.RethError, 60))
		}
		if result.CompareError != "" {
			fmt.Printf("  %s 对比错误: %s\n", yellow("→"), result.CompareError)
		}
		if len(result.Differences) > 0 {
			fmt.Printf("  %s 发现 %d 处差异:\n", yellow("→"), len(result.Differences))
			r.printDifferences(result.Differences, 5)
		}
	}
}

// printDifferences 打印差异列表
func (r *Reporter) printDifferences(diffs []diff.Difference, limit int) {
	if len(diffs) < limit {
		limit = len(diffs)
	}
	for i := 0; i < limit; i++ {
		d := diffs[i]
		severityTag := ""
		if d.Severity == diff.SeverityWarning {
			severityTag = "[warn] "
		}
		fmt.Printf("    %s[%s] %s\n", severityTag, d.Type, d.Path)
		if d.Expected != nil {
			fmt.Printf("      geth: %v\n", truncateValue(d.Expected, 200))
		}
		if d.Actual != nil {
			fmt.Printf("      reth: %v\n", truncateValue(d.Actual, 200))
		}
	}
	if len(diffs) > limit {
		fmt.Printf("    ... 还有 %d 处差异\n", len(diffs)-limit)
	}
}

// Generate 生成最终报告
func (r *Reporter) Generate() *Report {
	report := &Report{
		Timestamp:  time.Now(),
		GethURL:    r.gethURL,
		RethURL:    r.rethURL,
		TotalTests: len(r.results),
		Results:    r.results,
	}

	for _, result := range r.results {
		switch result.Status {
		case StatusPass:
			report.PassedTests++
		case StatusCompatible:
			report.CompatibleTests++
		case StatusWarning:
			report.WarningTests++
		case StatusFail:
			report.FailedTests++
		}
	}

	report.Summary = fmt.Sprintf("总计 %d: %d 通过, %d 兼容, %d 警告, %d 失败",
		report.TotalTests, report.PassedTests, report.CompatibleTests,
		report.WarningTests, report.FailedTests)

	return report
}

// PrintSummary 打印摘要
func (r *Reporter) PrintSummary() {
	report := r.Generate()

	fmt.Println()
	fmt.Println(strings.Repeat("=", 60))
	fmt.Println("测试摘要")
	fmt.Println(strings.Repeat("=", 60))

	green := color.New(color.FgGreen).SprintFunc()
	red := color.New(color.FgRed).SprintFunc()
	cyan := color.New(color.FgCyan).SprintFunc()
	yellow := color.New(color.FgYellow).SprintFunc()
	blue := color.New(color.FgBlue).SprintFunc()

	fmt.Printf("geth: %s\n", cyan(report.GethURL))
	fmt.Printf("reth: %s\n", cyan(report.RethURL))
	fmt.Printf("耗时: %v\n", time.Since(r.startTime))
	fmt.Println()

	// 统计方法覆盖情况
	methodCounts := make(map[string]int)
	for _, result := range report.Results {
		methodCounts[result.TestCase.Method]++
	}
	uniqueMethods := len(methodCounts)

	fmt.Printf("方法覆盖: %s 个方法\n", cyan(uniqueMethods))

	// 找出有多个测试案例的方法
	var multiCaseMethods []string
	for method, count := range methodCounts {
		if count > 1 {
			multiCaseMethods = append(multiCaseMethods, fmt.Sprintf("%s: %d", method, count))
		}
	}
	if len(multiCaseMethods) > 0 {
		// 排序以保持输出稳定
		sort.Strings(multiCaseMethods)
		fmt.Println("多案例方法:")
		for _, m := range multiCaseMethods {
			fmt.Printf("  %s\n", m)
		}
	}
	fmt.Println()

	fmt.Printf("总计: %d | 通过: %s | 兼容: %s | 警告: %s | 失败: %s\n",
		report.TotalTests,
		green(report.PassedTests),
		blue(report.CompatibleTests),
		yellow(report.WarningTests),
		red(report.FailedTests))

	// 打印各类别详情
	if report.CompatibleTests > 0 {
		fmt.Println()
		fmt.Println(blue("兼容的测试:"))
		for _, result := range report.Results {
			if result.Status == StatusCompatible {
				reason := ""
				if result.SkipReason != "" {
					reason = fmt.Sprintf(" - %s", result.SkipReason)
				}
				fmt.Printf("  ≈ %s [%s]%s\n", result.TestCase.Method, result.TestCase.Name, reason)
			}
		}
	}

	if report.WarningTests > 0 {
		fmt.Println()
		fmt.Println(yellow("有警告的测试:"))
		for _, result := range report.Results {
			if result.Status == StatusWarning {
				reason := ""
				if result.SkipReason != "" {
					reason = fmt.Sprintf(" - %s", result.SkipReason)
				}
				fmt.Printf("  ⚠ %s [%s]%s\n", result.TestCase.Method, result.TestCase.Name, reason)
			}
		}
	}

	if report.FailedTests > 0 {
		fmt.Println()
		fmt.Println(red("失败的测试:"))
		for _, result := range report.Results {
			if result.Status == StatusFail {
				fmt.Printf("  ✗ %s [%s]\n", result.TestCase.Method, result.TestCase.Name)
			}
		}
	}
}

// SaveJSON 保存 JSON 报告
func (r *Reporter) SaveJSON(filename string) error {
	report := r.Generate()
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filename, data, 0644)
}

// HasFailures 检查是否有真正失败的测试（不包括 WARNING/SKIP/COMPATIBLE）
func (r *Reporter) HasFailures() bool {
	for _, result := range r.results {
		if result.Status == StatusFail {
			return true
		}
	}
	return false
}

// truncateValue 截断值用于显示
func truncateValue(v interface{}, maxLen int) string {
	data, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("%v", v)
	}
	s := string(data)
	if len(s) > maxLen {
		return s[:maxLen] + "..."
	}
	return s
}
