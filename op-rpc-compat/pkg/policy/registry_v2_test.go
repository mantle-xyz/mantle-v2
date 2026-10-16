package policy

import (
	"encoding/json"
	"testing"
	"testing/fstest"

	"github.com/ethereum-optimism/optimism/op-rpc-compat/pkg/diff"
)

const (
	reviewedGethVersion = "Geth/v1.17.3-stable-d0169f78/darwin-arm64/go1.24.9"
	reviewedRethVersion = "mantle-reth/dev-mantle-v1.6.3-f4963d31/aarch64-macos"
)

func loadV2Registry(t *testing.T, scope map[string]any) *Registry {
	t.Helper()
	data, err := json.Marshal(map[string]any{
		"schema_version": 2,
		"registry_id":    "reviewed-rpc-differences",
		"client_sets": []any{
			map[string]any{"id": "geth", "builds": []any{map[string]any{"id": "git:d0169f78", "client_version": reviewedGethVersion}}},
			map[string]any{"id": "reth", "builds": []any{map[string]any{"id": "git:f4963d31", "client_version": reviewedRethVersion}}},
		},
		"rules": []any{map[string]any{
			"id": "reviewed-null", "scope": scope,
			"reviewed_pairs": []any{map[string]any{
				"baseline_build": "git:d0169f78", "target_build": "git:f4963d31",
				"evidence_url": "https://example.test/strict-report",
			}},
			"chain_id": "0x539", "genesis_hash": "0xgenesis", "corpus_id": "embedded",
			"case_id": "receipt-null", "method": "eth_getTransactionReceipt",
			"request_sha256": RequestDigest("eth_getTransactionReceipt", []any{"0xabc"}),
			"pointer":        "/result/logs", "diff_type": diff.DiffTypeNull,
			"baseline": map[string]any{"present": true, "type": "null", "value": nil},
			"target":   map[string]any{"present": true, "type": "array", "value": []any{}},
			"action":   "warning", "reason": "reviewed across builds",
			"evidence_url": "https://example.test/review",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	registry, err := Load(fstest.MapFS{"registry.json": &fstest.MapFile{Data: data}}, "registry.json")
	if err != nil {
		t.Fatal(err)
	}
	return registry
}

func TestBehaviorInvariantReusesReviewedDifferenceAcrossTargetBuilds(t *testing.T) {
	registry := loadV2Registry(t, map[string]any{
		"kind":     "behavior_invariant",
		"baseline": map[string]any{"kind": "family", "family": "Geth"},
		"target":   map[string]any{"kind": "family", "family": "mantle-reth"},
	})
	context := MatchContext{
		Baseline: Endpoint{BuildID: "git:d0169f78", ClientVersion: reviewedGethVersion},
		Target: Endpoint{
			BuildID: "git:abcdef12", ClientVersion: "mantle-reth/new-branch-abcdef12/aarch64-macos",
		},
		ChainID: "0x539", GenesisHash: "0xgenesis", CorpusID: "embedded",
		CaseID: "receipt-null", Method: "eth_getTransactionReceipt",
		RequestSHA256: RequestDigest("eth_getTransactionReceipt", []any{"0xabc"}),
	}
	difference := diff.Difference{
		Pointer: "/result/logs", Type: diff.DiffTypeNull,
		ExpectedPresent: true, ActualPresent: true,
		Expected: nil, Actual: []any{}, Severity: diff.SeverityFail,
	}
	if rule := registry.Match(context, difference); rule == nil || rule.ID != "reviewed-null" {
		t.Fatalf("reviewed difference on new target build matched rule = %+v", rule)
	}
	context.Baseline, context.Target = context.Target, context.Baseline
	if rule := registry.Match(context, difference); rule != nil {
		t.Fatalf("reverse direction matched rule = %+v", rule)
	}
}

func TestClientIdentityFromRethPR135(t *testing.T) {
	version := "mantle-reth/dev-mantle-v1.6.3-f4963d3/aarch64-macos"
	if got := BuildIDFromClientVersion(version); got != "git:f4963d3" {
		t.Fatalf("build ID = %q", got)
	}
	if got := ClientFamilyFromClientVersion(version); got != "mantle-reth" {
		t.Fatalf("client family = %q", got)
	}
	if got := ClientFamilyFromClientVersion("op-reth-rpc2"); got != "" {
		t.Fatalf("overridden identity has family %q", got)
	}
}

func TestBehaviorInvariantRequiresAnchorForSameFamily(t *testing.T) {
	registry := reviewedRegistry()
	registry.SchemaVersion = 2
	registry.Rules[0].BaselineSet, registry.Rules[0].TargetSet = "", ""
	registry.Rules[0].Scope = RuleScope{
		Kind:     "behavior_invariant",
		Baseline: ScopeSelector{Kind: "family", Family: "mantle-reth"},
		Target:   ScopeSelector{Kind: "family", Family: "mantle-reth"},
	}
	if err := registry.Validate(); err == nil {
		t.Fatal("same-family wildcard pair was accepted")
	}
}

func TestBehaviorInvariantAnchoredSameFamilyMatchesNewTargetOnly(t *testing.T) {
	registry := reviewedRegistry()
	registry.SchemaVersion = 2
	registry.ClientSets[1].Builds = append(registry.ClientSets[1].Builds,
		ClientBuild{ID: "git:abcdef12", ClientVersion: "mantle-reth/dev-old-abcdef12/aarch64-macos"})
	rule := &registry.Rules[0]
	rule.BaselineSet, rule.TargetSet = "", ""
	rule.Scope = RuleScope{
		Kind:     "behavior_invariant",
		Baseline: ScopeSelector{Kind: "exact_build", BuildID: "git:f4963d31"},
		Target:   ScopeSelector{Kind: "family", Family: "mantle-reth"},
	}
	rule.Pairs = []ReviewedPair{{BaselineBuild: "git:f4963d31", TargetBuild: "git:abcdef12", EvidenceURL: "https://example.test/strict-report"}}
	if err := registry.Validate(); err != nil {
		t.Fatal(err)
	}
	context := MatchContext{
		Baseline: Endpoint{BuildID: "git:f4963d31", ClientVersion: registry.ClientSets[1].Builds[0].ClientVersion},
		Target:   Endpoint{BuildID: "git:12345678", ClientVersion: "mantle-reth/new-branch-12345678/aarch64-macos"},
		ChainID:  rule.ChainID, GenesisHash: rule.GenesisHash, CorpusID: rule.CorpusID,
		CaseID: rule.CaseID, Method: rule.Method, RequestSHA256: rule.RequestSHA256,
	}
	difference := diff.Difference{Pointer: rule.Pointer, Type: rule.DiffType,
		ExpectedPresent: true, ActualPresent: true,
		Expected: nil, Actual: []any{}, Severity: diff.SeverityFail}
	if got := registry.Match(context, difference); got == nil {
		t.Fatal("new target build did not reuse anchored rule")
	}
	context.Target = context.Baseline
	if got := registry.Match(context, difference); got != nil {
		t.Fatal("the same build matched itself")
	}
	context.Target = Endpoint{ClientVersion: "mantle-reth/legacy/aarch64-macos"}
	if got := registry.Match(context, difference); got != nil {
		t.Fatal("unknown same-family target build matched anchored rule")
	}
	rule.Pairs[0].TargetBuild = rule.Pairs[0].BaselineBuild
	if err := registry.Validate(); err == nil {
		t.Fatal("self-pair review evidence was accepted")
	}
}

func TestExactBuildRuleDoesNotTransferToNewBuild(t *testing.T) {
	registry := loadV2Registry(t, map[string]any{
		"kind":     "exact_build",
		"baseline": map[string]any{"kind": "exact_build", "build_id": "git:d0169f78"},
		"target":   map[string]any{"kind": "exact_build", "build_id": "git:f4963d31"},
	})
	rule := &registry.Rules[0]
	context := MatchContext{
		Baseline: Endpoint{BuildID: "git:d0169f78", ClientVersion: reviewedGethVersion},
		Target:   Endpoint{BuildID: "git:f4963d31", ClientVersion: reviewedRethVersion},
		ChainID:  rule.ChainID, GenesisHash: rule.GenesisHash, CorpusID: rule.CorpusID,
		CaseID: rule.CaseID, Method: rule.Method, RequestSHA256: rule.RequestSHA256,
	}
	difference := diff.Difference{Pointer: rule.Pointer, Type: rule.DiffType,
		ExpectedPresent: true, ActualPresent: true,
		Expected: nil, Actual: []any{}, Severity: diff.SeverityFail}
	if got := registry.Match(context, difference); got == nil {
		t.Fatal("reviewed exact build pair did not match")
	}
	context.Target = Endpoint{BuildID: "git:12345678", ClientVersion: "mantle-reth/new-12345678/aarch64-macos"}
	if got := registry.Match(context, difference); got != nil {
		t.Fatal("unreviewed target build matched exact rule")
	}
}

func TestBehaviorInvariantStillRequiresExactReviewedDifference(t *testing.T) {
	registry := loadV2Registry(t, map[string]any{
		"kind":     "behavior_invariant",
		"baseline": map[string]any{"kind": "family", "family": "Geth"},
		"target":   map[string]any{"kind": "family", "family": "mantle-reth"},
	})
	rule := &registry.Rules[0]
	context := MatchContext{
		Baseline: Endpoint{ClientVersion: "Geth/v1.17.3-stable/linux-amd64/go1.24"},
		Target:   Endpoint{ClientVersion: "mantle-reth/legacy/aarch64-macos"},
		ChainID:  rule.ChainID, GenesisHash: rule.GenesisHash, CorpusID: rule.CorpusID,
		CaseID: rule.CaseID, Method: rule.Method, RequestSHA256: rule.RequestSHA256,
	}
	difference := diff.Difference{Pointer: rule.Pointer, Type: rule.DiffType,
		ExpectedPresent: true, ActualPresent: true,
		Expected: nil, Actual: []any{}, Severity: diff.SeverityFail}
	if got := registry.Match(context, difference); got == nil {
		t.Fatal("reviewed cross-family difference with unknown SHA did not match")
	}
	for _, tc := range []struct {
		name   string
		change func(*MatchContext, *diff.Difference)
	}{
		{"value changed", func(_ *MatchContext, d *diff.Difference) { d.Actual = []any{"new"} }},
		{"extra path", func(_ *MatchContext, d *diff.Difference) { d.Pointer = "/result/other" }},
		{"external corpus", func(c *MatchContext, _ *diff.Difference) { c.CorpusID = "external" }},
		{"overridden identity", func(c *MatchContext, _ *diff.Difference) { c.Target.ClientVersion = "op-reth-rpc2" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, d := context, difference
			tc.change(&c, &d)
			if got := registry.Match(c, d); got != nil {
				t.Fatalf("unreviewed change matched rule = %+v", got)
			}
		})
	}
}

func TestV2RejectsOverlappingBuildAndFamilyScopes(t *testing.T) {
	registry := loadV2Registry(t, map[string]any{
		"kind":     "behavior_invariant",
		"baseline": map[string]any{"kind": "family", "family": "Geth"},
		"target":   map[string]any{"kind": "family", "family": "mantle-reth"},
	})
	overlap := registry.Rules[0]
	overlap.ID = "same-difference-exact-build"
	overlap.Scope = RuleScope{Kind: "exact_build",
		Baseline: ScopeSelector{Kind: "exact_build", BuildID: "git:d0169f78"},
		Target:   ScopeSelector{Kind: "exact_build", BuildID: "git:f4963d31"}}
	registry.Rules = append(registry.Rules, overlap)
	if err := registry.Validate(); err == nil {
		t.Fatal("overlapping exact and family rules were accepted")
	}
}

func TestV2AllowsDistinctReviewedOutcomesAtSamePointer(t *testing.T) {
	registry := loadV2Registry(t, map[string]any{
		"kind":     "behavior_invariant",
		"baseline": map[string]any{"kind": "family", "family": "Geth"},
		"target":   map[string]any{"kind": "family", "family": "mantle-reth"},
	})
	other := registry.Rules[0]
	other.ID = "reviewed-other-array-value"
	other.Target = ValueSpec{Present: true, Type: "array", Value: json.RawMessage(`[1]`)}
	registry.Rules = append(registry.Rules, other)
	if err := registry.Validate(); err != nil {
		t.Fatalf("distinct outcomes at one pointer were treated as overlapping: %v", err)
	}
}

func TestV2RejectsIgnoredLegacyRuleSelectors(t *testing.T) {
	registry := loadV2Registry(t, map[string]any{
		"kind":     "behavior_invariant",
		"baseline": map[string]any{"kind": "family", "family": "Geth"},
		"target":   map[string]any{"kind": "family", "family": "mantle-reth"},
	})
	registry.Rules[0].BaselineSet = "geth"
	if err := registry.Validate(); err == nil {
		t.Fatal("v1 baseline_set was ignored by a v2 rule")
	}
}
