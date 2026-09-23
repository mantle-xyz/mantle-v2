// Package report records and renders RPC comparison results.
package report

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/ethereum-optimism/optimism/op-rpc-compat/pkg/diff"
	"github.com/ethereum-optimism/optimism/op-rpc-compat/pkg/rpc"

	"github.com/fatih/color"
)

// TestStatus classifies a test result.
type TestStatus string

const (
	StatusPass       TestStatus = "PASS"       // identical responses
	StatusCompatible TestStatus = "COMPATIBLE" // verified known difference or unsupported method on both sides
	StatusWarning    TestStatus = "WARNING"    // nonfatal difference such as an extra field
	StatusFail       TestStatus = "FAIL"       // failing difference
)

// KnownDiff describes an expected response difference.
type KnownDiff struct {
	TestName    string          `json:"test_name"`              // test case name
	Reason      string          `json:"reason,omitempty"`       // explanation of the difference
	GethExample json.RawMessage `json:"geth_example,omitempty"` // expected geth response example
	RethExample json.RawMessage `json:"reth_example,omitempty"` // expected reth response example
}

// KnownDiffsConfig is the known-difference file format.
type KnownDiffsConfig struct {
	KnownDiffs []KnownDiff `json:"known_diffs"`
}

// TestCase is a JSON-RPC comparison case.
type TestCase struct {
	Name        string      `json:"name"`
	Method      string      `json:"method"`
	Params      interface{} `json:"params,omitempty"`
	Description string      `json:"description,omitempty"`
}

// TestResult records the outcome of one case.
type TestResult struct {
	TestCase     TestCase          `json:"test_case"`
	Status       TestStatus        `json:"status"`
	Passed       bool              `json:"passed"` // true unless Status is FAIL, for existing consumers
	GethResponse json.RawMessage   `json:"geth_response,omitempty"`
	RethResponse json.RawMessage   `json:"reth_response,omitempty"`
	GethError    string            `json:"geth_error,omitempty"`
	RethError    string            `json:"reth_error,omitempty"`
	GethDuration time.Duration     `json:"geth_duration"`
	RethDuration time.Duration     `json:"reth_duration"`
	Differences  []diff.Difference `json:"differences,omitempty"`
	CompareError string            `json:"compare_error,omitempty"`
	SkipReason   string            `json:"skip_reason,omitempty"` // reason for a skipped or compatible result
}

// Report contains the complete comparison run.
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

// Reporter collects results and renders reports.
type Reporter struct {
	results    []TestResult
	gethURL    string
	rethURL    string
	startTime  time.Time
	verbose    bool
	knownDiffs map[string]KnownDiff // key: test_name
}

// NewReporter creates a result collector.
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

// LoadKnownDiffs loads known-difference rules from a reader.
func (r *Reporter) LoadKnownDiffs(reader io.Reader) error {
	data, err := io.ReadAll(reader)
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

// GetKnownDiff returns the rule for a test case, if one exists.
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

	// Preserve the reference response, including transport failures.
	if compareResult.PrimaryResponse != nil {
		result.GethDuration = compareResult.PrimaryResponse.Duration
		if compareResult.PrimaryResponse.Error != nil {
			result.GethError = compareResult.PrimaryResponse.Error.Error()
		}
		if compareResult.PrimaryResponse.RawBody != nil {
			result.GethResponse = compareResult.PrimaryResponse.RawBody
		}
	}

	// Preserve the target response, including transport failures.
	if compareResult.SecondaryResponse != nil {
		result.RethDuration = compareResult.SecondaryResponse.Duration
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

// AddResult records the comparison result of one test case.
func (r *Reporter) AddResult(tc TestCase, compareResult *rpc.CompareResult, diffResult *diff.CompareResult, compareErr error) {
	result := newTestResult(tc, compareResult)

	if knownDiff := r.GetKnownDiff(tc.Name); knownDiff != nil {
		// Waive only a response that still matches the recorded known difference.
		if r.matchesKnownDiff(&result, knownDiff) {
			result.Status = StatusCompatible
			result.SkipReason = knownDiff.Reason // preserve the known-difference reason in the existing field
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

	result.Status = r.determineStatus(&result, compareResult, diffResult, compareErr)

	// Warnings, skips, and compatible differences do not fail the run.
	result.Passed = result.Status != StatusFail

	r.results = append(r.results, result)

	r.printResult(result)
}

// matchesKnownDiff checks actual responses against a recorded difference.
//
// Comparing only error versus success would hide later code or message drift.
// The geth reference side compares type and code because its wording varies
// with input and version, and recorded examples may be truncated. The reth
// target side also compares normalized messages, allowing either message to
// contain the other when an example was truncated. A changed target message
// stops matching so the difference becomes visible again in the report.
func (r *Reporter) matchesKnownDiff(result *TestResult, knownDiff *KnownDiff) bool {
	return shapeMatch(parseShape(knownDiff.GethExample), parseShape(result.GethResponse), false) &&
		shapeMatch(parseShape(knownDiff.RethExample), parseShape(result.RethResponse), true)
}

var (
	hexRe = regexp.MustCompile(`0x[0-9a-fA-F]+`)
	numRe = regexp.MustCompile(`\d+`)
)

// normMsg removes variable hex and numeric values such as addresses, gas, and block numbers.
func normMsg(s string) string {
	s = hexRe.ReplaceAllString(s, "0xX")
	s = numRe.ReplaceAllString(s, "N")
	return strings.ToLower(strings.TrimSpace(s))
}

// respShape extracts the comparable shape of a response.
type respShape struct {
	typ  responseType
	code int
	msg  string // normalized error message, meaningful only for errors
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

// shapeMatch checks an actual response against a recorded shape.
// When matchMsg is true, it also compares normalized error messages.
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
		// Recorded examples may be truncated on either side.
		return strings.Contains(actual.msg, expected.msg) || strings.Contains(expected.msg, actual.msg)
	}
	// Successful results may contain variable values such as versions or filter IDs.
	return true
}

// responseType classifies a response as success, error, or unknown.
type responseType int

const (
	responseTypeUnknown responseType = iota
	responseTypeSuccess              // result field present
	responseTypeError                // error field present
)

// determineStatus classifies a comparison result.
func (r *Reporter) determineStatus(result *TestResult, compareResult *rpc.CompareResult, diffResult *diff.CompareResult, compareErr error) TestStatus {
	// Transport failures are always test failures.
	if result.GethError != "" || result.RethError != "" {
		return StatusFail
	}

	if compareErr != nil {
		result.CompareError = compareErr.Error()
		return StatusFail
	}

	gethHasRPCError := compareResult.PrimaryResponse != nil &&
		compareResult.PrimaryResponse.Response != nil &&
		compareResult.PrimaryResponse.Response.Error != nil
	rethHasRPCError := compareResult.SecondaryResponse != nil &&
		compareResult.SecondaryResponse.Response != nil &&
		compareResult.SecondaryResponse.Response.Error != nil

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

	if diffResult != nil {
		result.Differences = diffResult.Differences

		if len(diffResult.Differences) == 0 {
			return StatusPass
		}

		// Extra fields and other warning-level differences do not fail the test.
		if diffResult.FailCount == 0 && diffResult.WarningCount > 0 {
			// Count extra and missing fields separately for the target.
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

		if diffResult.FailCount > 0 {
			return StatusFail
		}
	}

	return StatusPass
}

// checkRPCErrorCompatibility returns status, differences, and a reason for two RPC errors.
func (r *Reporter) checkRPCErrorCompatibility(compareResult *rpc.CompareResult, gethHasError, rethHasError bool) (TestStatus, []diff.Difference, string) {
	var diffs []diff.Difference

	// An error on only one side is a failure.
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

	if gethHasError && rethHasError {
		gethErr := compareResult.PrimaryResponse.Response.Error
		rethErr := compareResult.SecondaryResponse.Response.Error

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

// printResult renders one result.
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

	if result.Status == StatusCompatible {
		if result.SkipReason != "" {
			fmt.Printf("  %s 已知差异: %s\n", blue("→"), result.SkipReason)
		} else {
			// Both clients reported an unsupported method.
			fmt.Printf("  %s 双方都返回错误（方法不存在/未实现），视为兼容\n", blue("→"))
		}
	}

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

// printDifferences renders a list of differences.
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

// Generate builds the final report.
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

// PrintSummary renders the run summary.
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

	methodCounts := make(map[string]int)
	for _, result := range report.Results {
		methodCounts[result.TestCase.Method]++
	}
	uniqueMethods := len(methodCounts)

	fmt.Printf("方法覆盖: %s 个方法\n", cyan(uniqueMethods))

	var multiCaseMethods []string
	for method, count := range methodCounts {
		if count > 1 {
			multiCaseMethods = append(multiCaseMethods, fmt.Sprintf("%s: %d", method, count))
		}
	}
	if len(multiCaseMethods) > 0 {
		// Sort methods for stable output.
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

// SaveJSON writes the report as JSON.
func (r *Reporter) SaveJSON(filename string) error {
	report := r.Generate()
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filename, data, 0644)
}

// HasFailures reports whether any result failed, excluding warnings, skips, and compatible differences.
func (r *Reporter) HasFailures() bool {
	for _, result := range r.results {
		if result.Status == StatusFail {
			return true
		}
	}
	return false
}

// truncateValue limits a value's display length.
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
