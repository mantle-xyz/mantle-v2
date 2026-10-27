package cmd

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func preconfCommandForTest() *cobra.Command {
	cmd := &cobra.Command{}
	for _, name := range []string{"sequencer-url", "baseline-verifier-url", "target-verifier-url", "op-node-url", "l1-url"} {
		cmd.Flags().String(name, "", "")
	}
	return cmd
}

func TestPreconfRoleFlags(t *testing.T) {
	for _, name := range []string{"sequencer-url", "baseline-verifier-url", "target-verifier-url", "op-node-url", "l1-url"} {
		flag := preconfCmd.Flags().Lookup(name)
		if flag == nil {
			t.Errorf("missing --%s", name)
			continue
		}
		if flag.DefValue != "" {
			t.Errorf("--%s default = %q; endpoint must be explicit", name, flag.DefValue)
		}
	}
	for _, name := range []string{"sequencer", "reth", "geth-verifier", "op-node", "l1"} {
		if preconfCmd.Flags().Lookup(name) != nil {
			t.Errorf("legacy --%s must not be registered", name)
		}
	}
}

func TestPreconfEndpointEnvironment(t *testing.T) {
	t.Setenv("SEQUENCER_RPC_URL", "http://sequencer.example")
	t.Setenv("BASELINE_VERIFIER_RPC_URL", "http://baseline.example")
	t.Setenv("TARGET_VERIFIER_RPC_URL", "http://target.example")
	t.Setenv("OP_NODE_RPC_URL", "http://op-node.example")
	t.Setenv("L1_RPC_URL", "http://l1.example")
	config, err := resolvePreconfConfig(preconfCommandForTest())
	if err != nil {
		t.Fatal(err)
	}
	if config.SequencerURL != "http://sequencer.example" || config.BaselineVerifierURL != "http://baseline.example" || config.TargetVerifierURL != "http://target.example" {
		t.Fatalf("preconf endpoints = %+v", config)
	}
	if config.OpNodeURL != "http://op-node.example" || config.L1URL != "http://l1.example" {
		t.Fatalf("support endpoints = %+v", config)
	}

	cmd := preconfCommandForTest()
	if err := cmd.Flags().Set("target-verifier-url", "http://flag-target.example"); err != nil {
		t.Fatal(err)
	}
	config, err = resolvePreconfConfig(cmd)
	if err != nil || config.TargetVerifierURL != "http://flag-target.example" {
		t.Fatalf("flag override = %+v, %v", config, err)
	}
}

func TestPreconfMissingCoreEndpoints(t *testing.T) {
	for _, name := range []string{"SEQUENCER_RPC_URL", "BASELINE_VERIFIER_RPC_URL", "TARGET_VERIFIER_RPC_URL"} {
		t.Setenv(name, "")
	}
	t.Setenv("GETH_RPC_URL", "http://legacy-geth.example")
	t.Setenv("RETH_RPC_URL", "http://legacy-reth.example")
	cmd := preconfCommandForTest()
	_, err := resolvePreconfConfig(cmd)
	if err == nil || !strings.Contains(err.Error(), "sequencer") {
		t.Fatalf("missing sequencer error = %v", err)
	}
	if err := cmd.Flags().Set("sequencer-url", "http://sequencer.example"); err != nil {
		t.Fatal(err)
	}
	_, err = resolvePreconfConfig(cmd)
	if err == nil || !strings.Contains(err.Error(), "baseline verifier") {
		t.Fatalf("missing baseline verifier error = %v", err)
	}
	if err := cmd.Flags().Set("baseline-verifier-url", "http://baseline.example"); err != nil {
		t.Fatal(err)
	}
	_, err = resolvePreconfConfig(cmd)
	if err == nil || !strings.Contains(err.Error(), "target verifier") {
		t.Fatalf("missing target verifier error = %v", err)
	}
}
