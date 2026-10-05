package cmd

import (
	"regexp"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestCommandHelpIsEnglish(t *testing.T) {
	han := regexp.MustCompile(`\p{Han}`)
	for name, command := range map[string]*cobra.Command{
		"root":    rootCmd,
		"preconf": preconfCmd,
		"stream":  streamCmd,
	} {
		for _, line := range strings.Split(command.UsageString(), "\n") {
			if han.MatchString(line) {
				t.Errorf("%s help contains Chinese: %s", name, line)
			}
		}
	}
}

func endpointCommandForTest() *cobra.Command {
	cmd := &cobra.Command{}
	cmd.Flags().String("baseline-url", "", "")
	cmd.Flags().String("target-url", "", "")
	cmd.Flags().String("diff-policy", "accepted", "")
	return cmd
}

func TestClientFlags(t *testing.T) {
	for _, name := range []string{"baseline-url", "target-url", "diff-policy"} {
		if rootCmd.Flags().Lookup(name) == nil {
			t.Errorf("missing --%s", name)
		}
	}
	for _, name := range []string{"geth", "reth", "baseline-name", "target-name"} {
		if rootCmd.Flags().Lookup(name) != nil {
			t.Errorf("--%s must not be registered", name)
		}
	}
}

func TestDiffPolicyMode(t *testing.T) {
	t.Setenv("BASELINE_RPC_URL", "http://baseline.example")
	t.Setenv("TARGET_RPC_URL", "http://target.example")
	cmd := endpointCommandForTest()
	config, err := resolveClientConfig(cmd)
	if err != nil || config.DiffPolicy != "accepted" {
		t.Fatalf("default policy = %q, error = %v", config.DiffPolicy, err)
	}
	if err := cmd.Flags().Set("diff-policy", "strict"); err != nil {
		t.Fatal(err)
	}
	config, err = resolveClientConfig(cmd)
	if err != nil || config.DiffPolicy != "strict" {
		t.Fatalf("strict policy = %q, error = %v", config.DiffPolicy, err)
	}
	if err := cmd.Flags().Set("diff-policy", "permissive"); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveClientConfig(cmd); err == nil {
		t.Fatal("unknown diff policy was accepted")
	}
}

func TestClientEnvironment(t *testing.T) {
	t.Setenv("BASELINE_RPC_URL", "http://baseline.example")
	t.Setenv("TARGET_RPC_URL", "http://target.example")
	config, err := resolveClientConfig(endpointCommandForTest())
	if err != nil {
		t.Fatal(err)
	}
	if config.BaselineURL != "http://baseline.example" || config.TargetURL != "http://target.example" {
		t.Fatalf("URLs = %q, %q", config.BaselineURL, config.TargetURL)
	}
}

func TestClientFlagsOverrideEnvironment(t *testing.T) {
	t.Setenv("BASELINE_RPC_URL", "http://env-baseline.example")
	t.Setenv("TARGET_RPC_URL", "http://env-target.example")
	cmd := endpointCommandForTest()
	for name, value := range map[string]string{
		"baseline-url": "http://flag-baseline.example",
		"target-url":   "http://flag-target.example",
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
