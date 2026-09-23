package cmd

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func endpointCommandForTest() *cobra.Command {
	cmd := &cobra.Command{}
	cmd.Flags().String("baseline-url", "", "")
	cmd.Flags().String("target-url", "", "")
	cmd.Flags().String("baseline-name", "baseline", "")
	cmd.Flags().String("target-name", "target", "")
	return cmd
}

func TestClientFlags(t *testing.T) {
	for _, name := range []string{"baseline-url", "target-url", "baseline-name", "target-name"} {
		if rootCmd.Flags().Lookup(name) == nil {
			t.Errorf("missing --%s", name)
		}
	}
	for _, name := range []string{"geth", "reth"} {
		if rootCmd.Flags().Lookup(name) != nil {
			t.Errorf("legacy --%s must not be registered", name)
		}
	}
}

func TestClientEnvironment(t *testing.T) {
	t.Setenv("BASELINE_RPC_URL", "http://baseline.example")
	t.Setenv("TARGET_RPC_URL", "http://target.example")
	t.Setenv("BASELINE_NAME", "old-reth")
	t.Setenv("TARGET_NAME", "new-reth")
	config, err := resolveClientConfig(endpointCommandForTest())
	if err != nil {
		t.Fatal(err)
	}
	if config.BaselineURL != "http://baseline.example" || config.TargetURL != "http://target.example" {
		t.Fatalf("URLs = %q, %q", config.BaselineURL, config.TargetURL)
	}
	if config.BaselineName != "old-reth" || config.TargetName != "new-reth" {
		t.Fatalf("names = %q, %q", config.BaselineName, config.TargetName)
	}
}

func TestClientFlagsOverrideEnvironment(t *testing.T) {
	t.Setenv("BASELINE_RPC_URL", "http://env-baseline.example")
	t.Setenv("TARGET_RPC_URL", "http://env-target.example")
	t.Setenv("BASELINE_NAME", "env-baseline")
	t.Setenv("TARGET_NAME", "env-target")
	cmd := endpointCommandForTest()
	for name, value := range map[string]string{
		"baseline-url":  "http://flag-baseline.example",
		"target-url":    "http://flag-target.example",
		"baseline-name": "flag-baseline",
		"target-name":   "flag-target",
	} {
		if err := cmd.Flags().Set(name, value); err != nil {
			t.Fatal(err)
		}
	}
	config, err := resolveClientConfig(cmd)
	if err != nil {
		t.Fatal(err)
	}
	if config.BaselineURL != "http://flag-baseline.example" || config.TargetURL != "http://flag-target.example" {
		t.Fatalf("URLs = %q, %q", config.BaselineURL, config.TargetURL)
	}
	if config.BaselineName != "flag-baseline" || config.TargetName != "flag-target" {
		t.Fatalf("names = %q, %q", config.BaselineName, config.TargetName)
	}
}

func TestMissingClientURL(t *testing.T) {
	t.Setenv("BASELINE_RPC_URL", "")
	t.Setenv("TARGET_RPC_URL", "")
	t.Setenv("GETH_RPC_URL", "http://legacy-geth.example")
	t.Setenv("RETH_RPC_URL", "http://legacy-reth.example")
	_, err := resolveClientConfig(endpointCommandForTest())
	if err == nil || !strings.Contains(err.Error(), "baseline") {
		t.Fatalf("missing baseline URL error = %v", err)
	}

	cmd := endpointCommandForTest()
	if err := cmd.Flags().Set("baseline-url", "http://baseline.example"); err != nil {
		t.Fatal(err)
	}
	_, err = resolveClientConfig(cmd)
	if err == nil || !strings.Contains(err.Error(), "target") {
		t.Fatalf("missing target URL error = %v", err)
	}
}
