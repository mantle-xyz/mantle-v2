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

type ScopeSelector struct {
	Kind    string `json:"kind"`
	Family  string `json:"family,omitempty"`
	BuildID string `json:"build_id,omitempty"`
}

func (s ScopeSelector) Descriptor() string {
	switch s.Kind {
	case "family":
		return "family:" + s.Family
	case "exact_build":
		return "exact_build:" + s.BuildID
	default:
		return ""
	}
}

type RuleScope struct {
	Kind     string        `json:"kind"`
	Baseline ScopeSelector `json:"baseline"`
	Target   ScopeSelector `json:"target"`
}

type ValueSpec struct {
	Present bool            `json:"present"`
	Type    string          `json:"type,omitempty"`
	Value   json.RawMessage `json:"value,omitempty"`
}

type Rule struct {
	ID            string         `json:"id"`
	Scope         RuleScope      `json:"scope,omitempty"`
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

var buildSHA = regexp.MustCompile(`(?:\+|-)([0-9a-f]{7,40})(?:/|$)`)

var clientFamily = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9._-]*/`)

// ClientFamilyFromClientVersion returns the self-reported client family.
func ClientFamilyFromClientVersion(version string) string {
	match := clientFamily.FindString(version)
	return strings.TrimSuffix(match, "/")
}

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
	if (r.SchemaVersion != 1 && r.SchemaVersion != 2) || r.RegistryID == "" {
		return fmt.Errorf("invalid registry header")
	}
	sets := make(map[string]map[string]ClientBuild)
	allBuilds := make(map[string]ClientBuild)
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
			if r.SchemaVersion == 2 && allBuilds[build.ID].ID != "" {
				return fmt.Errorf("duplicate build ID %q", build.ID)
			}
			allBuilds[build.ID] = build
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
		if len(rule.Pairs) == 0 {
			return fmt.Errorf("rule %q has no reviewed pairs", rule.ID)
		}
		if r.SchemaVersion == 1 {
			if sets[rule.BaselineSet] == nil || sets[rule.TargetSet] == nil {
				return fmt.Errorf("rule %q has unknown client sets", rule.ID)
			}
		} else {
			if rule.BaselineSet != "" || rule.TargetSet != "" {
				return fmt.Errorf("rule %q mixes v1 and v2 selectors", rule.ID)
			}
			if err := rule.Scope.validate(allBuilds); err != nil {
				return fmt.Errorf("rule %q scope: %w", rule.ID, err)
			}
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
			baselineBuild, targetBuild := allBuilds[pair.BaselineBuild], allBuilds[pair.TargetBuild]
			if pair.EvidenceURL == "" || baselineBuild.ID == "" || targetBuild.ID == "" {
				return fmt.Errorf("rule %q has an unreviewed build pair", rule.ID)
			}
			if r.SchemaVersion == 2 && pair.BaselineBuild == pair.TargetBuild {
				return fmt.Errorf("rule %q has a self-pair review example", rule.ID)
			}
			if r.SchemaVersion == 1 && (sets[rule.BaselineSet][pair.BaselineBuild].ID == "" ||
				sets[rule.TargetSet][pair.TargetBuild].ID == "") {
				return fmt.Errorf("rule %q has an unreviewed build pair", rule.ID)
			}
			if r.SchemaVersion == 2 && (!rule.Scope.Baseline.matchesBuild(baselineBuild) ||
				!rule.Scope.Target.matchesBuild(targetBuild)) {
				return fmt.Errorf("rule %q reviewed pair is outside its scope", rule.ID)
			}
			if r.SchemaVersion == 2 {
				continue
			}
			scope := fmt.Sprintf("%s|%s|%s|%s|%s|%s|%s|%s|%s", rule.ChainID, rule.GenesisHash,
				rule.CorpusID, rule.CaseID, rule.Method, rule.RequestSHA256, rule.Pointer, pair.BaselineBuild, pair.TargetBuild)
			if scopes[scope] {
				return fmt.Errorf("rule %q overlaps another rule", rule.ID)
			}
			scopes[scope] = true
		}
	}
	if r.SchemaVersion == 2 {
		for i := range r.Rules {
			for j := i + 1; j < len(r.Rules); j++ {
				if rulesOverlap(r.Rules[i], r.Rules[j], allBuilds) {
					return fmt.Errorf("rules %q and %q overlap", r.Rules[i].ID, r.Rules[j].ID)
				}
			}
		}
	}
	return nil
}

func (s RuleScope) validate(builds map[string]ClientBuild) error {
	if s.Kind != "behavior_invariant" && s.Kind != "exact_build" {
		return fmt.Errorf("unknown scope kind %q", s.Kind)
	}
	if err := s.Baseline.validate(builds); err != nil {
		return fmt.Errorf("baseline: %w", err)
	}
	if err := s.Target.validate(builds); err != nil {
		return fmt.Errorf("target: %w", err)
	}
	if s.Kind == "exact_build" {
		if s.Baseline.Kind != "exact_build" || s.Target.Kind != "exact_build" {
			return fmt.Errorf("exact_build scope requires two exact builds")
		}
		return nil
	}
	if s.Baseline.Kind != "family" && s.Target.Kind != "family" {
		return fmt.Errorf("behavior_invariant scope requires a family selector")
	}
	if s.Baseline.Kind == "family" && s.Target.Kind == "family" && s.Baseline.Family == s.Target.Family {
		return fmt.Errorf("same-family comparison requires an exact build anchor")
	}
	return nil
}

func (s ScopeSelector) validate(builds map[string]ClientBuild) error {
	switch s.Kind {
	case "family":
		if s.Family == "" || s.BuildID != "" || ClientFamilyFromClientVersion(s.Family+"/version") != s.Family {
			return fmt.Errorf("invalid family selector")
		}
	case "exact_build":
		if s.Family != "" || builds[s.BuildID].ID == "" {
			return fmt.Errorf("invalid exact build selector")
		}
	default:
		return fmt.Errorf("unknown selector kind %q", s.Kind)
	}
	return nil
}

func (s ScopeSelector) matchesBuild(build ClientBuild) bool {
	if s.Kind == "family" {
		return ClientFamilyFromClientVersion(build.ClientVersion) == s.Family
	}
	return s.Kind == "exact_build" && build.ID == s.BuildID
}

func (s ScopeSelector) matches(endpoint Endpoint, builds map[string]ClientBuild) bool {
	if s.Kind == "family" {
		return ClientFamilyFromClientVersion(endpoint.ClientVersion) == s.Family
	}
	build := builds[s.BuildID]
	return s.Kind == "exact_build" && endpoint.BuildID == build.ID && endpoint.ClientVersion == build.ClientVersion
}

func rulesOverlap(a, b Rule, builds map[string]ClientBuild) bool {
	if a.ChainID != b.ChainID || a.GenesisHash != b.GenesisHash || a.CorpusID != b.CorpusID ||
		a.CaseID != b.CaseID || a.Method != b.Method || a.RequestSHA256 != b.RequestSHA256 ||
		a.Pointer != b.Pointer || a.DiffType != b.DiffType ||
		!sameValueSpec(a.Baseline, b.Baseline) || !sameValueSpec(a.Target, b.Target) {
		return false
	}
	return selectorsOverlap(a.Scope.Baseline, b.Scope.Baseline, builds) &&
		selectorsOverlap(a.Scope.Target, b.Scope.Target, builds)
}

func sameValueSpec(a, b ValueSpec) bool {
	if a.Present != b.Present || a.Type != b.Type {
		return false
	}
	return !a.Present || sameJSON(a.Value, b.Value)
}

func selectorsOverlap(a, b ScopeSelector, builds map[string]ClientBuild) bool {
	if a.Kind == "family" && b.Kind == "family" {
		return a.Family == b.Family
	}
	if a.Kind == "exact_build" && b.Kind == "exact_build" {
		return a.BuildID == b.BuildID
	}
	if a.Kind == "exact_build" {
		return ClientFamilyFromClientVersion(builds[a.BuildID].ClientVersion) == b.Family
	}
	return a.Family == ClientFamilyFromClientVersion(builds[b.BuildID].ClientVersion)
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
	if rule.ChainID != context.ChainID || rule.GenesisHash != context.GenesisHash ||
		rule.CorpusID != context.CorpusID || rule.CaseID != context.CaseID ||
		rule.Method != context.Method || rule.RequestSHA256 != context.RequestSHA256 {
		return false
	}
	if r.SchemaVersion == 2 {
		builds := make(map[string]ClientBuild)
		for _, set := range r.ClientSets {
			for _, build := range set.Builds {
				builds[build.ID] = build
			}
		}
		if !rule.Scope.Baseline.matches(context.Baseline, builds) ||
			!rule.Scope.Target.matches(context.Target, builds) {
			return false
		}
		if rule.Scope.Kind == "behavior_invariant" && context.Baseline.BuildID == context.Target.BuildID &&
			context.Baseline.BuildID != "" {
			return false
		}
		if rule.Scope.Baseline.Kind != rule.Scope.Target.Kind &&
			ClientFamilyFromClientVersion(context.Baseline.ClientVersion) == ClientFamilyFromClientVersion(context.Target.ClientVersion) &&
			(context.Baseline.BuildID == "" || context.Target.BuildID == "") {
			return false
		}
		if rule.Scope.Kind == "behavior_invariant" {
			return true
		}
	} else if context.Baseline.BuildID == "" || context.Target.BuildID == "" ||
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
