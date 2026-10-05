// Package policy matches reviewed, directional RPC differences.
package policy

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"reflect"
	"regexp"
	"strings"

	"github.com/ethereum-optimism/optimism/op-rpc-compat/pkg/diff"
)

type ClientBuild struct {
	ID            string `json:"id"`
	ClientVersion string `json:"client_version"`
}

type ClientSet struct {
	ID     string        `json:"id"`
	Builds []ClientBuild `json:"builds"`
}

type ReviewedPair struct {
	BaselineBuild string `json:"baseline_build"`
	TargetBuild   string `json:"target_build"`
	EvidenceURL   string `json:"evidence_url"`
}

type ValueSpec struct {
	Present bool            `json:"present"`
	Type    string          `json:"type,omitempty"`
	Value   json.RawMessage `json:"value,omitempty"`
}

type Rule struct {
	ID            string         `json:"id"`
	BaselineSet   string         `json:"baseline_set"`
	TargetSet     string         `json:"target_set"`
	Pairs         []ReviewedPair `json:"reviewed_pairs"`
	ChainID       string         `json:"chain_id"`
	GenesisHash   string         `json:"genesis_hash"`
	CorpusID      string         `json:"corpus_id"`
	CaseID        string         `json:"case_id"`
	Method        string         `json:"method"`
	RequestSHA256 string         `json:"request_sha256"`
	Pointer       string         `json:"pointer"`
	DiffType      diff.DiffType  `json:"diff_type"`
	Baseline      ValueSpec      `json:"baseline"`
	Target        ValueSpec      `json:"target"`
	Action        string         `json:"action"`
	Reason        string         `json:"reason"`
	EvidenceURL   string         `json:"evidence_url"`
}

type Registry struct {
	SchemaVersion int         `json:"schema_version"`
	RegistryID    string      `json:"registry_id"`
	ClientSets    []ClientSet `json:"client_sets"`
	Rules         []Rule      `json:"rules"`
}

type Endpoint struct {
	BuildID       string
	ClientVersion string
}

type MatchContext struct {
	Baseline      Endpoint
	Target        Endpoint
	ChainID       string
	GenesisHash   string
	CorpusID      string
	CaseID        string
	Method        string
	RequestSHA256 string
}

var buildSHA = regexp.MustCompile(`(?:\+|-)([0-9a-f]{8,40})(?:/|$)`)

// BuildIDFromClientVersion recognizes a build SHA only when the RPC version includes one.
func BuildIDFromClientVersion(version string) string {
	match := buildSHA.FindStringSubmatch(version)
	if len(match) != 2 {
		return ""
	}
	return "git:" + match[1]
}

// RequestDigest binds a rule to the method and complete request parameters.
func RequestDigest(method string, params any) string {
	data, err := json.Marshal(struct {
		Method string `json:"method"`
		Params any    `json:"params"`
	}{Method: method, Params: params})
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func (r *Registry) Digest() string {
	data, err := json.Marshal(r)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func Load(filesystem fs.FS, path string) (*Registry, error) {
	file, err := filesystem.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	var registry Registry
	if err := decoder.Decode(&registry); err != nil {
		return nil, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, fmt.Errorf("registry has trailing JSON: %v", err)
	}
	if err := registry.Validate(); err != nil {
		return nil, err
	}
	return &registry, nil
}

func (r *Registry) Validate() error {
	if r.SchemaVersion != 1 || r.RegistryID == "" {
		return fmt.Errorf("invalid registry header")
	}
	sets := make(map[string]map[string]ClientBuild)
	for _, set := range r.ClientSets {
		if set.ID == "" || len(set.Builds) == 0 || sets[set.ID] != nil {
			return fmt.Errorf("invalid or duplicate client set %q", set.ID)
		}
		builds := make(map[string]ClientBuild)
		for _, build := range set.Builds {
			if build.ID == "" || build.ClientVersion == "" || builds[build.ID].ID != "" {
				return fmt.Errorf("invalid or duplicate build in set %q", set.ID)
			}
			if strings.HasPrefix(build.ID, "git:") && BuildIDFromClientVersion(build.ClientVersion) != build.ID {
				return fmt.Errorf("build %q does not match client version", build.ID)
			}
			builds[build.ID] = build
		}
		sets[set.ID] = builds
	}
	ruleIDs := make(map[string]bool)
	scopes := make(map[string]bool)
	for _, rule := range r.Rules {
		if rule.ID == "" || ruleIDs[rule.ID] {
			return fmt.Errorf("invalid or duplicate rule ID %q", rule.ID)
		}
		ruleIDs[rule.ID] = true
		if sets[rule.BaselineSet] == nil || sets[rule.TargetSet] == nil || len(rule.Pairs) == 0 {
			return fmt.Errorf("rule %q has unknown client sets or no reviewed pairs", rule.ID)
		}
		if rule.ChainID == "" || rule.GenesisHash == "" || rule.CorpusID == "" || rule.CaseID == "" || rule.Method == "" ||
			len(rule.RequestSHA256) != 64 || rule.Action != "warning" || rule.Reason == "" || rule.EvidenceURL == "" ||
			!validPointer(rule.Pointer) || !validDiffType(rule.DiffType) {
			return fmt.Errorf("rule %q has incomplete or invalid scope", rule.ID)
		}
		if _, err := hex.DecodeString(rule.RequestSHA256); err != nil {
			return fmt.Errorf("rule %q has invalid request digest: %w", rule.ID, err)
		}
		if err := rule.Baseline.validate(); err != nil {
			return fmt.Errorf("rule %q baseline: %w", rule.ID, err)
		}
		if err := rule.Target.validate(); err != nil {
			return fmt.Errorf("rule %q target: %w", rule.ID, err)
		}
		for _, pair := range rule.Pairs {
			if sets[rule.BaselineSet][pair.BaselineBuild].ID == "" ||
				sets[rule.TargetSet][pair.TargetBuild].ID == "" || pair.EvidenceURL == "" {
				return fmt.Errorf("rule %q has an unreviewed build pair", rule.ID)
			}
			scope := fmt.Sprintf("%s|%s|%s|%s|%s|%s|%s|%s|%s", rule.ChainID, rule.GenesisHash,
				rule.CorpusID, rule.CaseID, rule.Method, rule.RequestSHA256, rule.Pointer, pair.BaselineBuild, pair.TargetBuild)
			if scopes[scope] {
				return fmt.Errorf("rule %q overlaps another rule", rule.ID)
			}
			scopes[scope] = true
		}
	}
	return nil
}

func validPointer(pointer string) bool {
	if !strings.HasPrefix(pointer, "/") {
		return false
	}
	for i := 0; i < len(pointer); i++ {
		if pointer[i] == '~' {
			if i+1 >= len(pointer) || (pointer[i+1] != '0' && pointer[i+1] != '1') {
				return false
			}
			i++
		}
	}
	return true
}

func validDiffType(kind diff.DiffType) bool {
	switch kind {
	case diff.DiffTypeValue, diff.DiffTypeType, diff.DiffTypeMissing, diff.DiffTypeExtra,
		diff.DiffTypeNull, diff.DiffTypeError, diff.DiffTypeOrder:
		return true
	}
	return false
}

func (v ValueSpec) validate() error {
	if !v.Present {
		if v.Type != "" || len(v.Value) != 0 {
			return fmt.Errorf("absent value has a type or value")
		}
		return nil
	}
	if len(v.Value) == 0 || v.Type != jsonType(v.Value) {
		return fmt.Errorf("present value has an invalid type or value")
	}
	return nil
}

func jsonType(raw []byte) string {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return ""
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return ""
	}
	switch value.(type) {
	case nil:
		return "null"
	case bool:
		return "boolean"
	case string:
		return "string"
	case json.Number:
		return "number"
	case []any:
		return "array"
	case map[string]any:
		return "object"
	}
	return ""
}

func (r *Registry) Match(context MatchContext, difference diff.Difference) *Rule {
	if context.Baseline.BuildID == "" || context.Target.BuildID == "" {
		return nil
	}
	for i := range r.Rules {
		rule := &r.Rules[i]
		if !r.Applicable(rule, context) || rule.Pointer != difference.Pointer || rule.DiffType != difference.Type {
			continue
		}
		if !rule.Baseline.matches(difference.ExpectedPresent, difference.Expected) ||
			!rule.Target.matches(difference.ActualPresent, difference.Actual) {
			continue
		}
		return rule
	}
	return nil
}

func (r *Registry) Applicable(rule *Rule, context MatchContext) bool {
	if context.Baseline.BuildID == "" || context.Target.BuildID == "" ||
		rule.ChainID != context.ChainID || rule.GenesisHash != context.GenesisHash ||
		rule.CorpusID != context.CorpusID || rule.CaseID != context.CaseID ||
		rule.Method != context.Method || rule.RequestSHA256 != context.RequestSHA256 ||
		!r.inSet(rule.BaselineSet, context.Baseline) || !r.inSet(rule.TargetSet, context.Target) {
		return false
	}
	for _, pair := range rule.Pairs {
		if pair.BaselineBuild == context.Baseline.BuildID && pair.TargetBuild == context.Target.BuildID {
			return true
		}
	}
	return false
}

func (r *Registry) inSet(id string, endpoint Endpoint) bool {
	for _, set := range r.ClientSets {
		if set.ID != id {
			continue
		}
		for _, build := range set.Builds {
			if build.ID == endpoint.BuildID && build.ClientVersion == endpoint.ClientVersion {
				return true
			}
		}
	}
	return false
}

func (v ValueSpec) matches(present bool, actual any) bool {
	if v.Present != present {
		return false
	}
	if !present {
		return true
	}
	data, err := json.Marshal(actual)
	if err != nil || jsonType(data) != v.Type {
		return false
	}
	return sameJSON(v.Value, data)
}

func sameJSON(a, b []byte) bool {
	decode := func(raw []byte) (any, error) {
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.UseNumber()
		var value any
		err := decoder.Decode(&value)
		return value, err
	}
	left, leftErr := decode(a)
	right, rightErr := decode(b)
	return leftErr == nil && rightErr == nil && reflect.DeepEqual(left, right)
}
