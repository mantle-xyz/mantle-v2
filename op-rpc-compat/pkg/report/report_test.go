package report

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ethereum-optimism/optimism/op-rpc-compat/pkg/diff"
	"github.com/ethereum-optimism/optimism/op-rpc-compat/pkg/policy"
	"github.com/ethereum-optimism/optimism/op-rpc-compat/pkg/rpc"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/p2p/enode"
	"github.com/ethereum/go-ethereum/p2p/enr"
)

func pairedSuccessfulResult() *rpc.CompareResult {
	return &rpc.CompareResult{
		BaselineResponse: &rpc.ResponseWithMeta{Response: &rpc.Response{Result: json.RawMessage(`null`)}},
		TargetResponse:   &rpc.ResponseWithMeta{Response: &rpc.Response{Result: json.RawMessage(`null`)}},
	}
}

func TestAcceptedPolicyChangesOnlyReviewedDifference(t *testing.T) {
	const versionA = "Geth/v1.17.3-stable-d0169f78/darwin-arm64/go1.24.9"
	const versionB = "mantle-reth/mantle-v1.6.3-dev+f4963d31/aarch64-apple-darwin"
	genesis := "0x" + strings.Repeat("11", 32)
	tc := TestCase{Name: "reviewed-case", Method: "eth_getBlockByNumber", Params: []any{"0x1", false}, CorpusID: "embedded",
		RequestTemplateSHA256: policy.RequestDigest("eth_getBlockByNumber", []any{"{{latest_block_number}}", false})}
	registry := &policy.Registry{
		SchemaVersion: 1, RegistryID: "reviewed",
		ClientSets: []policy.ClientSet{
			{ID: "geth", Builds: []policy.ClientBuild{{ID: "git:d0169f78", ClientVersion: versionA}}},
			{ID: "reth", Builds: []policy.ClientBuild{{ID: "git:f4963d31", ClientVersion: versionB}}},
		},
		Rules: []policy.Rule{{
			ID: "reviewed-field", BaselineSet: "geth", TargetSet: "reth",
			CorpusID: "embedded",
			Pairs:    []policy.ReviewedPair{{BaselineBuild: "git:d0169f78", TargetBuild: "git:f4963d31", EvidenceURL: "https://example.test/review"}},
			ChainID:  "0x539", GenesisHash: genesis, CaseID: tc.Name, Method: tc.Method,
			RequestSHA256: tc.RequestTemplateSHA256,
			Pointer:       "/result/logs", DiffType: diff.DiffTypeNull,
			Baseline: policy.ValueSpec{Present: true, Type: "null", Value: json.RawMessage(`null`)},
			Target:   policy.ValueSpec{Present: true, Type: "array", Value: json.RawMessage(`[]`)},
			Action:   "warning", Reason: "reviewed shape difference", EvidenceURL: "https://example.test/issue",
		}},
	}
	baseline := EndpointMetadata{Name: "baseline", ClientVersion: versionA, BuildID: "git:d0169f78", ChainID: "0x539", GenesisHash: genesis}
	target := EndpointMetadata{Name: "target", ClientVersion: versionB, BuildID: "git:f4963d31", ChainID: "0x539", GenesisHash: genesis}
	reviewed := diff.Difference{Pointer: "/result/logs", Type: diff.DiffTypeNull, Severity: diff.SeverityFail,
		ExpectedPresent: true, ActualPresent: true, Expected: nil, Actual: []any{}}
	for _, variant := range []struct {
		name          string
		mode          string
		buildID       string
		addUnexpected bool
		want          TestStatus
	}{
		{"accepted", "accepted", "git:f4963d31", false, StatusWarning},
		{"strict", "strict", "git:f4963d31", false, StatusFail},
		{"unexpected extra difference", "accepted", "git:f4963d31", true, StatusFail},
		{"unknown build", "accepted", "", false, StatusFail},
	} {
		t.Run(variant.name, func(t *testing.T) {
			selectedTarget := target
			selectedTarget.BuildID = variant.buildID
			r := NewReporter(baseline, selectedTarget, false)
			if err := r.ConfigurePolicy(registry, variant.mode); err != nil {
				t.Fatal(err)
			}
			differences := []diff.Difference{reviewed}
			if variant.addUnexpected {
				differences = append(differences, diff.Difference{Pointer: "/result/new", Type: diff.DiffTypeExtra,
					Severity: diff.SeverityFail, ExpectedPresent: false, ActualPresent: true, Actual: "new"})
			}
			r.AddResult(tc, pairedSuccessfulResult(), &diff.CompareResult{Differences: differences, FailCount: len(differences)}, nil)
			report := r.Generate()
			result := report.Results[0]
			if result.Status != variant.want || result.ObservedStatus != StatusFail ||
				(result.Status == StatusFail) != r.HasFailures() || report.SchemaVersion != 4 {
				t.Fatalf("policy result = %+v", result)
			}
			if result.CorpusID != "embedded" {
				t.Fatalf("case provenance missing: %+v", result)
			}
			if result.RequestTemplateSHA256 != tc.RequestTemplateSHA256 {
				t.Fatalf("template fingerprint missing: %+v", result)
			}
			if report.RegistryID != "reviewed" || len(report.RegistryDigest) != 64 {
				t.Fatalf("registry metadata = %+v", report)
			}
			if variant.buildID == "" && len(report.StaleRuleIDs) != 0 {
				t.Fatalf("unrelated rule was marked stale: %+v", report.StaleRuleIDs)
			}
			if variant.buildID != "" && (result.Differences[0].RuleID != "reviewed-field" ||
				result.Differences[0].RuleReason != "reviewed shape difference") {
				t.Fatalf("reviewed rule not visible: %+v", result.Differences[0])
			}
			if variant.buildID == "" && result.Differences[0].RuleID != "" {
				t.Fatalf("unknown build matched: %+v", result.Differences[0])
			}
		})
	}
	vanished := NewReporter(baseline, target, false)
	if err := vanished.ConfigurePolicy(registry, "accepted"); err != nil {
		t.Fatal(err)
	}
	vanished.AddResult(tc, &rpc.CompareResult{
		BaselineResponse: &rpc.ResponseWithMeta{Response: &rpc.Response{Result: json.RawMessage(`null`)}},
		TargetResponse:   &rpc.ResponseWithMeta{Response: &rpc.Response{Result: json.RawMessage(`null`)}},
	}, &diff.CompareResult{}, nil)
	if got := vanished.Generate(); got.Results[0].Status != StatusPass || len(got.StaleRuleIDs) != 1 {
		t.Fatalf("disappeared reviewed difference = %+v", got)
	}
}

func TestV2BehaviorRuleKeepsRawFailureAndReportsScope(t *testing.T) {
	const baselineVersion = "Geth/v1.17.3-stable-d0169f78/darwin-arm64/go1.24.9"
	const reviewedVersion = "mantle-reth/dev-mantle-v1.6.3-f4963d3/aarch64-macos"
	const newVersion = "mantle-reth/feature-branch-abcdef0/aarch64-macos"
	tc := TestCase{Name: "reviewed-case", Method: "eth_getBlockByNumber", Params: []any{"0x1", false}, CorpusID: "embedded"}
	registry := &policy.Registry{SchemaVersion: 2, RegistryID: "reviewed",
		ClientSets: []policy.ClientSet{
			{ID: "geth", Builds: []policy.ClientBuild{{ID: "git:d0169f78", ClientVersion: baselineVersion}}},
			{ID: "reth", Builds: []policy.ClientBuild{{ID: "git:f4963d3", ClientVersion: reviewedVersion}}},
		},
		Rules: []policy.Rule{{
			ID: "reviewed-field",
			Scope: policy.RuleScope{Kind: "behavior_invariant",
				Baseline: policy.ScopeSelector{Kind: "family", Family: "Geth"},
				Target:   policy.ScopeSelector{Kind: "family", Family: "mantle-reth"}},
			Pairs:   []policy.ReviewedPair{{BaselineBuild: "git:d0169f78", TargetBuild: "git:f4963d3", EvidenceURL: "https://example.test/strict"}},
			ChainID: "0x539", GenesisHash: "0x" + strings.Repeat("11", 32), CorpusID: "embedded",
			CaseID: tc.Name, Method: tc.Method, RequestSHA256: policy.RequestDigest(tc.Method, tc.Params),
			Pointer: "/result/logs", DiffType: diff.DiffTypeNull,
			Baseline: policy.ValueSpec{Present: true, Type: "null", Value: json.RawMessage(`null`)},
			Target:   policy.ValueSpec{Present: true, Type: "array", Value: json.RawMessage(`[]`)},
			Action:   "warning", Reason: "reviewed across builds", EvidenceURL: "https://example.test/review",
		}},
	}
	baseline := EndpointMetadata{Name: "baseline", ClientVersion: baselineVersion, BuildID: "git:d0169f78",
		ChainID: "0x539", GenesisHash: registry.Rules[0].GenesisHash}
	target := EndpointMetadata{Name: "target", ClientVersion: newVersion, BuildID: "git:abcdef0",
		ChainID: "0x539", GenesisHash: registry.Rules[0].GenesisHash}
	reviewed := diff.Difference{Pointer: "/result/logs", Type: diff.DiffTypeNull, Severity: diff.SeverityFail,
		ExpectedPresent: true, ActualPresent: true, Expected: nil, Actual: []any{}}
	for _, variant := range []struct {
		name, mode string
		extra      bool
		want       TestStatus
	}{
		{"accepted new build", "accepted", false, StatusWarning},
		{"strict new build", "strict", false, StatusFail},
		{"unreviewed extra difference", "accepted", true, StatusFail},
	} {
		t.Run(variant.name, func(t *testing.T) {
			r := NewReporter(baseline, target, false)
			if err := r.ConfigurePolicy(registry, variant.mode); err != nil {
				t.Fatal(err)
			}
			differences := []diff.Difference{reviewed}
			if variant.extra {
				differences = append(differences, diff.Difference{Pointer: "/result/new", Type: diff.DiffTypeExtra,
					Severity: diff.SeverityFail, ExpectedPresent: false, ActualPresent: true, Actual: "new"})
			}
			r.AddResult(tc, pairedSuccessfulResult(), &diff.CompareResult{Differences: differences, FailCount: len(differences)}, nil)
			got := r.Generate()
			if got.SchemaVersion != 4 || got.Results[0].Status != variant.want || got.Results[0].ObservedStatus != StatusFail ||
				(got.Results[0].Status == StatusFail) != r.HasFailures() {
				t.Fatalf("v2 policy result = %+v", got.Results[0])
			}
			data, err := json.Marshal(got)
			if err != nil {
				t.Fatal(err)
			}
			var saved struct {
				Baseline struct {
					ClientFamily string `json:"client_family"`
				} `json:"baseline"`
				Target struct {
					ClientFamily string `json:"client_family"`
				} `json:"target"`
				Results []struct {
					Differences []struct {
						ScopeKind     string `json:"scope_kind"`
						ScopeBaseline string `json:"scope_baseline"`
						ScopeTarget   string `json:"scope_target"`
					} `json:"differences"`
				} `json:"results"`
			}
			if err := json.Unmarshal(data, &saved); err != nil {
				t.Fatal(err)
			}
			if saved.Baseline.ClientFamily != "Geth" || saved.Target.ClientFamily != "mantle-reth" ||
				saved.Results[0].Differences[0].ScopeKind != "behavior_invariant" ||
				saved.Results[0].Differences[0].ScopeBaseline != "family:Geth" ||
				saved.Results[0].Differences[0].ScopeTarget != "family:mantle-reth" {
				t.Fatalf("missing policy provenance: %s", data)
			}
		})
	}
}

func TestInconclusiveAndNoComparableCasesFail(t *testing.T) {
	recorded := NewReporter(EndpointMetadata{}, EndpointMetadata{}, false)
	recorded.AddInconclusiveResult(TestCase{Name: "latest", Method: "eth_getBalance"}, nil, "heads differ")
	if got := recorded.Generate().Results[0]; got.Status != StatusInconclusive || !recorded.HasFailures() {
		t.Fatalf("recorded inconclusive = %+v", got)
	}
	r := NewReporter(EndpointMetadata{}, EndpointMetadata{}, false)
	if !r.HasFailures() {
		t.Fatal("empty run must not exit successfully")
	}
	r.results = append(r.results, TestResult{Status: TestStatus("INCONCLUSIVE")})
	if !r.HasFailures() || r.Generate().InconclusiveTests != 1 {
		t.Fatalf("inconclusive run = %+v", r.Generate())
	}
	r.results = []TestResult{{Status: TestStatus("NOT_APPLICABLE")}}
	if !r.HasFailures() || r.Generate().NotApplicableTests != 1 {
		t.Fatalf("no comparable cases = %+v", r.Generate())
	}
}

func TestSuiteReportListsExcludedCasesWithoutPassingThem(t *testing.T) {
	r := NewReporter(EndpointMetadata{}, EndpointMetadata{}, false)
	r.SetSuite("core")
	r.AddExcludedCase(TestCase{Name: "pending", Method: "eth_getBalance", CorpusID: "embedded"},
		"case belongs to diagnostic suite")
	r.AddResult(TestCase{Name: "fixed", Method: "eth_chainId", CorpusID: "embedded"},
		pairedSuccessfulResult(), &diff.CompareResult{}, nil)
	got := r.Generate()
	if got.SelectedSuite != "core" || got.TotalTests != 1 || got.PassedTests != 1 ||
		got.ExcludedTests != 1 || len(got.ExcludedCases) != 1 ||
		got.ExcludedCases[0].Name != "pending" || got.ExcludedCases[0].Reason == "" || r.HasFailures() {
		t.Fatalf("suite report = %+v", got)
	}
	if !strings.Contains(got.Summary, "1 excluded") {
		t.Fatalf("summary hides excluded cases: %q", got.Summary)
	}
}

func TestSemanticAssertionResult(t *testing.T) {
	response := &rpc.CompareResult{
		BaselineResponse: &rpc.ResponseWithMeta{RawBody: []byte(`{"result":"0x1"}`)},
		TargetResponse:   &rpc.ResponseWithMeta{RawBody: []byte(`{"result":"0x2"}`)},
	}
	passed := NewReporter(EndpointMetadata{}, EndpointMetadata{}, false)
	passed.AddAssertionResult(TestCase{Name: "filter", Method: "eth_newFilter"}, response, nil)
	if got := passed.Generate().Results[0]; got.Status != StatusPass ||
		string(got.BaselineResponse) != `{"result":"0x1"}` || string(got.TargetResponse) != `{"result":"0x2"}` {
		t.Fatalf("filter assertion = %+v", got)
	}
	failed := NewReporter(EndpointMetadata{}, EndpointMetadata{}, false)
	failed.AddAssertionResult(TestCase{Name: "filter", Method: "eth_newFilter"}, response, errors.New("uninstall failed"))
	if got := failed.Generate().Results[0]; got.Status != StatusFail || !failed.HasFailures() {
		t.Fatalf("failed filter assertion = %+v", got)
	}
}

func TestNodeIdentitySemanticAssertionWarnsOnlyForOwnedDifferences(t *testing.T) {
	makeBody := func(seed byte, port int, extraProtocol bool) ([]byte, string) {
		keyBytes := make([]byte, 32)
		for i := range keyBytes {
			keyBytes[i] = seed
		}
		key, err := crypto.ToECDSA(keyBytes)
		if err != nil {
			t.Fatal(err)
		}
		ip := net.ParseIP("127.0.0.1")
		node := enode.NewV4(&key.PublicKey, ip, port, port)
		var record enr.Record
		record.Set(enr.IP(ip))
		record.Set(enr.TCP(port))
		record.Set(enr.UDP(port))
		if err := enode.SignV4(&record, key); err != nil {
			t.Fatal(err)
		}
		enrNode, err := enode.New(enode.ValidSchemes, &record)
		if err != nil {
			t.Fatal(err)
		}
		protocols := map[string]any{"eth": map[string]any{"network": 5003}}
		if extraProtocol {
			protocols["snap"] = map[string]any{}
		}
		data, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "result": map[string]any{
			"id": node.ID().String(), "enode": node.URLv4(), "enr": enrNode.String(),
			"name": "mantle-reth/version", "listenAddr": fmt.Sprintf("127.0.0.1:%d", port),
			"ports": map[string]any{"listener": port, "discovery": port}, "protocols": protocols,
		}})
		if err != nil {
			t.Fatal(err)
		}
		return data, node.ID().String()
	}
	baseline, baselineID := makeBody(1, 30303, false)
	tc := TestCase{Name: "admin_nodeInfo_structdiff", Method: "admin_nodeInfo", Params: []any{}, CorpusID: "embedded"}
	for _, variant := range []struct {
		name, mode string
		extra      bool
		corpus     string
		want       TestStatus
		overlap    bool
	}{
		{"reviewed identity", "accepted", false, "embedded", StatusWarning, false},
		{"strict identity", "strict", false, "embedded", StatusFail, false},
		{"additional protocol field", "accepted", true, "embedded", StatusFail, false},
		{"external same-name case", "accepted", false, "external", StatusFail, false},
		{"overlapping exact rule", "accepted", false, "embedded", StatusFail, true},
	} {
		t.Run(variant.name, func(t *testing.T) {
			target, targetID := makeBody(2, 30304, variant.extra)
			compared, err := diff.Compare(baseline, target, diff.DefaultOptions())
			if err != nil {
				t.Fatal(err)
			}
			var baselineRPC, targetRPC rpc.Response
			if err := json.Unmarshal(baseline, &baselineRPC); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(target, &targetRPC); err != nil {
				t.Fatal(err)
			}
			pair := &rpc.CompareResult{
				BaselineResponse: &rpc.ResponseWithMeta{RawBody: baseline, Response: &baselineRPC},
				TargetResponse:   &rpc.ResponseWithMeta{RawBody: target, Response: &targetRPC},
			}
			baselineMeta := EndpointMetadata{ClientVersion: "Geth/v1.17.3-stable-d0169f78/linux-amd64/go1.24", BuildID: "git:d0169f78",
				ChainID: "0x539", GenesisHash: "0xgenesis"}
			targetMeta := EndpointMetadata{ClientVersion: "mantle-reth/dev-f4963d3/aarch64-macos", BuildID: "git:f4963d3",
				ChainID: "0x539", GenesisHash: "0xgenesis"}
			r := NewReporter(baselineMeta, targetMeta, false)
			registry := &policy.Registry{SchemaVersion: 2, RegistryID: "empty"}
			if variant.overlap {
				baselineValue, _ := json.Marshal(baselineID)
				targetValue, _ := json.Marshal(targetID)
				registry.ClientSets = []policy.ClientSet{
					{ID: "geth", Builds: []policy.ClientBuild{{ID: baselineMeta.BuildID, ClientVersion: baselineMeta.ClientVersion}}},
					{ID: "reth", Builds: []policy.ClientBuild{{ID: targetMeta.BuildID, ClientVersion: targetMeta.ClientVersion}}},
				}
				registry.Rules = []policy.Rule{{ID: "overlap",
					Scope: policy.RuleScope{Kind: "behavior_invariant",
						Baseline: policy.ScopeSelector{Kind: "family", Family: "Geth"},
						Target:   policy.ScopeSelector{Kind: "family", Family: "mantle-reth"}},
					Pairs: []policy.ReviewedPair{{BaselineBuild: baselineMeta.BuildID, TargetBuild: targetMeta.BuildID,
						EvidenceURL: "https://example.test/strict"}},
					ChainID: "0x539", GenesisHash: "0xgenesis", CorpusID: "embedded",
					CaseID: tc.Name, Method: tc.Method, RequestSHA256: policy.RequestDigest(tc.Method, tc.Params),
					Pointer: "/result/id", DiffType: diff.DiffTypeValue,
					Baseline: policy.ValueSpec{Present: true, Type: "string", Value: baselineValue},
					Target:   policy.ValueSpec{Present: true, Type: "string", Value: targetValue},
					Action:   "warning", Reason: "duplicate owner", EvidenceURL: "https://example.test/review"}}
			}
			if err := r.ConfigurePolicy(registry, variant.mode); err != nil {
				t.Fatal(err)
			}
			selected := tc
			selected.CorpusID = variant.corpus
			r.AddResult(selected, pair, compared, nil)
			result := r.Generate().Results[0]
			if result.Status != variant.want || result.ObservedStatus != StatusFail ||
				(result.Status == StatusFail) != r.HasFailures() {
				t.Fatalf("semantic policy result = %+v", result)
			}
			if variant.corpus == "embedded" && result.SemanticAssertionID != "node-local-identity" {
				t.Fatalf("missing semantic assertion ID: %+v", result)
			}
			if variant.overlap && result.SemanticError == "" {
				t.Fatalf("overlap was not rejected: %+v", result)
			}
		})
	}
}

func TestReportV4ShowsRawAndEffectiveDifference(t *testing.T) {
	r := NewReporter(EndpointMetadata{}, EndpointMetadata{}, false)
	if err := r.ConfigurePolicy(&policy.Registry{SchemaVersion: 1, RegistryID: "empty"}, "accepted"); err != nil {
		t.Fatal(err)
	}
	r.AddResult(TestCase{Name: "null", Method: "eth_call"}, pairedSuccessfulResult(), &diff.CompareResult{
		Differences: []diff.Difference{{Pointer: "/result", Type: diff.DiffTypeNull,
			Severity: diff.SeverityFail, ExpectedPresent: true, ActualPresent: true, Actual: map[string]any{}}},
		FailCount: 1,
	}, nil)
	data, err := json.Marshal(r.Generate())
	if err != nil {
		t.Fatal(err)
	}
	var saved struct {
		Results []struct {
			EffectiveStatus string                       `json:"effective_status"`
			Differences     []map[string]json.RawMessage `json:"differences"`
		} `json:"results"`
	}
	if err := json.Unmarshal(data, &saved); err != nil {
		t.Fatal(err)
	}
	difference := saved.Results[0].Differences[0]
	if saved.Results[0].EffectiveStatus != "FAIL" || string(difference["observed_severity"]) != `"fail"` ||
		string(difference["effective_severity"]) != `"fail"` || string(difference["expected_present"]) != "true" ||
		string(difference["expected"]) != "null" {
		t.Fatalf("report v3 difference = %s", data)
	}
}

func TestReporterPrintsLogicalEndpointNames(t *testing.T) {
	r := NewReporter(EndpointMetadata{Name: "old-reth", URL: "http://old.example"}, EndpointMetadata{Name: "new-reth", URL: "http://new.example"}, true)
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	previous := os.Stdout
	os.Stdout = writer
	t.Cleanup(func() {
		os.Stdout = previous
		reader.Close()
		writer.Close()
	})
	r.AddResult(TestCase{Name: "transport", Method: "eth_chainId"}, &rpc.CompareResult{
		BaselineResponse: &rpc.ResponseWithMeta{Error: errors.New("baseline down")},
		TargetResponse:   &rpc.ResponseWithMeta{Error: errors.New("target down")},
	}, nil, nil)
	r.PrintSummary()
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	os.Stdout = previous
	output, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(output), "old-reth") || !strings.Contains(string(output), "new-reth") {
		t.Fatalf("missing logical names in output: %s", output)
	}
	if !strings.Contains(string(output), "\n    old-reth:") || !strings.Contains(string(output), "\n    new-reth:") {
		t.Fatalf("error details must use logical names: %s", output)
	}
	if strings.Contains(string(output), "geth error") || strings.Contains(string(output), "reth error") ||
		strings.Contains(string(output), "\n    geth:") || strings.Contains(string(output), "\n    reth:") {
		t.Fatalf("fixed client labels remain: %s", output)
	}
}

func TestRPCErrorDifferenceUsesLogicalEndpointNames(t *testing.T) {
	r := NewReporter(EndpointMetadata{Name: "old-reth"}, EndpointMetadata{Name: "new-reth"}, false)
	r.AddResult(TestCase{Name: "rpc-error", Method: "eth_call"}, &rpc.CompareResult{
		BaselineResponse: &rpc.ResponseWithMeta{Response: &rpc.Response{Error: &rpc.RPCError{Code: -32000, Message: "failed"}}},
		TargetResponse:   &rpc.ResponseWithMeta{Response: &rpc.Response{Result: json.RawMessage(`"0x1"`)}},
	}, nil, nil)
	result := r.Generate().Results[0]
	if len(result.Differences) == 0 || !strings.Contains(result.Differences[0].Message, "old-reth") || !strings.Contains(result.Differences[0].Message, "new-reth") {
		t.Fatalf("RPC error difference = %+v", result.Differences)
	}
}

func TestReportUsesClientIndependentSchema(t *testing.T) {
	r := NewReporter(
		EndpointMetadata{Name: "old-reth", URL: "http://baseline.example", ClientVersion: "reth/2.4"},
		EndpointMetadata{Name: "new-reth", URL: "http://target.example", ClientVersion: "reth/2.5"},
		false,
	)
	r.AddCompatibleResult(TestCase{Name: "sample", Method: "eth_chainId"}, &rpc.CompareResult{
		BaselineResponse: &rpc.ResponseWithMeta{RawBody: []byte(`{"result":"0x1"}`)},
		TargetResponse:   &rpc.ResponseWithMeta{RawBody: []byte(`{"result":"0x2"}`)},
	}, "expected difference")

	data, err := json.Marshal(r.Generate())
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["schema_version"] != float64(4) {
		t.Fatalf("schema_version = %v", decoded["schema_version"])
	}
	for key, want := range map[string]string{"baseline": "old-reth", "target": "new-reth"} {
		endpoint, ok := decoded[key].(map[string]any)
		if !ok || endpoint["name"] != want || endpoint["client_version"] == "" {
			t.Fatalf("%s metadata = %v", key, decoded[key])
		}
	}
	if decoded["geth_url"] != nil || decoded["reth_url"] != nil {
		t.Fatalf("legacy endpoint fields remain: %s", data)
	}
	results := decoded["results"].([]any)
	result := results[0].(map[string]any)
	if result["baseline_response"] == nil || result["target_response"] == nil {
		t.Fatalf("missing client-independent response fields: %v", result)
	}
	if result["geth_response"] != nil || result["reth_response"] != nil {
		t.Fatalf("legacy response fields remain: %v", result)
	}
}

func TestRPCErrorDifferencesFail(t *testing.T) {
	tests := []struct {
		name             string
		baseline, target rpc.RPCError
		wantPath         string
	}{
		{"same message different code", rpc.RPCError{Code: -32000, Message: "unavailable"}, rpc.RPCError{Code: -32601, Message: "unavailable"}, "error.code"},
		{"different data", rpc.RPCError{Code: -32000, Message: "failed", Data: json.RawMessage(`{"reason":1}`)}, rpc.RPCError{Code: -32000, Message: "failed", Data: json.RawMessage(`{"reason":2}`)}, "error.data"},
		{"both method not found with different wording", rpc.RPCError{Code: -32601, Message: "method not found"}, rpc.RPCError{Code: -32601, Message: "not available"}, "error.message"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := NewReporter(EndpointMetadata{Name: "baseline"}, EndpointMetadata{Name: "target"}, false)
			r.AddResult(TestCase{Name: "error", Method: "eth_call"}, &rpc.CompareResult{
				BaselineResponse: &rpc.ResponseWithMeta{Response: &rpc.Response{Error: &tc.baseline}},
				TargetResponse:   &rpc.ResponseWithMeta{Response: &rpc.Response{Error: &tc.target}},
			}, nil, nil)
			result := r.Generate().Results[0]
			if result.Status != StatusFail || !r.HasFailures() {
				t.Fatalf("error mismatch did not fail: %+v", result)
			}
			found := false
			for _, difference := range result.Differences {
				if difference.Path == tc.wantPath && difference.Severity == diff.SeverityFail {
					found = true
				}
			}
			if !found {
				t.Fatalf("missing failing %s difference: %+v", tc.wantPath, result.Differences)
			}
		})
	}
}

func TestOneSidedRPCErrorHasFailingPointer(t *testing.T) {
	r := NewReporter(EndpointMetadata{Name: "baseline"}, EndpointMetadata{Name: "target"}, false)
	r.AddResult(TestCase{Name: "one-sided", Method: "eth_call"}, &rpc.CompareResult{
		BaselineResponse: &rpc.ResponseWithMeta{Response: &rpc.Response{Error: &rpc.RPCError{Code: -32601, Message: "missing"}}},
		TargetResponse:   &rpc.ResponseWithMeta{Response: &rpc.Response{Result: json.RawMessage(`null`)}},
	}, nil, nil)
	result := r.Generate().Results[0]
	if result.Status != StatusFail || len(result.Differences) != 2 {
		t.Fatalf("one-sided error result = %+v", result)
	}
	difference := result.Differences[0]
	if difference.Pointer != "/error" || difference.Type != diff.DiffTypeMissing ||
		difference.Severity != diff.SeverityFail || !difference.ExpectedPresent || difference.ActualPresent {
		t.Fatalf("one-sided error difference = %+v", difference)
	}
	if result.Differences[1].Pointer != "/result" || result.Differences[1].Type != diff.DiffTypeExtra ||
		result.Differences[1].Severity != diff.SeverityFail || result.Differences[1].ExpectedPresent || !result.Differences[1].ActualPresent {
		t.Fatalf("success result was not bound: %+v", result.Differences[1])
	}
}

func TestOneSidedErrorRuleDoesNotAcceptUnreviewedSuccessResult(t *testing.T) {
	const baselineVersion = "Geth/v1.17.3-stable-d0169f78/darwin-arm64/go1.24.9"
	const targetVersion = "mantle-reth/mantle-v1.6.3-dev+f4963d31/aarch64-apple-darwin"
	genesis := "0x" + strings.Repeat("11", 32)
	tc := TestCase{Name: "one-sided", Method: "eth_call", Params: []any{}, CorpusID: "embedded"}
	registry := &policy.Registry{SchemaVersion: 1, RegistryID: "one-sided", ClientSets: []policy.ClientSet{
		{ID: "geth", Builds: []policy.ClientBuild{{ID: "git:d0169f78", ClientVersion: baselineVersion}}},
		{ID: "reth", Builds: []policy.ClientBuild{{ID: "git:f4963d31", ClientVersion: targetVersion}}},
	}, Rules: []policy.Rule{{
		ID: "error-only", BaselineSet: "geth", TargetSet: "reth", CorpusID: "embedded",
		Pairs:   []policy.ReviewedPair{{BaselineBuild: "git:d0169f78", TargetBuild: "git:f4963d31", EvidenceURL: "https://example.test/review"}},
		ChainID: "0x539", GenesisHash: genesis, CaseID: tc.Name, Method: tc.Method,
		RequestSHA256: policy.RequestDigest(tc.Method, tc.Params), Pointer: "/error", DiffType: diff.DiffTypeMissing,
		Baseline: policy.ValueSpec{Present: true, Type: "object", Value: json.RawMessage(`{"code":-32601,"message":"missing"}`)},
		Target:   policy.ValueSpec{Present: false}, Action: "warning", Reason: "reviewed error", EvidenceURL: "https://example.test/issue",
	}}}
	r := NewReporter(
		EndpointMetadata{ClientVersion: baselineVersion, BuildID: "git:d0169f78", ChainID: "0x539", GenesisHash: genesis},
		EndpointMetadata{ClientVersion: targetVersion, BuildID: "git:f4963d31", ChainID: "0x539", GenesisHash: genesis}, false)
	if err := r.ConfigurePolicy(registry, "accepted"); err != nil {
		t.Fatal(err)
	}
	r.AddResult(tc, &rpc.CompareResult{
		BaselineResponse: &rpc.ResponseWithMeta{Response: &rpc.Response{Error: &rpc.RPCError{Code: -32601, Message: "missing"}}},
		TargetResponse:   &rpc.ResponseWithMeta{Response: &rpc.Response{Result: json.RawMessage(`"0x123"`)}},
	}, nil, nil)
	if got := r.Generate().Results[0]; got.Status != StatusFail || !r.HasFailures() {
		t.Fatalf("unreviewed success value was accepted: %+v", got)
	}
}

func TestPairedComparisonRejectsMissingEndpointResponse(t *testing.T) {
	r := NewReporter(EndpointMetadata{}, EndpointMetadata{}, false)
	r.AddResult(TestCase{Name: "incomplete", Method: "eth_getBalance"}, &rpc.CompareResult{
		BaselineResponse: &rpc.ResponseWithMeta{Response: &rpc.Response{Result: json.RawMessage(`"0x1"`)}},
	}, nil, nil)
	if got := r.Generate().Results[0]; got.Status != StatusFail || !r.HasFailures() {
		t.Fatalf("incomplete paired response = %+v", got)
	}
}

func TestUncountedDifferenceStillFails(t *testing.T) {
	r := NewReporter(EndpointMetadata{}, EndpointMetadata{}, false)
	r.AddResult(TestCase{Name: "manual", Method: "eth_getTransactionReceipt"}, pairedSuccessfulResult(),
		&diff.CompareResult{Differences: []diff.Difference{{Pointer: "/result", Type: diff.DiffTypeNull}}}, nil)
	if got := r.Generate().Results[0]; got.Status != StatusFail || !r.HasFailures() {
		t.Fatalf("uncounted difference passed: %+v", got)
	}
}

func TestPairedSuccessRequiresComparison(t *testing.T) {
	r := NewReporter(EndpointMetadata{}, EndpointMetadata{}, false)
	r.AddResult(TestCase{Name: "uncompared", Method: "eth_call"}, pairedSuccessfulResult(), nil, nil)
	if got := r.Generate().Results[0]; got.Status != StatusFail || !r.HasFailures() {
		t.Fatalf("uncompared responses passed: %+v", got)
	}
}

func TestMalformedHTTPBodyDoesNotBreakJSONReport(t *testing.T) {
	badBody := []byte(`{"jsonrpc":`)
	r := NewReporter(EndpointMetadata{}, EndpointMetadata{}, false)
	r.AddResult(TestCase{Name: "malformed", Method: "eth_call"}, &rpc.CompareResult{
		BaselineResponse: &rpc.ResponseWithMeta{RawBody: badBody, Error: errors.New("invalid JSON-RPC response")},
		TargetResponse: &rpc.ResponseWithMeta{RawBody: []byte(`{"jsonrpc":"2.0","id":1,"result":null}`),
			Response: &rpc.Response{Result: json.RawMessage(`null`)}},
	}, nil, nil)
	path := filepath.Join(t.TempDir(), "report.json")
	if err := r.SaveJSON(path); err != nil {
		t.Fatalf("write failed report: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var saved struct {
		Results []struct {
			Status                string `json:"status"`
			BaselineRawBodyBase64 string `json:"baseline_raw_body_base64"`
		} `json:"results"`
	}
	if err := json.Unmarshal(data, &saved); err != nil {
		t.Fatal(err)
	}
	decoded, err := base64.StdEncoding.DecodeString(saved.Results[0].BaselineRawBodyBase64)
	if err != nil || saved.Results[0].Status != "FAIL" || string(decoded) != string(badBody) {
		t.Fatalf("saved malformed response = %s, decode error = %v", data, err)
	}
}

func TestAddCompatibleResultPreservesRPCErrorWithoutFailing(t *testing.T) {
	r := NewReporter(EndpointMetadata{Name: "geth", URL: "geth"}, EndpointMetadata{Name: "reth", URL: "reth"}, false)
	rpcBody := json.RawMessage(`{"jsonrpc":"2.0","id":1,"error":{"code":-32000,"message":"already known"}}`)
	response := &rpc.ResponseWithMeta{
		Response: &rpc.Response{
			JSONRPC: "2.0",
			ID:      1,
			Error:   &rpc.RPCError{Code: -32000, Message: "already known"},
		},
		RawBody: rpcBody,
	}
	compareResult := &rpc.CompareResult{TargetResponse: response}

	r.AddCompatibleResult(
		TestCase{Name: "forwarded_reth", Method: "eth_sendRawTransaction"},
		compareResult,
		"the shared sequencer already knows the forwarded transaction",
	)

	if len(r.results) != 1 {
		t.Fatalf("results length = %d, want 1", len(r.results))
	}
	result := r.results[0]
	if result.Status != StatusCompatible || !result.Passed {
		t.Fatalf("status = %s, passed = %v; want COMPATIBLE and true", result.Status, result.Passed)
	}
	if string(result.TargetResponse) != string(rpcBody) {
		t.Fatalf("reth response = %s, want %s", result.TargetResponse, rpcBody)
	}
	if result.SkipReason == "" {
		t.Fatal("compatible result must retain its reason")
	}
}
