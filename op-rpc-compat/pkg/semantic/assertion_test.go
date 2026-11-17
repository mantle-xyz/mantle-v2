package semantic

import (
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"testing"

	"github.com/ethereum-optimism/optimism/op-rpc-compat/pkg/diff"
	"github.com/ethereum-optimism/optimism/op-rpc-compat/pkg/policy"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/p2p/enode"
	"github.com/ethereum/go-ethereum/p2p/enr"
)

func nodeInfoResponse(seed byte, port int, extraProtocol bool) []byte {
	privateKey := make([]byte, 32)
	for i := range privateKey {
		privateKey[i] = seed
	}
	key, err := crypto.ToECDSA(privateKey)
	if err != nil {
		panic(err)
	}
	address := net.ParseIP("127.0.0.1")
	node := enode.NewV4(&key.PublicKey, address, port, port)
	var record enr.Record
	record.Set(enr.IP(address))
	record.Set(enr.TCP(port))
	record.Set(enr.UDP(port))
	if err := enode.SignV4(&record, key); err != nil {
		panic(err)
	}
	enrNode, err := enode.New(enode.ValidSchemes, &record)
	if err != nil {
		panic(err)
	}
	protocols := map[string]any{"eth": map[string]any{"network": 5003}}
	if extraProtocol {
		protocols["snap"] = map[string]any{}
	}
	response, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 1,
		"result": map[string]any{
			"enode": node.URLv4(), "enr": enrNode.String(), "id": node.ID().String(),
			"name": "mantle-reth/version", "listenAddr": fmt.Sprintf("127.0.0.1:%d", port),
			"ports":     map[string]any{"listener": port, "discovery": port},
			"protocols": protocols,
		},
	})
	return response
}

func TestNodeIdentityAssertionCoversOnlyPresentIdentityValues(t *testing.T) {
	base := nodeInfoResponse(1, 30303, false)
	target := nodeInfoResponse(2, 30304, false)
	caseIdentity := Case{CorpusID: "embedded", CaseID: "admin_nodeInfo_structdiff", Method: "admin_nodeInfo",
		RequestSHA256: policy.RequestDigest("admin_nodeInfo", []any{})}
	compared, err := diff.Compare(base, target, diff.DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	assertion := Evaluate(caseIdentity, base, target)
	if assertion == nil || assertion.Err != nil || assertion.ID != "node-local-identity" {
		t.Fatalf("assertion = %+v", assertion)
	}
	for _, difference := range compared.Differences {
		if !assertion.Covers(difference) {
			t.Fatalf("identity difference not covered: %+v", difference)
		}
	}
	if assertion.Covers(diff.Difference{Pointer: "/result/id", Type: diff.DiffTypeMissing,
		ExpectedPresent: true, ActualPresent: false}) {
		t.Fatal("missing identity field was covered")
	}

	withProtocolChange := nodeInfoResponse(2, 30304, true)
	compared, err = diff.Compare(base, withProtocolChange, diff.DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	assertion = Evaluate(caseIdentity, base, withProtocolChange)
	var foundUnowned bool
	for _, difference := range compared.Differences {
		if !assertion.Covers(difference) {
			foundUnowned = true
		}
	}
	if !foundUnowned {
		t.Fatal("protocol configuration change was covered")
	}
}

func TestNodeIdentityAssertionRejectsInvalidResponseAndExternalCase(t *testing.T) {
	base := nodeInfoResponse(1, 30303, false)
	target := nodeInfoResponse(2, 30304, false)
	caseIdentity := Case{CorpusID: "embedded", CaseID: "admin_nodeInfo_structdiff", Method: "admin_nodeInfo",
		RequestSHA256: policy.RequestDigest("admin_nodeInfo", []any{})}
	for _, tc := range []struct {
		name  string
		field string
		value any
	}{
		{"invalid enode", "enode", "enode://x"},
		{"invalid enr", "enr", "enr:example"},
		{"invalid listening address", "listenAddr", "garbage"},
		{"mismatched id", "id", strings.Repeat("a", 64)},
		{"mismatched port", "ports", map[string]any{"listener": 30305, "discovery": 30304}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var envelope map[string]any
			if err := json.Unmarshal(target, &envelope); err != nil {
				t.Fatal(err)
			}
			envelope["result"].(map[string]any)[tc.field] = tc.value
			bad, err := json.Marshal(envelope)
			if err != nil {
				t.Fatal(err)
			}
			if got := Evaluate(caseIdentity, base, bad); got == nil || got.Err == nil {
				t.Fatalf("invalid node identity was accepted: %+v", got)
			}
		})
	}
	caseIdentity.CorpusID = "external"
	if got := Evaluate(caseIdentity, base, target); got != nil {
		t.Fatalf("external case reused embedded assertion: %+v", got)
	}
}
