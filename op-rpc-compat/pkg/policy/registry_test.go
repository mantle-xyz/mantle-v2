package policy

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/ethereum-optimism/optimism/op-rpc-compat/pkg/diff"
)

func reviewedRegistry() Registry {
	return Registry{
		SchemaVersion: 1,
		RegistryID:    "reviewed-rpc-differences",
		ClientSets: []ClientSet{
			{ID: "geth", Builds: []ClientBuild{{ID: "git:d0169f78", ClientVersion: "Geth/v1.17.3-stable-d0169f78/darwin-arm64/go1.24.9"}}},
			{ID: "reth", Builds: []ClientBuild{{ID: "git:f4963d31", ClientVersion: "mantle-reth/mantle-v1.6.3-dev+f4963d31/aarch64-apple-darwin"}}},
		},
		Rules: []Rule{{
			ID: "reviewed-null", BaselineSet: "geth", TargetSet: "reth",
			CorpusID: "embedded",
			Pairs:    []ReviewedPair{{BaselineBuild: "git:d0169f78", TargetBuild: "git:f4963d31", EvidenceURL: "https://example.test/review"}},
			ChainID:  "0x539", GenesisHash: "0x" + strings.Repeat("11", 32),
			CaseID: "receipt-null", Method: "eth_getTransactionReceipt",
			RequestSHA256: RequestDigest("eth_getTransactionReceipt", []any{"0xabc"}),
			Pointer:       "/result/logs", DiffType: diff.DiffTypeNull,
			Baseline: ValueSpec{Present: true, Type: "null", Value: json.RawMessage(`null`)},
			Target:   ValueSpec{Present: true, Type: "array", Value: json.RawMessage(`[]`)},
			Action:   "warning", Reason: "reviewed response shape", EvidenceURL: "https://example.test/issue",
		}},
	}
}

func TestRegistryMatchesOnlyReviewedDirectedDifference(t *testing.T) {
	registry := reviewedRegistry()
	if err := registry.Validate(); err != nil {
		t.Fatal(err)
	}
	context := MatchContext{
		Baseline: Endpoint{BuildID: "git:d0169f78", ClientVersion: registry.ClientSets[0].Builds[0].ClientVersion},
		Target:   Endpoint{BuildID: "git:f4963d31", ClientVersion: registry.ClientSets[1].Builds[0].ClientVersion},
		ChainID:  "0x539", GenesisHash: "0x" + strings.Repeat("11", 32),
		CorpusID: "embedded",
		CaseID:   "receipt-null", Method: "eth_getTransactionReceipt",
		RequestSHA256: RequestDigest("eth_getTransactionReceipt", []any{"0xabc"}),
	}
	difference := diff.Difference{
		Pointer: "/result/logs", Type: diff.DiffTypeNull,
		ExpectedPresent: true, ActualPresent: true,
		Expected: nil, Actual: []any{}, Severity: diff.SeverityFail,
	}
	if rule := registry.Match(context, difference); rule == nil || rule.ID != "reviewed-null" {
		t.Fatalf("exact difference matched rule = %+v", rule)
	}
	for _, tc := range []struct {
		name   string
		change func(*MatchContext, *diff.Difference)
	}{
		{"reverse direction", func(c *MatchContext, _ *diff.Difference) { c.Baseline, c.Target = c.Target, c.Baseline }},
		{"unknown build", func(c *MatchContext, _ *diff.Difference) { c.Target.BuildID = "" }},
		{"same name different request", func(c *MatchContext, _ *diff.Difference) { c.RequestSHA256 = RequestDigest(c.Method, []any{"0xdef"}) }},
		{"external identical request", func(c *MatchContext, _ *diff.Difference) { c.CorpusID = "sha256:external" }},
		{"extra field", func(_ *MatchContext, d *diff.Difference) { d.Pointer = "/result/other" }},
		{"value drift", func(_ *MatchContext, d *diff.Difference) { d.Actual = []any{"unexpected"} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, d := context, difference
			tc.change(&c, &d)
			if rule := registry.Match(c, d); rule != nil {
				t.Fatalf("unexpected rule: %+v", rule)
			}
		})
	}
}

func TestRegistryRejectsOverlappingAndIncompleteRules(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*Registry)
	}{
		{"duplicate rule", func(r *Registry) { r.Rules = append(r.Rules, r.Rules[0]) }},
		{"unreviewed pair", func(r *Registry) { r.Rules[0].Pairs = nil }},
		{"missing evidence", func(r *Registry) { r.Rules[0].EvidenceURL = "" }},
		{"invalid pointer", func(r *Registry) { r.Rules[0].Pointer = "result.logs" }},
		{"unknown build", func(r *Registry) { r.Rules[0].Pairs[0].TargetBuild = "git:ffffffff" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			registry := reviewedRegistry()
			tc.change(&registry)
			if err := registry.Validate(); err == nil {
				t.Fatal("invalid registry was accepted")
			}
		})
	}
}

func TestBuildIDFromClientVersion(t *testing.T) {
	for _, tc := range []struct{ version, want string }{
		{"Geth/v1.17.3-stable-d0169f78/darwin-arm64/go1.24.9", "git:d0169f78"},
		{"mantle-reth/mantle-v1.6.3-dev+f4963d31/aarch64-apple-darwin", "git:f4963d31"},
		{"mantle-reth/op-reth-v2.2.1-mantle-arsia.2-dev/aarch64-apple-darwin", ""},
		{"op-reth-rpc2", ""},
	} {
		if got := BuildIDFromClientVersion(tc.version); got != tc.want {
			t.Errorf("version %q: build ID %q, want %q", tc.version, got, tc.want)
		}
	}
}

func TestTypeRuleBindsActualPayloads(t *testing.T) {
	registry := reviewedRegistry()
	rule := &registry.Rules[0]
	rule.Pointer = "/result"
	rule.DiffType = diff.DiffTypeType
	rule.Baseline = ValueSpec{Present: true, Type: "object", Value: json.RawMessage(`{"a":1}`)}
	rule.Target = ValueSpec{Present: true, Type: "array", Value: json.RawMessage(`[1]`)}
	context := MatchContext{
		Baseline: Endpoint{BuildID: "git:d0169f78", ClientVersion: registry.ClientSets[0].Builds[0].ClientVersion},
		Target:   Endpoint{BuildID: "git:f4963d31", ClientVersion: registry.ClientSets[1].Builds[0].ClientVersion},
		ChainID:  rule.ChainID, GenesisHash: rule.GenesisHash, CorpusID: rule.CorpusID,
		CaseID: rule.CaseID, Method: rule.Method, RequestSHA256: rule.RequestSHA256,
	}
	for _, tc := range []struct {
		baseline, target string
		want             bool
	}{
		{`{"result":{"a":1}}`, `{"result":[1]}`, true},
		{`{"result":{"b":2}}`, `{"result":[2]}`, false},
	} {
		compared, err := diff.Compare([]byte(tc.baseline), []byte(tc.target), diff.DefaultOptions())
		if err != nil {
			t.Fatal(err)
		}
		if got := registry.Match(context, compared.Differences[0]); (got != nil) != tc.want {
			t.Fatalf("type rule matched = %v for %s / %s", got != nil, tc.baseline, tc.target)
		}
	}
}
