// Package triage identifies changes between RPC compatibility reports without changing verdicts.
package triage

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/ethereum-optimism/optimism/op-rpc-compat/pkg/diff"
	"github.com/ethereum-optimism/optimism/op-rpc-compat/pkg/policy"
	"github.com/ethereum-optimism/optimism/op-rpc-compat/pkg/report"
)

type Finding struct {
	Fingerprint     string        `json:"fingerprint"`
	CorpusID        string        `json:"corpus_id"`
	CaseID          string        `json:"case_id"`
	Method          string        `json:"method"`
	RequestSHA256   string        `json:"request_sha256"`
	ScopeBaseline   string        `json:"scope_baseline,omitempty"`
	ScopeTarget     string        `json:"scope_target,omitempty"`
	Pointer         string        `json:"pointer"`
	DiffType        diff.DiffType `json:"diff_type"`
	ExpectedPresent bool          `json:"expected_present"`
	ActualPresent   bool          `json:"actual_present"`
	Expected        any           `json:"expected"`
	Actual          any           `json:"actual"`
}

type Change struct {
	Previous Finding `json:"previous"`
	Current  Finding `json:"current"`
}

type Summary struct {
	PreviousBuildID string    `json:"previous_build_id,omitempty"`
	CurrentBuildID  string    `json:"current_build_id,omitempty"`
	New             []Finding `json:"new"`
	Changed         []Change  `json:"changed"`
	Resolved        []Finding `json:"resolved"`
	NotRun          []Finding `json:"not_run"`
	Uncompared      []Finding `json:"uncompared"`
	Unchanged       int       `json:"unchanged"`
}

func Compare(previous, current *report.Report) (*Summary, error) {
	if previous == nil || current == nil {
		return nil, fmt.Errorf("both reports are required")
	}
	previousFamilies := families(previous)
	currentFamilies := families(current)
	if previousFamilies.Baseline == "" || previousFamilies.Target == "" ||
		currentFamilies.Baseline == "" || currentFamilies.Target == "" ||
		previousFamilies != currentFamilies || normalizedSuite(previous.SelectedSuite) != normalizedSuite(current.SelectedSuite) ||
		previous.Baseline.ChainID != current.Baseline.ChainID ||
		previous.Target.ChainID != current.Target.ChainID ||
		previous.Baseline.GenesisHash != current.Baseline.GenesisHash ||
		previous.Target.GenesisHash != current.Target.GenesisHash {
		return nil, fmt.Errorf("reports use different client families or chains")
	}
	if previousFamilies.Baseline == previousFamilies.Target && !sameFamilyDirection(previous, current) {
		return nil, fmt.Errorf("same-family comparison direction is ambiguous or reversed")
	}
	before, err := collect(previous, previousFamilies)
	if err != nil {
		return nil, err
	}
	after, err := collect(current, currentFamilies)
	if err != nil {
		return nil, err
	}
	summary := &Summary{PreviousBuildID: previous.Target.BuildID, CurrentBuildID: current.Target.BuildID,
		New: []Finding{}, Changed: []Change{}, Resolved: []Finding{}, NotRun: []Finding{}, Uncompared: []Finding{}}
	currentCases := comparableCases(current)
	executed := executedCases(current)
	locations := make(map[string]bool)
	for key := range before {
		locations[key] = true
	}
	for key := range after {
		locations[key] = true
	}
	keys := make([]string, 0, len(locations))
	for key := range locations {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		old, next := before[key], after[key]
		if len(old) > 0 && !currentCases[caseKey(old[0].CorpusID, old[0].CaseID, old[0].Method, old[0].RequestSHA256)] {
			if executed[caseKey(old[0].CorpusID, old[0].CaseID, old[0].Method, old[0].RequestSHA256)] {
				summary.Uncompared = append(summary.Uncompared, old...)
			} else {
				summary.NotRun = append(summary.NotRun, old...)
			}
			summary.New = append(summary.New, next...)
			continue
		}
		oldByFingerprint := make(map[string][]Finding)
		for _, finding := range old {
			oldByFingerprint[finding.Fingerprint] = append(oldByFingerprint[finding.Fingerprint], finding)
		}
		var unmatchedNext []Finding
		for _, finding := range next {
			matches := oldByFingerprint[finding.Fingerprint]
			if len(matches) == 0 {
				unmatchedNext = append(unmatchedNext, finding)
				continue
			}
			oldByFingerprint[finding.Fingerprint] = matches[1:]
			summary.Unchanged++
		}
		var unmatchedOld []Finding
		for _, matches := range oldByFingerprint {
			unmatchedOld = append(unmatchedOld, matches...)
		}
		sortFindings(unmatchedOld)
		sortFindings(unmatchedNext)
		for len(unmatchedOld) > 0 && len(unmatchedNext) > 0 {
			summary.Changed = append(summary.Changed, Change{Previous: unmatchedOld[0], Current: unmatchedNext[0]})
			unmatchedOld, unmatchedNext = unmatchedOld[1:], unmatchedNext[1:]
		}
		summary.Resolved = append(summary.Resolved, unmatchedOld...)
		summary.New = append(summary.New, unmatchedNext...)
	}
	return summary, nil
}

func sameFamilyDirection(previous, current *report.Report) bool {
	previousBaseline, previousTarget := previous.Baseline.BuildID, previous.Target.BuildID
	currentBaseline, currentTarget := current.Baseline.BuildID, current.Target.BuildID
	if (previousBaseline != "" && previousBaseline == currentTarget) ||
		(previousTarget != "" && previousTarget == currentBaseline) {
		return false
	}
	if previousBaseline != "" && currentBaseline == previousBaseline &&
		currentTarget != "" && currentTarget != previousBaseline {
		return true
	}
	if previousTarget != "" && currentTarget == previousTarget &&
		currentBaseline != "" && currentBaseline != previousTarget {
		return true
	}
	if previousBaseline != "" || previousTarget != "" ||
		(currentBaseline != "" && currentBaseline == currentTarget) {
		return false
	}
	return previous.Baseline.URL != "" && previous.Target.URL != "" &&
		previous.Baseline.URL != previous.Target.URL &&
		previous.Baseline.URL == current.Baseline.URL && previous.Target.URL == current.Target.URL
}

func normalizedSuite(suite string) string {
	if suite == "" {
		return "all"
	}
	return suite
}

func caseKey(corpusID, caseID, method, requestSHA string) string {
	data, _ := json.Marshal(struct{ CorpusID, CaseID, Method, RequestSHA string }{
		corpusID, caseID, method, requestSHA,
	})
	return string(data)
}

func comparableCases(run *report.Report) map[string]bool {
	cases := make(map[string]bool)
	for _, result := range run.Results {
		if result.Status == report.StatusInconclusive || result.Status == report.StatusNotApplicable {
			continue
		}
		if result.BaselineError != "" || result.TargetError != "" || result.CompareError != "" ||
			(result.Status == report.StatusFail && len(result.Differences) == 0) {
			continue
		}
		cases[reportCaseKey(result)] = true
	}
	return cases
}

func executedCases(run *report.Report) map[string]bool {
	cases := make(map[string]bool)
	for _, result := range run.Results {
		cases[reportCaseKey(result)] = true
	}
	return cases
}

func reportCaseKey(result report.TestResult) string {
	requestSHA := result.RequestTemplateSHA256
	if requestSHA == "" {
		requestSHA = policy.RequestDigest(result.TestCase.Method, result.TestCase.Params)
	}
	return caseKey(result.CorpusID, result.TestCase.Name, result.TestCase.Method, requestSHA)
}

func sortFindings(findings []Finding) {
	sort.Slice(findings, func(i, j int) bool { return findings[i].Fingerprint < findings[j].Fingerprint })
}

type pairFamilies struct {
	Baseline string
	Target   string
}

func families(run *report.Report) pairFamilies {
	baseline, target := run.Baseline.ClientFamily, run.Target.ClientFamily
	if baseline == "" {
		baseline = policy.ClientFamilyFromClientVersion(run.Baseline.ClientVersion)
	}
	if target == "" {
		target = policy.ClientFamilyFromClientVersion(run.Target.ClientVersion)
	}
	return pairFamilies{Baseline: baseline, Target: target}
}

func collect(run *report.Report, families pairFamilies) (map[string][]Finding, error) {
	byLocation := make(map[string][]Finding)
	for _, result := range run.Results {
		requestSHA := result.RequestTemplateSHA256
		if requestSHA == "" {
			requestSHA = policy.RequestDigest(result.TestCase.Method, result.TestCase.Params)
		}
		for _, difference := range result.Differences {
			if difference.Severity != diff.SeverityFail {
				continue
			}
			baselineScope, targetScope := difference.ScopeBaseline, difference.ScopeTarget
			if baselineScope == "" {
				baselineScope = "family:" + families.Baseline
			}
			if targetScope == "" {
				targetScope = "family:" + families.Target
			}
			location := struct {
				Families      pairFamilies
				ChainID       string
				Genesis       string
				CorpusID      string
				CaseID        string
				Method        string
				Request       string
				Pointer       string
				BaselineScope string
				TargetScope   string
			}{families, run.Baseline.ChainID, run.Baseline.GenesisHash, result.CorpusID,
				result.TestCase.Name, result.TestCase.Method, requestSHA, difference.Pointer,
				baselineScope, targetScope}
			locationJSON, err := json.Marshal(location)
			if err != nil {
				return nil, fmt.Errorf("encode difference location: %w", err)
			}
			fingerprintJSON, err := json.Marshal(struct {
				Location        json.RawMessage
				Type            diff.DiffType
				ExpectedPresent bool
				ActualPresent   bool
				Expected        any
				Actual          any
			}{locationJSON, difference.Type, difference.ExpectedPresent, difference.ActualPresent,
				difference.Expected, difference.Actual})
			if err != nil {
				return nil, fmt.Errorf("encode difference fingerprint: %w", err)
			}
			sum := sha256.Sum256(fingerprintJSON)
			finding := Finding{Fingerprint: hex.EncodeToString(sum[:]), CorpusID: result.CorpusID,
				CaseID: result.TestCase.Name, Method: result.TestCase.Method, RequestSHA256: requestSHA,
				ScopeBaseline: baselineScope, ScopeTarget: targetScope,
				Pointer: difference.Pointer, DiffType: difference.Type,
				ExpectedPresent: difference.ExpectedPresent, ActualPresent: difference.ActualPresent,
				Expected: difference.Expected, Actual: difference.Actual}
			byLocation[string(locationJSON)] = append(byLocation[string(locationJSON)], finding)
		}
	}
	return byLocation, nil
}
