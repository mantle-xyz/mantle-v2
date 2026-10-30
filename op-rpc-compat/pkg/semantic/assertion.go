// Package semantic verifies reviewed RPC behavior that cannot be compared byte for byte.
package semantic

import (
	"encoding/json"
	"fmt"
	"net"
	"strconv"
	"strings"

	"github.com/ethereum-optimism/optimism/op-rpc-compat/pkg/diff"
	"github.com/ethereum-optimism/optimism/op-rpc-compat/pkg/policy"
	"github.com/ethereum/go-ethereum/p2p/enode"
)

type Case struct {
	CorpusID      string
	CaseID        string
	Method        string
	RequestSHA256 string
}

type Assertion struct {
	ID     string
	Reason string
	Err    error
}

var nodeIdentityPaths = map[string]bool{
	"/result/enode":           true,
	"/result/enr":             true,
	"/result/id":              true,
	"/result/listenAddr":      true,
	"/result/name":            true,
	"/result/ports/discovery": true,
	"/result/ports/listener":  true,
}

func (a *Assertion) Covers(difference diff.Difference) bool {
	return a != nil && a.Err == nil && a.ID == "node-local-identity" &&
		nodeIdentityPaths[difference.Pointer] && difference.Type == diff.DiffTypeValue &&
		difference.ExpectedPresent && difference.ActualPresent
}

func Evaluate(tc Case, baseline, target json.RawMessage) *Assertion {
	if tc.CorpusID != "embedded" || tc.CaseID != "admin_nodeInfo_structdiff" ||
		tc.Method != "admin_nodeInfo" || tc.RequestSHA256 != policy.RequestDigest(tc.Method, []any{}) {
		return nil
	}
	assertion := &Assertion{ID: "node-local-identity", Reason: "node identity and listening ports are local values"}
	if err := validateNodeIdentity(baseline); err != nil {
		assertion.Err = fmt.Errorf("baseline node identity: %w", err)
	} else if err := validateNodeIdentity(target); err != nil {
		assertion.Err = fmt.Errorf("target node identity: %w", err)
	}
	return assertion
}

func validateNodeIdentity(raw json.RawMessage) error {
	var envelope struct {
		Result json.RawMessage `json:"result"`
		Error  json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil || len(envelope.Result) == 0 || len(envelope.Error) != 0 {
		return fmt.Errorf("invalid successful RPC response")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(envelope.Result, &fields); err != nil || fields == nil {
		return fmt.Errorf("nodeInfo result is not an object")
	}
	readString := func(name string) (string, error) {
		var value string
		if err := json.Unmarshal(fields[name], &value); err != nil || value == "" {
			return "", fmt.Errorf("invalid %s", name)
		}
		return value, nil
	}
	enodeURL, err := readString("enode")
	if err != nil {
		return err
	}
	node, err := enode.ParseV4(enodeURL)
	if err != nil || node.ValidateComplete() != nil {
		return fmt.Errorf("invalid enode")
	}
	enrValue, err := readString("enr")
	if err != nil {
		return err
	}
	enrNode, err := enode.Parse(enode.ValidSchemes, enrValue)
	if err != nil || enrNode.ID() != node.ID() {
		return fmt.Errorf("invalid or mismatched enr")
	}
	id, err := readString("id")
	if err != nil || !strings.EqualFold(id, node.ID().String()) {
		return fmt.Errorf("invalid or mismatched id")
	}
	if _, err := readString("name"); err != nil {
		return err
	}
	listenAddr, err := readString("listenAddr")
	if err != nil {
		return err
	}
	_, listenPort, err := net.SplitHostPort(listenAddr)
	if err != nil {
		return fmt.Errorf("invalid listenAddr: %w", err)
	}
	var ports map[string]json.Number
	if err := json.Unmarshal(fields["ports"], &ports); err != nil || ports == nil {
		return fmt.Errorf("invalid ports")
	}
	for _, name := range []string{"discovery", "listener"} {
		port, err := strconv.Atoi(ports[name].String())
		if err != nil || port < 0 || port > 65535 {
			return fmt.Errorf("invalid %s port", name)
		}
		if name == "discovery" && port != node.UDP() {
			return fmt.Errorf("discovery port does not match enode")
		}
		if name == "listener" {
			parsedListenPort, err := strconv.Atoi(listenPort)
			if err != nil || port != node.TCP() || port != parsedListenPort {
				return fmt.Errorf("listener port does not match enode or listenAddr")
			}
		}
	}
	return nil
}
