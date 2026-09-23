// Package diff compares nested JSON values and classifies differences.
package diff

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
)

// DiffType identifies the kind of difference.
type DiffType string

const (
	DiffTypeValue   DiffType = "value"   // value mismatch
	DiffTypeType    DiffType = "type"    // type mismatch
	DiffTypeMissing DiffType = "missing" // field missing on the target
	DiffTypeExtra   DiffType = "extra"   // field added by the target
	DiffTypeError   DiffType = "error"   // error mismatch
	DiffTypeOrder   DiffType = "order"   // array order mismatch
)

// DiffSeverity classifies the impact of a difference.
type DiffSeverity string

const (
	SeverityFail    DiffSeverity = "fail"    // failing difference
	SeverityWarning DiffSeverity = "warning" // nonfatal difference such as an extra field
	SeverityInfo    DiffSeverity = "info"    // informational difference
)

// Difference describes one mismatch between responses.
type Difference struct {
	Path     string       `json:"path"`               // JSON path, such as ".result.blockNumber"
	Type     DiffType     `json:"type"`               // difference type
	Severity DiffSeverity `json:"severity"`           // difference severity
	Expected interface{}  `json:"expected,omitempty"` // baseline value
	Actual   interface{}  `json:"actual,omitempty"`   // target value
	Message  string       `json:"message,omitempty"`  // details
}

// CompareResult collects the differences from a comparison.
type CompareResult struct {
	IsEqual       bool         `json:"is_equal"`
	Differences   []Difference `json:"differences,omitempty"`
	TotalFields   int          `json:"total_fields"`
	MatchedFields int          `json:"matched_fields"`
	DiffFields    int          `json:"diff_fields"`
	FailCount     int          `json:"fail_count"`
	WarningCount  int          `json:"warning_count"`
	InfoCount     int          `json:"info_count"`
}

// HasFails reports whether any difference has failure severity.
func (r *CompareResult) HasFails() bool {
	return r.FailCount > 0
}

// HasWarnings reports whether any difference has warning severity.
func (r *CompareResult) HasWarnings() bool {
	return r.WarningCount > 0
}

// Options controls response comparison.
type Options struct {
	IgnorePaths     []string // JSON paths excluded from comparison
	IgnoreOrder     bool     // ignore array ordering
	NormalizeHex    bool     // normalize hexadecimal values
	IgnoreErrorData bool     // ignore error.data differences
}

// DefaultOptions returns the default comparison settings.
func DefaultOptions() *Options {
	return &Options{
		IgnorePaths:     []string{},
		IgnoreOrder:     false,
		NormalizeHex:    true,
		IgnoreErrorData: false,
	}
}

// Compare recursively compares two JSON values.
func Compare(expected, actual []byte, opts *Options) (*CompareResult, error) {
	if opts == nil {
		opts = DefaultOptions()
	}

	var expectedVal, actualVal interface{}

	if err := json.Unmarshal(expected, &expectedVal); err != nil {
		return nil, fmt.Errorf("解析 expected JSON 失败: %w", err)
	}

	if err := json.Unmarshal(actual, &actualVal); err != nil {
		return nil, fmt.Errorf("解析 actual JSON 失败: %w", err)
	}

	result := &CompareResult{
		IsEqual:     true,
		Differences: []Difference{},
	}

	compareValues("", expectedVal, actualVal, result, opts)

	for i := range result.Differences {
		result.Differences[i].Severity = classifyDiffSeverity(&result.Differences[i])
		switch result.Differences[i].Severity {
		case SeverityFail:
			result.FailCount++
		case SeverityWarning:
			result.WarningCount++
		case SeverityInfo:
			result.InfoCount++
		}
	}

	result.DiffFields = len(result.Differences)
	result.MatchedFields = result.TotalFields - result.DiffFields
	// Warnings do not make the responses unequal.
	result.IsEqual = result.FailCount == 0

	return result, nil
}

// classifyDiffSeverity classifies a difference by type.
func classifyDiffSeverity(d *Difference) DiffSeverity {
	// Extra fields on either side are warnings.
	if d.Type == DiffTypeExtra || d.Type == DiffTypeMissing {
		return SeverityWarning
	}

	return SeverityFail
}

// compareValues recursively compares values at a JSON path.
func compareValues(path string, expected, actual interface{}, result *CompareResult, opts *Options) {
	for _, ignorePath := range opts.IgnorePaths {
		if strings.HasPrefix(path, ignorePath) || path == ignorePath {
			return
		}
	}

	result.TotalFields++

	if expected == nil && actual == nil {
		return
	}
	if expected == nil {
		result.Differences = append(result.Differences, Difference{
			Path:    path,
			Type:    DiffTypeExtra,
			Actual:  actual,
			Message: "baseline returned null; target returned a value",
		})
		return
	}
	if actual == nil {
		result.Differences = append(result.Differences, Difference{
			Path:     path,
			Type:     DiffTypeMissing,
			Expected: expected,
			Message:  "baseline returned a value; target returned null",
		})
		return
	}

	expectedType := reflect.TypeOf(expected)
	actualType := reflect.TypeOf(actual)

	if expectedType != actualType {
		// JSON decoders may represent the same number as json.Number or float64.
		if isNumericType(expected) && isNumericType(actual) {
			if !compareNumeric(expected, actual, opts) {
				result.Differences = append(result.Differences, Difference{
					Path:     path,
					Type:     DiffTypeValue,
					Expected: expected,
					Actual:   actual,
					Message:  "数值不同",
				})
			}
			return
		}

		result.Differences = append(result.Differences, Difference{
			Path:     path,
			Type:     DiffTypeType,
			Expected: fmt.Sprintf("%T", expected),
			Actual:   fmt.Sprintf("%T", actual),
			Message:  fmt.Sprintf("类型不同: %T vs %T", expected, actual),
		})
		return
	}

	switch exp := expected.(type) {
	case map[string]interface{}:
		act := actual.(map[string]interface{})
		compareObjects(path, exp, act, result, opts)

	case []interface{}:
		act := actual.([]interface{})
		compareArrays(path, exp, act, result, opts)

	case string:
		act := actual.(string)
		if opts.NormalizeHex {
			exp = normalizeHex(exp)
			act = normalizeHex(act)
		}
		if exp != act {
			result.Differences = append(result.Differences, Difference{
				Path:     path,
				Type:     DiffTypeValue,
				Expected: expected,
				Actual:   actual,
				Message:  "字符串值不同",
			})
		}

	case float64:
		act := actual.(float64)
		if exp != act {
			result.Differences = append(result.Differences, Difference{
				Path:     path,
				Type:     DiffTypeValue,
				Expected: expected,
				Actual:   actual,
				Message:  "数值不同",
			})
		}

	case bool:
		act := actual.(bool)
		if exp != act {
			result.Differences = append(result.Differences, Difference{
				Path:     path,
				Type:     DiffTypeValue,
				Expected: expected,
				Actual:   actual,
				Message:  "布尔值不同",
			})
		}

	default:
		if !reflect.DeepEqual(expected, actual) {
			result.Differences = append(result.Differences, Difference{
				Path:     path,
				Type:     DiffTypeValue,
				Expected: expected,
				Actual:   actual,
				Message:  "值不同",
			})
		}
	}
}

// compareObjects compares JSON objects.
func compareObjects(path string, expected, actual map[string]interface{}, result *CompareResult, opts *Options) {
	allKeys := make(map[string]bool)
	for k := range expected {
		allKeys[k] = true
	}
	for k := range actual {
		allKeys[k] = true
	}

	// Sort keys for stable difference ordering.
	keys := make([]string, 0, len(allKeys))
	for k := range allKeys {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, key := range keys {
		childPath := path + "." + key
		if path == "" {
			childPath = key
		}

		expVal, expExists := expected[key]
		actVal, actExists := actual[key]

		if expExists && !actExists {
			result.TotalFields++
			result.Differences = append(result.Differences, Difference{
				Path:     childPath,
				Type:     DiffTypeMissing,
				Expected: expVal,
				Message:  "target response is missing this field",
			})
			continue
		}

		if !expExists && actExists {
			result.TotalFields++
			result.Differences = append(result.Differences, Difference{
				Path:    childPath,
				Type:    DiffTypeExtra,
				Actual:  actVal,
				Message: "target response has an extra field",
			})
			continue
		}

		compareValues(childPath, expVal, actVal, result, opts)
	}
}

// compareArrays compares JSON arrays.
func compareArrays(path string, expected, actual []interface{}, result *CompareResult, opts *Options) {
	if len(expected) != len(actual) {
		result.Differences = append(result.Differences, Difference{
			Path:     path,
			Type:     DiffTypeValue,
			Expected: len(expected),
			Actual:   len(actual),
			Message:  fmt.Sprintf("数组长度不同: %d vs %d", len(expected), len(actual)),
		})
	}

	maxLen := len(expected)
	if len(actual) > maxLen {
		maxLen = len(actual)
	}

	for i := 0; i < maxLen; i++ {
		childPath := fmt.Sprintf("%s[%d]", path, i)

		if i >= len(expected) {
			result.TotalFields++
			result.Differences = append(result.Differences, Difference{
				Path:    childPath,
				Type:    DiffTypeExtra,
				Actual:  actual[i],
				Message: "target response has extra array elements",
			})
			continue
		}

		if i >= len(actual) {
			result.TotalFields++
			result.Differences = append(result.Differences, Difference{
				Path:     childPath,
				Type:     DiffTypeMissing,
				Expected: expected[i],
				Message:  "target response is missing array elements",
			})
			continue
		}

		compareValues(childPath, expected[i], actual[i], result, opts)
	}
}

// normalizeHex normalizes a hexadecimal string.
func normalizeHex(s string) string {
	if !strings.HasPrefix(s, "0x") && !strings.HasPrefix(s, "0X") {
		return s
	}

	s = strings.ToLower(s)

	// Remove leading zeroes from numeric hex while retaining one digit.
	if len(s) > 2 {
		trimmed := strings.TrimLeft(s[2:], "0")
		if trimmed == "" {
			return "0x0"
		}
		return "0x" + trimmed
	}
	return s
}

// isNumericType reports whether a value can be compared numerically.
func isNumericType(v interface{}) bool {
	switch v.(type) {
	case float64, float32, int, int64, int32, int16, int8,
		uint, uint64, uint32, uint16, uint8, json.Number:
		return true
	}
	return false
}

// compareNumeric compares two numeric representations.
func compareNumeric(expected, actual interface{}, opts *Options) bool {
	expFloat := toFloat64(expected)
	actFloat := toFloat64(actual)
	return expFloat == actFloat
}

// toFloat64 converts a numeric value to float64.
func toFloat64(v interface{}) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case float32:
		return float64(n)
	case int:
		return float64(n)
	case int64:
		return float64(n)
	case int32:
		return float64(n)
	case int16:
		return float64(n)
	case int8:
		return float64(n)
	case uint:
		return float64(n)
	case uint64:
		return float64(n)
	case uint32:
		return float64(n)
	case uint16:
		return float64(n)
	case uint8:
		return float64(n)
	case json.Number:
		f, _ := n.Float64()
		return f
	}
	return 0
}

// FormatDifferences formats differences for human-readable output.
func FormatDifferences(diffs []Difference) string {
	if len(diffs) == 0 {
		return "无差异"
	}

	var sb strings.Builder
	for i, d := range diffs {
		sb.WriteString(fmt.Sprintf("%d. [%s] %s\n", i+1, d.Type, d.Path))
		if d.Expected != nil {
			sb.WriteString(fmt.Sprintf("   baseline: %v\n", formatValue(d.Expected)))
		}
		if d.Actual != nil {
			sb.WriteString(fmt.Sprintf("   target: %v\n", formatValue(d.Actual)))
		}
		if d.Message != "" {
			sb.WriteString(fmt.Sprintf("   说明: %s\n", d.Message))
		}
	}
	return sb.String()
}

// formatValue formats a response value for display.
func formatValue(v interface{}) string {
	switch val := v.(type) {
	case string:
		if len(val) > 100 {
			return val[:100] + "..."
		}
		return val
	case []byte:
		s := string(val)
		if len(s) > 100 {
			return s[:100] + "..."
		}
		return s
	default:
		data, err := json.Marshal(v)
		if err != nil {
			return fmt.Sprintf("%v", v)
		}
		s := string(data)
		if len(s) > 100 {
			return s[:100] + "..."
		}
		return s
	}
}

// ErrorCompatibility classifies compatibility between JSON-RPC errors.
type ErrorCompatibility string

const (
	ErrorCompatNone           ErrorCompatibility = "none"             // incompatible errors
	ErrorCompatMethodNotFound ErrorCompatibility = "method_not_found" // unsupported method on both sides
	ErrorCompatIdentical      ErrorCompatibility = "identical"        // identical errors
)

// methodNotFoundKeywords identifies method-not-found errors from message text.
var methodNotFoundKeywords = []string{
	"not exist",
	"not available",
	"unimplemented",
	"method not found",
	"does not exist",
}

// CheckErrorCompatibility classifies whether two JSON-RPC errors are compatible.
func CheckErrorCompatibility(baselineError, targetError map[string]interface{}) ErrorCompatibility {
	if baselineError == nil || targetError == nil {
		return ErrorCompatNone
	}

	baselineMsg := getErrorMessage(baselineError)
	targetMsg := getErrorMessage(targetError)

	if baselineMsg == targetMsg {
		return ErrorCompatIdentical
	}

	// Both clients may report an unsupported method with different wording.
	baselineIsMethodNotFound := isMethodNotFoundError(baselineMsg)
	targetIsMethodNotFound := isMethodNotFoundError(targetMsg)

	if baselineIsMethodNotFound && targetIsMethodNotFound {
		return ErrorCompatMethodNotFound
	}

	return ErrorCompatNone
}

// getErrorMessage extracts the message from an error object.
func getErrorMessage(errObj map[string]interface{}) string {
	if msg, ok := errObj["message"]; ok {
		if s, ok := msg.(string); ok {
			return strings.ToLower(s)
		}
	}
	return ""
}

// isMethodNotFoundError detects unsupported-method wording.
func isMethodNotFoundError(msg string) bool {
	msg = strings.ToLower(msg)
	for _, keyword := range methodNotFoundKeywords {
		if strings.Contains(msg, keyword) {
			return true
		}
	}
	return false
}
