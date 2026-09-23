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
	TestName        string                  `json:"test_name"`
	Reason          string                  `json:"reason,omitempty"`
	AppliesTo       *KnownDiffApplicability `json:"applies_to,omitempty"`
	BaselineExample json.RawMessage         `json:"baseline_example,omitempty"`
	TargetExample   json.RawMessage         `json:"target_example,omitempty"`
}

// EndpointMatcher selects client names and versions for a known difference.
type EndpointMatcher struct {
	NamePattern    string `json:"name_pattern,omitempty"`
	VersionPattern string `json:"version_pattern,omitempty"`
}

// KnownDiffApplicability limits a known difference to the compared endpoints.
type KnownDiffApplicability struct {
	Baseline *EndpointMatcher `json:"baseline,omitempty"`
	Target   *EndpointMatcher `json:"target,omitempty"`
}

func (m *EndpointMatcher) matches(endpoint EndpointMetadata) bool {
	if m == nil {
		return true
	}
	for _, pair := range [][2]string{{m.NamePattern, endpoint.Name}, {m.VersionPattern, endpoint.ClientVersion}} {
		if pair[0] == "" {
			continue
		}
		matched, err := regexp.MatchString(pair[0], pair[1])
		if err != nil || !matched {
			return false
		}
	}
	return true
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
	TestCase         TestCase          `json:"test_case"`
	Status           TestStatus        `json:"status"`
	Passed           bool              `json:"passed"` // true unless Status is FAIL, for existing consumers
	BaselineResponse json.RawMessage   `json:"baseline_response,omitempty"`
	TargetResponse   json.RawMessage   `json:"target_response,omitempty"`
	BaselineError    string            `json:"baseline_error,omitempty"`
	TargetError      string            `json:"target_error,omitempty"`
	BaselineDuration time.Duration     `json:"baseline_duration"`
	TargetDuration   time.Duration     `json:"target_duration"`
	Differences      []diff.Difference `json:"differences,omitempty"`
	CompareError     string            `json:"compare_error,omitempty"`
	SkipReason       string            `json:"skip_reason,omitempty"` // reason for a skipped or compatible result
}

// EndpointMetadata identifies one side of a comparison.
type EndpointMetadata struct {
	Name          string `json:"name"`
	URL           string `json:"url"`
	ClientVersion string `json:"client_version"`
}

const reportSchemaVersion = 2

// Report contains the complete comparison run.
type Report struct {
	SchemaVersion   int              `json:"schema_version"`
	Timestamp       time.Time        `json:"timestamp"`
	Baseline        EndpointMetadata `json:"baseline"`
	Target          EndpointMetadata `json:"target"`
	TotalTests      int              `json:"total_tests"`
	PassedTests     int              `json:"passed_tests"`
	CompatibleTests int              `json:"compatible_tests"`
	WarningTests    int              `json:"warning_tests"`
	FailedTests     int              `json:"failed_tests"`
	Results         []TestResult     `json:"results"`
	Summary         string           `json:"summary"`
}

// Reporter collects results and renders reports.
type Reporter struct {
	results    []TestResult
	baseline   EndpointMetadata
	target     EndpointMetadata
	startTime  time.Time
	verbose    bool
	knownDiffs map[string]KnownDiff // key: test_name
}

// NewReporter creates a result collector.
func NewReporter(baseline, target EndpointMetadata, verbose bool) *Reporter {
	return &Reporter{
		results:    []TestResult{},
		baseline:   baseline,
		target:     target,
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
		if kd.AppliesTo != nil {
			for _, matcher := range []*EndpointMatcher{kd.AppliesTo.Baseline, kd.AppliesTo.Target} {
				if matcher == nil {
					continue
				}
				for _, pattern := range []string{matcher.NamePattern, matcher.VersionPattern} {
					if pattern == "" {
						continue
					}
					if _, err := regexp.Compile(pattern); err != nil {
						return fmt.Errorf("known difference %q has invalid applicability pattern %q: %w", kd.TestName, pattern, err)
					}
				}
			}
		}
		r.knownDiffs[kd.TestName] = kd
	}

	return nil
}

// GetKnownDiff returns the rule for a test case, if one exists.
func (r *Reporter) GetKnownDiff(testName string) *KnownDiff {
	if kd, ok := r.knownDiffs[testName]; ok {
		if kd.AppliesTo != nil && (!kd.AppliesTo.Baseline.matches(r.baseline) || !kd.AppliesTo.Target.matches(r.target)) {
			return nil
		}
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
	if compareResult.BaselineResponse != nil {
		result.BaselineDuration = compareResult.BaselineResponse.Duration
		if compareResult.BaselineResponse.Error != nil {
			result.BaselineError = compareResult.BaselineResponse.Error.Error()
		}
		if compareResult.BaselineResponse.RawBody != nil {
			result.BaselineResponse = compareResult.BaselineResponse.RawBody
		}
	}

	// Preserve the target response, including transport failures.
	if compareResult.TargetResponse != nil {
		result.TargetDuration = compareResult.TargetResponse.Duration
		if compareResult.TargetResponse.Error != nil {
			result.TargetError = compareResult.TargetResponse.Error.Error()
		}
		if compareResult.TargetResponse.RawBody != nil {
			result.TargetResponse = compareResult.TargetResponse.RawBody
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
// The baseline reference side compares type and code because its wording varies
// with input and version, and recorded examples may be truncated. The target
// target side also compares normalized messages, allowing either message to
// contain the other when an example was truncated. A changed target message
// stops matching so the difference becomes visible again in the report.
func (r *Reporter) matchesKnownDiff(result *TestResult, knownDiff *KnownDiff) bool {
	return shapeMatch(parseShape(knownDiff.BaselineExample), parseShape(result.BaselineResponse), false) &&
		shapeMatch(parseShape(knownDiff.TargetExample), parseShape(result.TargetResponse), true)
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
	if result.BaselineError != "" || result.TargetError != "" {
		return StatusFail
	}

	if compareErr != nil {
		result.CompareError = compareErr.Error()
		return StatusFail
	}

	baselineHasRPCError := compareResult.BaselineResponse != nil &&
		compareResult.BaselineResponse.Response != nil &&
		compareResult.BaselineResponse.Response.Error != nil
	targetHasRPCError := compareResult.TargetResponse != nil &&
		compareResult.TargetResponse.Response != nil &&
		compareResult.TargetResponse.Response.Error != nil

	if baselineHasRPCError || targetHasRPCError {
		status, diffs, reason := r.checkRPCErrorCompatibility(compareResult, baselineHasRPCError, targetHasRPCError)
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
			targetExtraCount := 0
			baselineExtraCount := 0
			for _, d := range diffResult.Differences {
				if d.Severity == diff.SeverityWarning {
					if d.Type == diff.DiffTypeExtra {
						targetExtraCount++
					} else if d.Type == diff.DiffTypeMissing {
						baselineExtraCount++
					}
				}
			}

			var reasonParts []string
			if targetExtraCount > 0 {
				reasonParts = append(reasonParts, fmt.Sprintf("target 有 %d 处额外字段", targetExtraCount))
			}
			if baselineExtraCount > 0 {
				reasonParts = append(reasonParts, fmt.Sprintf("baseline 有 %d 处额外字段", baselineExtraCount))
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
func (r *Reporter) checkRPCErrorCompatibility(compareResult *rpc.CompareResult, baselineHasError, targetHasError bool) (TestStatus, []diff.Difference, string) {
	var diffs []diff.Difference

	// An error on only one side is a failure.
	if baselineHasError != targetHasError {
		if baselineHasError {
			baselineErr := compareResult.BaselineResponse.Response.Error
			diffs = append(diffs, diff.Difference{
				Path:     "error",
				Type:     diff.DiffTypeExtra,
				Expected: fmt.Sprintf("code=%d, message=%s", baselineErr.Code, baselineErr.Message),
				Actual:   nil,
				Message:  fmt.Sprintf("%s returned an error; %s succeeded", r.baseline.Name, r.target.Name),
			})
		} else {
			targetErr := compareResult.TargetResponse.Response.Error
			diffs = append(diffs, diff.Difference{
				Path:     "error",
				Type:     diff.DiffTypeMissing,
				Expected: nil,
				Actual:   fmt.Sprintf("code=%d, message=%s", targetErr.Code, targetErr.Message),
				Message:  fmt.Sprintf("%s succeeded; %s returned an error", r.baseline.Name, r.target.Name),
			})
		}
		return StatusFail, diffs, ""
	}

	if baselineHasError && targetHasError {
		baselineErr := compareResult.BaselineResponse.Response.Error
		targetErr := compareResult.TargetResponse.Response.Error

		baselineErrObj := map[string]interface{}{
			"code":    baselineErr.Code,
			"message": baselineErr.Message,
		}
		targetErrObj := map[string]interface{}{
			"code":    targetErr.Code,
			"message": targetErr.Message,
		}

		compat := diff.CheckErrorCompatibility(baselineErrObj, targetErrObj)
		switch compat {
		case diff.ErrorCompatIdentical:
			return StatusPass, nil, ""
		case diff.ErrorCompatMethodNotFound:
			reason := fmt.Sprintf("both clients reported an unsupported method (%s: %d, %s: %d)",
				r.baseline.Name, baselineErr.Code, r.target.Name, targetErr.Code)
			return StatusCompatible, nil, reason
		}

		if baselineErr.Code != targetErr.Code {
			diffs = append(diffs, diff.Difference{
				Path:     "error.code",
				Type:     diff.DiffTypeValue,
				Expected: baselineErr.Code,
				Actual:   targetErr.Code,
				Message:  "错误码不同",
			})
		}
		if baselineErr.Message != targetErr.Message {
			diffs = append(diffs, diff.Difference{
				Path:     "error.message",
				Type:     diff.DiffTypeValue,
				Expected: baselineErr.Message,
				Actual:   targetErr.Message,
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
		fmt.Printf("  %s: %v, %s: %v\n", r.baseline.Name, result.BaselineDuration, r.target.Name, result.TargetDuration)
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
		if result.BaselineError != "" && result.TargetError == "" {
			fmt.Printf("  %s %s error: %s\n", yellow("→"), r.baseline.Name, result.BaselineError)
		}
		if result.TargetError != "" && result.BaselineError == "" {
			fmt.Printf("  %s %s error: %s\n", yellow("→"), r.target.Name, result.TargetError)
		}
		if result.BaselineError != "" && result.TargetError != "" {
			fmt.Printf("  %s 双方错误不兼容:\n", yellow("→"))
			fmt.Printf("    %s: %s\n", r.baseline.Name, truncateValue(result.BaselineError, 60))
			fmt.Printf("    %s: %s\n", r.target.Name, truncateValue(result.TargetError, 60))
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
			fmt.Printf("      %s: %v\n", r.baseline.Name, truncateValue(d.Expected, 200))
		}
		if d.Actual != nil {
			fmt.Printf("      %s: %v\n", r.target.Name, truncateValue(d.Actual, 200))
		}
	}
	if len(diffs) > limit {
		fmt.Printf("    ... 还有 %d 处差异\n", len(diffs)-limit)
	}
}

// Generate builds the final report.
func (r *Reporter) Generate() *Report {
	report := &Report{
		SchemaVersion: reportSchemaVersion,
		Timestamp:     time.Now(),
		Baseline:      r.baseline,
		Target:        r.target,
		TotalTests:    len(r.results),
		Results:       r.results,
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

	fmt.Printf("%s: %s\n", report.Baseline.Name, cyan(report.Baseline.URL))
	fmt.Printf("%s: %s\n", report.Target.Name, cyan(report.Target.URL))
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
