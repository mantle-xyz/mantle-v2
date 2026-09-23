// Package diff 提供深度 JSON 对比功能
package diff

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
)

// DiffType 差异类型
type DiffType string

const (
	DiffTypeValue    DiffType = "value"     // 值不同
	DiffTypeType     DiffType = "type"      // 类型不同
	DiffTypeMissing  DiffType = "missing"   // 字段缺失（reth 缺少）
	DiffTypeExtra    DiffType = "extra"     // 多余字段（reth 多出）
	DiffTypeError    DiffType = "error"     // 错误差异
	DiffTypeOrder    DiffType = "order"     // 顺序不同（数组）
)

// DiffSeverity 差异严重性
type DiffSeverity string

const (
	SeverityFail    DiffSeverity = "fail"    // 严重差异，需要修复
	SeverityWarning DiffSeverity = "warning" // 警告，额外字段等
	SeverityInfo    DiffSeverity = "info"    // 信息，可忽略
)

// Difference 单个差异
type Difference struct {
	Path       string       `json:"path"`                  // JSON 路径，如 ".result.blockNumber"
	Type       DiffType     `json:"type"`                  // 差异类型
	Severity   DiffSeverity `json:"severity"`              // 严重性
	Expected   interface{}  `json:"expected,omitempty"`    // Primary (geth) 的值
	Actual     interface{}  `json:"actual,omitempty"`      // Secondary (reth) 的值
	Message    string       `json:"message,omitempty"`     // 详细说明
}

// CompareResult 对比结果
type CompareResult struct {
	IsEqual     bool          `json:"is_equal"`
	Differences []Difference  `json:"differences,omitempty"`
	// 统计信息
	TotalFields    int `json:"total_fields"`
	MatchedFields  int `json:"matched_fields"`
	DiffFields     int `json:"diff_fields"`
	// 按严重性分类
	FailCount    int `json:"fail_count"`
	WarningCount int `json:"warning_count"`
	InfoCount    int `json:"info_count"`
}

// HasFails 是否有严重差异
func (r *CompareResult) HasFails() bool {
	return r.FailCount > 0
}

// HasWarnings 是否有警告
func (r *CompareResult) HasWarnings() bool {
	return r.WarningCount > 0
}

// Options 对比选项
type Options struct {
	IgnorePaths      []string // 忽略的路径
	IgnoreOrder      bool     // 是否忽略数组顺序
	NormalizeHex     bool     // 规范化十六进制格式
	IgnoreErrorData  bool     // 是否忽略 error.data 差异
}

// DefaultOptions 默认选项
func DefaultOptions() *Options {
	return &Options{
		IgnorePaths:     []string{},
		IgnoreOrder:     false,
		NormalizeHex:    true,
		IgnoreErrorData: false,
	}
}

// Compare 深度对比两个 JSON 数据
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
	
	// 为每个差异分配严重性并统计
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
	// 只有当没有 FAIL 级别差异时才算相等
	result.IsEqual = result.FailCount == 0

	return result, nil
}

// classifyDiffSeverity 根据差异类型分类严重性
func classifyDiffSeverity(d *Difference) DiffSeverity {
	// 额外字段（reth 多出或 geth 多出）视为警告
	if d.Type == DiffTypeExtra || d.Type == DiffTypeMissing {
		return SeverityWarning
	}
	
	// 其他差异默认为失败
	return SeverityFail
}

// compareValues 递归对比值
func compareValues(path string, expected, actual interface{}, result *CompareResult, opts *Options) {
	// 检查是否应忽略此路径
	for _, ignorePath := range opts.IgnorePaths {
		if strings.HasPrefix(path, ignorePath) || path == ignorePath {
			return
		}
	}

	result.TotalFields++

	// 处理 nil 情况
	if expected == nil && actual == nil {
		return
	}
	if expected == nil {
		result.Differences = append(result.Differences, Difference{
			Path:    path,
			Type:    DiffTypeExtra,
			Actual:  actual,
			Message: "geth 返回 null，reth 返回非 null",
		})
		return
	}
	if actual == nil {
		result.Differences = append(result.Differences, Difference{
			Path:     path,
			Type:     DiffTypeMissing,
			Expected: expected,
			Message:  "geth 返回非 null，reth 返回 null",
		})
		return
	}

	expectedType := reflect.TypeOf(expected)
	actualType := reflect.TypeOf(actual)

	// 类型检查
	if expectedType != actualType {
		// 特殊处理：数字类型可能不同（json.Number vs float64）
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

// compareObjects 对比对象
func compareObjects(path string, expected, actual map[string]interface{}, result *CompareResult, opts *Options) {
	// 收集所有键
	allKeys := make(map[string]bool)
	for k := range expected {
		allKeys[k] = true
	}
	for k := range actual {
		allKeys[k] = true
	}

	// 排序键以保证一致的输出顺序
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
				Message:  "reth 响应中缺少此字段",
			})
			continue
		}

		if !expExists && actExists {
			result.TotalFields++
			result.Differences = append(result.Differences, Difference{
				Path:    childPath,
				Type:    DiffTypeExtra,
				Actual:  actVal,
				Message: "reth 响应中有多余字段",
			})
			continue
		}

		compareValues(childPath, expVal, actVal, result, opts)
	}
}

// compareArrays 对比数组
func compareArrays(path string, expected, actual []interface{}, result *CompareResult, opts *Options) {
	if len(expected) != len(actual) {
		result.Differences = append(result.Differences, Difference{
			Path:     path,
			Type:     DiffTypeValue,
			Expected: len(expected),
			Actual:   len(actual),
			Message:  fmt.Sprintf("数组长度不同: %d vs %d", len(expected), len(actual)),
		})
		// 继续比较共同元素
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
				Message: "reth 响应中有多余的数组元素",
			})
			continue
		}

		if i >= len(actual) {
			result.TotalFields++
			result.Differences = append(result.Differences, Difference{
				Path:     childPath,
				Type:     DiffTypeMissing,
				Expected: expected[i],
				Message:  "reth 响应中缺少数组元素",
			})
			continue
		}

		compareValues(childPath, expected[i], actual[i], result, opts)
	}
}

// normalizeHex 规范化十六进制字符串
func normalizeHex(s string) string {
	if !strings.HasPrefix(s, "0x") && !strings.HasPrefix(s, "0X") {
		return s
	}

	// 转小写
	s = strings.ToLower(s)

	// 对于十六进制数字，去除前导零（但保留至少一位）
	if len(s) > 2 {
		trimmed := strings.TrimLeft(s[2:], "0")
		if trimmed == "" {
			return "0x0"
		}
		return "0x" + trimmed
	}
	return s
}

// isNumericType 检查是否为数字类型
func isNumericType(v interface{}) bool {
	switch v.(type) {
	case float64, float32, int, int64, int32, int16, int8,
		uint, uint64, uint32, uint16, uint8, json.Number:
		return true
	}
	return false
}

// compareNumeric 比较数字值
func compareNumeric(expected, actual interface{}, opts *Options) bool {
	expFloat := toFloat64(expected)
	actFloat := toFloat64(actual)
	return expFloat == actFloat
}

// toFloat64 转换为 float64
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

// FormatDifferences 格式化差异列表为可读字符串
func FormatDifferences(diffs []Difference) string {
	if len(diffs) == 0 {
		return "无差异"
	}

	var sb strings.Builder
	for i, d := range diffs {
		sb.WriteString(fmt.Sprintf("%d. [%s] %s\n", i+1, d.Type, d.Path))
		if d.Expected != nil {
			sb.WriteString(fmt.Sprintf("   geth: %v\n", formatValue(d.Expected)))
		}
		if d.Actual != nil {
			sb.WriteString(fmt.Sprintf("   reth: %v\n", formatValue(d.Actual)))
		}
		if d.Message != "" {
			sb.WriteString(fmt.Sprintf("   说明: %s\n", d.Message))
		}
	}
	return sb.String()
}

// formatValue 格式化值用于显示
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

// ErrorCompatibility 错误兼容性类型
type ErrorCompatibility string

const (
	ErrorCompatNone        ErrorCompatibility = "none"        // 不兼容
	ErrorCompatMethodNotFound ErrorCompatibility = "method_not_found" // 方法不存在/未实现
	ErrorCompatIdentical   ErrorCompatibility = "identical"   // 完全相同
)

// methodNotFoundKeywords 方法不存在的关键词
var methodNotFoundKeywords = []string{
	"not exist",
	"not available",
	"unimplemented",
	"method not found",
	"does not exist",
}

// CheckErrorCompatibility 检查两个错误是否兼容
// 返回兼容性类型
func CheckErrorCompatibility(gethError, rethError map[string]interface{}) ErrorCompatibility {
	if gethError == nil || rethError == nil {
		return ErrorCompatNone
	}

	gethMsg := getErrorMessage(gethError)
	rethMsg := getErrorMessage(rethError)

	// 完全相同
	if gethMsg == rethMsg {
		return ErrorCompatIdentical
	}

	// 检查是否都是"方法不存在/未实现"类型的错误
	gethIsMethodNotFound := isMethodNotFoundError(gethMsg)
	rethIsMethodNotFound := isMethodNotFoundError(rethMsg)

	if gethIsMethodNotFound && rethIsMethodNotFound {
		return ErrorCompatMethodNotFound
	}

	return ErrorCompatNone
}

// getErrorMessage 从错误对象中提取消息
func getErrorMessage(errObj map[string]interface{}) string {
	if msg, ok := errObj["message"]; ok {
		if s, ok := msg.(string); ok {
			return strings.ToLower(s)
		}
	}
	return ""
}

// isMethodNotFoundError 检查错误消息是否表示方法不存在
func isMethodNotFoundError(msg string) bool {
	msg = strings.ToLower(msg)
	for _, keyword := range methodNotFoundKeywords {
		if strings.Contains(msg, keyword) {
			return true
		}
	}
	return false
}

