// Package report records and renders RPC comparison results.
package report

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/ethereum-optimism/optimism/op-rpc-compat/pkg/diff"
	"github.com/ethereum-optimism/optimism/op-rpc-compat/pkg/policy"
	"github.com/ethereum-optimism/optimism/op-rpc-compat/pkg/rpc"
	"github.com/ethereum-optimism/optimism/op-rpc-compat/pkg/semantic"

	"github.com/fatih/color"
)

// TestStatus classifies a test result.
type TestStatus string

const (
	StatusPass          TestStatus = "PASS"       // identical responses
	StatusCompatible    TestStatus = "COMPATIBLE" // explicitly asserted scenario behavior
	StatusWarning       TestStatus = "WARNING"    // accepted reviewed difference
	StatusFail          TestStatus = "FAIL"       // failing difference
	StatusInconclusive  TestStatus = "INCONCLUSIVE"
	StatusNotApplicable TestStatus = "NOT_APPLICABLE"
)

// TestCase is a JSON-RPC comparison case.
type TestCase struct {
	Name                  string      `json:"name"`
	Method                string      `json:"method"`
	Params                interface{} `json:"params,omitempty"`
	Description           string      `json:"description,omitempty"`
	CorpusID              string      `json:"-"`
	RequestTemplateSHA256 string      `json:"-"`
	TemplateError         string      `json:"-"`
	SnapshotTags          []string    `json:"-"`
}

// TestResult records the outcome of one case.
type TestResult struct {
	TestCase              TestCase          `json:"test_case"`
	CorpusID              string            `json:"corpus_id,omitempty"`
	RequestTemplateSHA256 string            `json:"request_template_sha256,omitempty"`
	ObservedStatus        TestStatus        `json:"observed_status"`
	Status                TestStatus        `json:"status"`
	EffectiveStatus       TestStatus        `json:"effective_status"`
	Passed                bool              `json:"passed"` // true unless Status is FAIL, for existing consumers
	BaselineResponse      json.RawMessage   `json:"baseline_response,omitempty"`
	TargetResponse        json.RawMessage   `json:"target_response,omitempty"`
	BaselineRawBodyBase64 string            `json:"baseline_raw_body_base64,omitempty"`
	TargetRawBodyBase64   string            `json:"target_raw_body_base64,omitempty"`
	BaselineError         string            `json:"baseline_error,omitempty"`
	TargetError           string            `json:"target_error,omitempty"`
	BaselineDuration      time.Duration     `json:"baseline_duration"`
	TargetDuration        time.Duration     `json:"target_duration"`
	Differences           []diff.Difference `json:"differences,omitempty"`
	CompareError          string            `json:"compare_error,omitempty"`
	SemanticAssertionID   string            `json:"semantic_assertion_id,omitempty"`
	SemanticError         string            `json:"semantic_error,omitempty"`
	SkipReason            string            `json:"skip_reason,omitempty"` // reason for a skipped or compatible result
}

// EndpointMetadata identifies one side of a comparison.
type EndpointMetadata struct {
	Name           string `json:"name"`
	URL            string `json:"url"`
	ClientVersion  string `json:"client_version"`
	ClientFamily   string `json:"client_family,omitempty"`
	FamilySource   string `json:"family_source,omitempty"`
	BuildID        string `json:"build_id,omitempty"`
	IdentitySource string `json:"identity_source,omitempty"`
	ChainID        string `json:"chain_id,omitempty"`
	GenesisHash    string `json:"genesis_hash,omitempty"`
}

type ExcludedCase struct {
	Name     string `json:"name"`
	Method   string `json:"method"`
	CorpusID string `json:"corpus_id,omitempty"`
	Reason   string `json:"reason"`
}

const reportSchemaVersion = 4

// Report contains the complete comparison run.
type Report struct {
	SchemaVersion      int              `json:"schema_version"`
	PolicyMode         string           `json:"policy_mode,omitempty"`
	RegistryID         string           `json:"registry_id,omitempty"`
	RegistryDigest     string           `json:"registry_digest,omitempty"`
	StaleRuleIDs       []string         `json:"stale_rule_ids,omitempty"`
	SelectedSuite      string           `json:"selected_suite,omitempty"`
	ExcludedTests      int              `json:"excluded_tests,omitempty"`
	ExcludedCases      []ExcludedCase   `json:"excluded_cases,omitempty"`
	Timestamp          time.Time        `json:"timestamp"`
	Baseline           EndpointMetadata `json:"baseline"`
	Target             EndpointMetadata `json:"target"`
	TotalTests         int              `json:"total_tests"`
	PassedTests        int              `json:"passed_tests"`
	CompatibleTests    int              `json:"compatible_tests"`
	WarningTests       int              `json:"warning_tests"`
	FailedTests        int              `json:"failed_tests"`
	InconclusiveTests  int              `json:"inconclusive_tests"`
	NotApplicableTests int              `json:"not_applicable_tests"`
	Results            []TestResult     `json:"results"`
	Summary            string           `json:"summary"`
}

// Reporter collects results and renders reports.
type Reporter struct {
	results          []TestResult
	baseline         EndpointMetadata
	target           EndpointMetadata
	startTime        time.Time
	verbose          bool
	policyRegistry   *policy.Registry
	policyMode       string
	matchedRuleIDs   map[string]bool
	executedContexts []policy.MatchContext
	selectedSuite    string
	excludedCases    []ExcludedCase
}

func (r *Reporter) SetSuite(suite string) {
	r.selectedSuite = suite
}

func (r *Reporter) AddExcludedCase(tc TestCase, reason string) {
	r.excludedCases = append(r.excludedCases, ExcludedCase{
		Name: tc.Name, Method: tc.Method, CorpusID: tc.CorpusID, Reason: reason,
	})
}

// NewReporter creates a result collector.
func NewReporter(baseline, target EndpointMetadata, verbose bool) *Reporter {
	if baseline.ClientFamily == "" {
		baseline.ClientFamily = policy.ClientFamilyFromClientVersion(baseline.ClientVersion)
		if baseline.ClientFamily != "" {
			baseline.FamilySource = "rpc_version"
		}
	}
	if target.ClientFamily == "" {
		target.ClientFamily = policy.ClientFamilyFromClientVersion(target.ClientVersion)
		if target.ClientFamily != "" {
			target.FamilySource = "rpc_version"
		}
	}
	return &Reporter{
		results:        []TestResult{},
		baseline:       baseline,
		target:         target,
		startTime:      time.Now(),
		verbose:        verbose,
		matchedRuleIDs: make(map[string]bool),
	}
}

func (r *Reporter) ConfigurePolicy(registry *policy.Registry, mode string) error {
	if mode != "accepted" && mode != "strict" {
		return fmt.Errorf("unknown diff policy %q", mode)
	}
	if registry == nil {
		return fmt.Errorf("missing accepted registry")
	}
	if err := registry.Validate(); err != nil {
		return err
	}
	r.policyRegistry = registry
	r.policyMode = mode
	return nil
}

func newTestResult(tc TestCase, compareResult *rpc.CompareResult) TestResult {
	result := TestResult{
		TestCase:              tc,
		CorpusID:              tc.CorpusID,
		RequestTemplateSHA256: tc.RequestTemplateSHA256,
		Status:                StatusPass,
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
			body := compareResult.BaselineResponse.RawBody
			if json.Valid(body) {
				result.BaselineResponse = body
			}
			if compareResult.BaselineResponse.Error != nil || !json.Valid(body) {
				result.BaselineRawBodyBase64 = base64.StdEncoding.EncodeToString(body)
			}
		}
	}

	// Preserve the target response, including transport failures.
	if compareResult.TargetResponse != nil {
		result.TargetDuration = compareResult.TargetResponse.Duration
		if compareResult.TargetResponse.Error != nil {
			result.TargetError = compareResult.TargetResponse.Error.Error()
		}
		if compareResult.TargetResponse.RawBody != nil {
			body := compareResult.TargetResponse.RawBody
			if json.Valid(body) {
				result.TargetResponse = body
			}
			if compareResult.TargetResponse.Error != nil || !json.Valid(body) {
				result.TargetRawBodyBase64 = base64.StdEncoding.EncodeToString(body)
			}
		}
	}
	return result
}

// AddCompatibleResult records an expected, scenario-specific RPC response while preserving the
// actual response in the report. The caller must perform the later behavioral assertion.
func (r *Reporter) AddCompatibleResult(tc TestCase, compareResult *rpc.CompareResult, reason string) {
	result := newTestResult(tc, compareResult)
	result.Status = StatusCompatible
	result.ObservedStatus = StatusCompatible
	result.Passed = true
	result.SkipReason = reason
	r.results = append(r.results, result)
	r.printResult(result)
}

func (r *Reporter) AddAssertionResult(tc TestCase, compareResult *rpc.CompareResult, assertionErr error) {
	result := newTestResult(tc, compareResult)
	if assertionErr == nil {
		result.Status = StatusPass
		result.Passed = true
	} else {
		result.Status = StatusFail
		result.CompareError = assertionErr.Error()
	}
	result.ObservedStatus = result.Status
	r.results = append(r.results, result)
	r.printResult(result)
}

func (r *Reporter) AddNotApplicableResult(tc TestCase, reason string) {
	result := TestResult{TestCase: tc, CorpusID: tc.CorpusID, RequestTemplateSHA256: tc.RequestTemplateSHA256,
		Status: StatusNotApplicable, ObservedStatus: StatusNotApplicable,
		SkipReason: reason}
	r.results = append(r.results, result)
	r.printResult(result)
}

func (r *Reporter) AddInconclusiveResult(tc TestCase, compareResult *rpc.CompareResult, reason string) {
	result := newTestResult(tc, compareResult)
	result.Status = StatusInconclusive
	result.ObservedStatus = StatusInconclusive
	result.SkipReason = reason
	r.results = append(r.results, result)
	r.printResult(result)
}

// AddScenarioResult includes a transaction assertion in the shared JSON report.
func (r *Reporter) AddScenarioResult(name, category string, passed bool, detail string) {
	result := TestResult{TestCase: TestCase{Name: name, Method: "transaction", Description: category}}
	if passed {
		result.Status = StatusPass
		result.ObservedStatus = StatusPass
		result.Passed = true
	} else {
		result.Status = StatusFail
		result.ObservedStatus = StatusFail
		result.CompareError = detail
		if result.CompareError == "" {
			result.CompareError = "transaction assertion failed"
		}
	}
	r.results = append(r.results, result)
}

// AddResult records the comparison result of one test case.
func (r *Reporter) AddResult(tc TestCase, compareResult *rpc.CompareResult, diffResult *diff.CompareResult, compareErr error) {
	result := newTestResult(tc, compareResult)

	result.Status = r.determineStatus(&result, compareResult, diffResult, compareErr)
	result.ObservedStatus = result.Status
	if r.policyRegistry != nil && result.BaselineError == "" && result.TargetError == "" && compareErr == nil &&
		compareResult != nil && compareResult.BaselineResponse != nil && compareResult.TargetResponse != nil &&
		compareResult.BaselineResponse.Response != nil && compareResult.TargetResponse.Response != nil {
		r.executedContexts = append(r.executedContexts, r.matchContext(tc))
	}
	r.applyPolicy(tc, &result)

	// Warnings, skips, and compatible differences do not fail the run.
	result.Passed = result.Status != StatusFail

	r.results = append(r.results, result)

	r.printResult(result)
}

func (r *Reporter) applyPolicy(tc TestCase, result *TestResult) {
	if r.policyRegistry == nil || result.Status != StatusFail || result.BaselineError != "" ||
		result.TargetError != "" || result.CompareError != "" || len(result.Differences) == 0 {
		return
	}
	context := r.matchContext(tc)
	assertion := semantic.Evaluate(semantic.Case{
		CorpusID: context.CorpusID, CaseID: context.CaseID,
		Method: context.Method, RequestSHA256: context.RequestSHA256,
	}, result.BaselineResponse, result.TargetResponse)
	if assertion != nil {
		result.SemanticAssertionID = assertion.ID
		if assertion.Err != nil {
			result.SemanticError = assertion.Err.Error()
			return
		}
		for _, difference := range result.Differences {
			if assertion.Covers(difference) && r.policyRegistry.Match(context, difference) != nil {
				result.SemanticError = "semantic assertion overlaps a reviewed difference rule"
				return
			}
		}
	}
	allAccepted := true
	var matchedIDs []string
	matchedNames := make(map[string]bool)
	for i := range result.Differences {
		difference := &result.Differences[i]
		difference.EffectiveSeverity = difference.Severity
		if difference.Severity != diff.SeverityFail {
			allAccepted = false
			continue
		}
		if assertion != nil && assertion.Covers(*difference) {
			difference.SemanticAssertionID = assertion.ID
			if r.policyMode == "accepted" {
				name := "assertion:" + assertion.ID
				if !matchedNames[name] {
					matchedIDs = append(matchedIDs, name)
					matchedNames[name] = true
				}
				difference.EffectiveSeverity = diff.SeverityWarning
				continue
			}
			allAccepted = false
			continue
		}
		if rule := r.policyRegistry.Match(context, *difference); rule != nil {
			difference.RuleID = rule.ID
			difference.RuleReason = rule.Reason
			difference.ScopeKind = rule.Scope.Kind
			difference.ScopeBaseline = rule.Scope.Baseline.Descriptor()
			difference.ScopeTarget = rule.Scope.Target.Descriptor()
			r.matchedRuleIDs[rule.ID] = true
			if r.policyMode == "accepted" {
				if !matchedNames[rule.ID] {
					matchedIDs = append(matchedIDs, rule.ID)
					matchedNames[rule.ID] = true
				}
				difference.EffectiveSeverity = diff.SeverityWarning
				continue
			}
		}
		allAccepted = false
	}
	if allAccepted && r.policyMode == "accepted" {
		result.Status = StatusWarning
		sort.Strings(matchedIDs)
		result.SkipReason = "reviewed rules: " + strings.Join(matchedIDs, ", ")
	}
}

func (r *Reporter) matchContext(tc TestCase) policy.MatchContext {
	digest := tc.RequestTemplateSHA256
	if digest == "" {
		digest = policy.RequestDigest(tc.Method, tc.Params)
	}
	return policy.MatchContext{
		Baseline: policy.Endpoint{BuildID: r.baseline.BuildID, ClientVersion: r.baseline.ClientVersion},
		Target:   policy.Endpoint{BuildID: r.target.BuildID, ClientVersion: r.target.ClientVersion},
		ChainID:  r.baseline.ChainID, GenesisHash: r.baseline.GenesisHash,
		CorpusID: tc.CorpusID,
		CaseID:   tc.Name, Method: tc.Method, RequestSHA256: digest,
	}
}

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
	if compareResult == nil || compareResult.BaselineResponse == nil || compareResult.TargetResponse == nil ||
		compareResult.BaselineResponse.Response == nil || compareResult.TargetResponse.Response == nil {
		result.CompareError = "incomplete paired RPC response"
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
	if diffResult == nil {
		result.CompareError = "successful responses were not compared"
		return StatusFail
	}

	result.Differences = diffResult.Differences
	if len(result.Differences) > 0 {
		for i := range result.Differences {
			if result.Differences[i].Severity == "" {
				result.Differences[i].Severity = diff.SeverityFail
			}
		}
		return StatusFail
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
				Path:            "error",
				Pointer:         "/error",
				Type:            diff.DiffTypeMissing,
				Severity:        diff.SeverityFail,
				ExpectedPresent: true,
				ActualPresent:   false,
				Expected:        baselineErr,
				Actual:          nil,
				Message:         fmt.Sprintf("%s returned an error; %s succeeded", r.baseline.Name, r.target.Name),
			})
			diffs = append(diffs, diff.Difference{
				Path: "result", Pointer: "/result", Type: diff.DiffTypeExtra,
				Severity: diff.SeverityFail, ExpectedPresent: false, ActualPresent: true,
				Actual:  compareResult.TargetResponse.Response.Result,
				Message: "target returned a success result",
			})
		} else {
			targetErr := compareResult.TargetResponse.Response.Error
			diffs = append(diffs, diff.Difference{
				Path:            "error",
				Pointer:         "/error",
				Type:            diff.DiffTypeExtra,
				Severity:        diff.SeverityFail,
				ExpectedPresent: false,
				ActualPresent:   true,
				Expected:        nil,
				Actual:          targetErr,
				Message:         fmt.Sprintf("%s succeeded; %s returned an error", r.baseline.Name, r.target.Name),
			})
			diffs = append(diffs, diff.Difference{
				Path: "result", Pointer: "/result", Type: diff.DiffTypeMissing,
				Severity: diff.SeverityFail, ExpectedPresent: true, ActualPresent: false,
				Expected: compareResult.BaselineResponse.Response.Result,
				Message:  "baseline returned a success result",
			})
		}
		return StatusFail, diffs, ""
	}

	if baselineHasError && targetHasError {
		baselineErr := compareResult.BaselineResponse.Response.Error
		targetErr := compareResult.TargetResponse.Response.Error

		if baselineErr.Code != targetErr.Code {
			diffs = append(diffs, diff.Difference{
				Path:            "error.code",
				Pointer:         "/error/code",
				Type:            diff.DiffTypeValue,
				Severity:        diff.SeverityFail,
				ExpectedPresent: true,
				ActualPresent:   true,
				Expected:        baselineErr.Code,
				Actual:          targetErr.Code,
				Message:         "error codes differ",
			})
		}
		if baselineErr.Message != targetErr.Message {
			diffs = append(diffs, diff.Difference{
				Path:            "error.message",
				Pointer:         "/error/message",
				Type:            diff.DiffTypeValue,
				Severity:        diff.SeverityFail,
				ExpectedPresent: true,
				ActualPresent:   true,
				Expected:        baselineErr.Message,
				Actual:          targetErr.Message,
				Message:         "error messages differ",
			})
		}
		if !sameErrorData(baselineErr.Data, targetErr.Data) {
			diffs = append(diffs, diff.Difference{
				Path:            "error.data",
				Pointer:         "/error/data",
				Type:            diff.DiffTypeValue,
				Severity:        diff.SeverityFail,
				ExpectedPresent: len(baselineErr.Data) != 0,
				ActualPresent:   len(targetErr.Data) != 0,
				Expected:        baselineErr.Data,
				Actual:          targetErr.Data,
				Message:         "error data differs",
			})
		}
		if len(diffs) == 0 {
			return StatusPass, nil, ""
		}
		return StatusFail, diffs, ""
	}

	return StatusFail, diffs, ""
}

func sameErrorData(baseline, target json.RawMessage) bool {
	if len(baseline) == 0 || len(target) == 0 {
		return len(baseline) == len(target)
	}
	compared, err := diff.Compare(baseline, target, diff.DefaultOptions())
	return err == nil && len(compared.Differences) == 0
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
	case StatusInconclusive:
		statusStr = red("? INCONCLUSIVE")
	case StatusNotApplicable:
		statusStr = yellow("- NOT_APPLICABLE")
	default:
		statusStr = red("✗ FAIL")
	}

	fmt.Printf("%s %s [%s]\n", statusStr, cyan(result.TestCase.Method), result.TestCase.Name)

	if r.verbose {
		fmt.Printf("  %s: %v, %s: %v\n", r.baseline.Name, result.BaselineDuration, r.target.Name, result.TargetDuration)
	}

	if result.Status == StatusCompatible {
		if result.SkipReason != "" {
			fmt.Printf("  %s Known difference: %s\n", blue("→"), result.SkipReason)
		} else {
			// Both clients reported an unsupported method.
			fmt.Printf("  %s Both endpoints report an unsupported method\n", blue("→"))
		}
	}

	if result.Status == StatusWarning {
		if result.SkipReason != "" {
			fmt.Printf("  %s %s\n", yellow("→"), result.SkipReason)
		} else {
			fmt.Printf("  %s Found %d nonfatal field differences:\n", yellow("→"), len(result.Differences))
		}
		if len(result.Differences) > 0 {
			r.printDifferences(result.Differences, 5)
		}
	}
	if result.Status == StatusInconclusive || result.Status == StatusNotApplicable {
		fmt.Printf("  %s\n", result.SkipReason)
	}

	if result.Status == StatusFail {
		if result.BaselineError != "" && result.TargetError == "" {
			fmt.Printf("  %s %s error: %s\n", yellow("→"), r.baseline.Name, result.BaselineError)
		}
		if result.TargetError != "" && result.BaselineError == "" {
			fmt.Printf("  %s %s error: %s\n", yellow("→"), r.target.Name, result.TargetError)
		}
		if result.BaselineError != "" && result.TargetError != "" {
			fmt.Printf("  %s Endpoint errors are incompatible:\n", yellow("→"))
			fmt.Printf("    %s: %s\n", r.baseline.Name, truncateValue(result.BaselineError, 60))
			fmt.Printf("    %s: %s\n", r.target.Name, truncateValue(result.TargetError, 60))
		}
		if result.CompareError != "" {
			fmt.Printf("  %s Comparison error: %s\n", yellow("→"), result.CompareError)
		}
		if len(result.Differences) > 0 {
			fmt.Printf("  %s Found %d differences:\n", yellow("→"), len(result.Differences))
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
		fmt.Printf("    ... and %d more differences\n", len(diffs)-limit)
	}
}

// Generate builds the final report.
func (r *Reporter) Generate() *Report {
	report := &Report{
		SchemaVersion: reportSchemaVersion,
		PolicyMode:    r.policyMode,
		SelectedSuite: r.selectedSuite,
		ExcludedTests: len(r.excludedCases),
		ExcludedCases: append([]ExcludedCase(nil), r.excludedCases...),
		Timestamp:     time.Now(),
		Baseline:      r.baseline,
		Target:        r.target,
		TotalTests:    len(r.results),
		Results:       append([]TestResult(nil), r.results...),
	}
	if r.policyRegistry != nil {
		report.RegistryID = r.policyRegistry.RegistryID
		report.RegistryDigest = r.policyRegistry.Digest()
		for _, rule := range r.policyRegistry.Rules {
			if r.matchedRuleIDs[rule.ID] {
				continue
			}
			for _, context := range r.executedContexts {
				if r.policyRegistry.Applicable(&rule, context) {
					report.StaleRuleIDs = append(report.StaleRuleIDs, rule.ID)
					break
				}
			}
		}
		sort.Strings(report.StaleRuleIDs)
	}

	for i := range report.Results {
		result := &report.Results[i]
		if result.ObservedStatus == "" {
			result.ObservedStatus = result.Status
		}
		result.EffectiveStatus = result.Status
		switch result.Status {
		case StatusPass:
			report.PassedTests++
		case StatusCompatible:
			report.CompatibleTests++
		case StatusWarning:
			report.WarningTests++
		case StatusFail:
			report.FailedTests++
		case StatusInconclusive:
			report.InconclusiveTests++
		case StatusNotApplicable:
			report.NotApplicableTests++
		}
	}

	report.Summary = fmt.Sprintf("Total %d: %d passed, %d compatible, %d warnings, %d failed, %d inconclusive, %d not applicable, %d excluded",
		report.TotalTests, report.PassedTests, report.CompatibleTests,
		report.WarningTests, report.FailedTests, report.InconclusiveTests, report.NotApplicableTests, report.ExcludedTests)

	return report
}

// PrintSummary renders the run summary.
func (r *Reporter) PrintSummary() {
	report := r.Generate()

	fmt.Println()
	fmt.Println(strings.Repeat("=", 60))
	fmt.Println("Test summary")
	fmt.Println(strings.Repeat("=", 60))

	green := color.New(color.FgGreen).SprintFunc()
	red := color.New(color.FgRed).SprintFunc()
	cyan := color.New(color.FgCyan).SprintFunc()
	yellow := color.New(color.FgYellow).SprintFunc()
	blue := color.New(color.FgBlue).SprintFunc()

	fmt.Printf("%s: %s\n", report.Baseline.Name, cyan(report.Baseline.URL))
	fmt.Printf("%s: %s\n", report.Target.Name, cyan(report.Target.URL))
	fmt.Printf("Duration: %v\n", time.Since(r.startTime))
	if report.SelectedSuite != "" {
		fmt.Printf("Selected suite: %s | Excluded cases: %d\n", report.SelectedSuite, report.ExcludedTests)
	}
	fmt.Println()

	methodCounts := make(map[string]int)
	for _, result := range report.Results {
		methodCounts[result.TestCase.Method]++
	}
	uniqueMethods := len(methodCounts)

	fmt.Printf("Method coverage: %s methods\n", cyan(uniqueMethods))

	var multiCaseMethods []string
	for method, count := range methodCounts {
		if count > 1 {
			multiCaseMethods = append(multiCaseMethods, fmt.Sprintf("%s: %d", method, count))
		}
	}
	if len(multiCaseMethods) > 0 {
		// Sort methods for stable output.
		sort.Strings(multiCaseMethods)
		fmt.Println("Methods with multiple cases:")
		for _, m := range multiCaseMethods {
			fmt.Printf("  %s\n", m)
		}
	}
	fmt.Println()

	fmt.Printf("Total: %d | Passed: %s | Compatible: %s | Warnings: %s | Failed: %s | Inconclusive: %s | Not applicable: %s\n",
		report.TotalTests,
		green(report.PassedTests),
		blue(report.CompatibleTests),
		yellow(report.WarningTests),
		red(report.FailedTests),
		red(report.InconclusiveTests),
		yellow(report.NotApplicableTests))

	if report.CompatibleTests > 0 {
		fmt.Println()
		fmt.Println(blue("Compatible tests:"))
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
		fmt.Println(yellow("Tests with warnings:"))
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
		fmt.Println(red("Failed tests:"))
		for _, result := range report.Results {
			if result.Status == StatusFail {
				fmt.Printf("  ✗ %s [%s]\n", result.TestCase.Method, result.TestCase.Name)
			}
		}
	}
	if report.InconclusiveTests > 0 {
		reasons := make(map[string]int)
		for _, result := range report.Results {
			if result.Status == StatusInconclusive {
				reasons[result.SkipReason]++
			}
		}
		labels := make([]string, 0, len(reasons))
		for reason := range reasons {
			labels = append(labels, reason)
		}
		sort.Strings(labels)
		fmt.Println()
		fmt.Println(red("Inconclusive cases:"))
		for _, reason := range labels {
			fmt.Printf("  %d: %s\n", reasons[reason], reason)
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

// HasFailures reports failures, inconclusive results, or a run with no comparable cases.
func (r *Reporter) HasFailures() bool {
	for _, result := range r.results {
		if result.Status == StatusFail || result.Status == StatusInconclusive {
			return true
		}
	}
	for _, result := range r.results {
		if result.Status == StatusPass || result.Status == StatusWarning || result.Status == StatusCompatible {
			return false
		}
	}
	return true
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
