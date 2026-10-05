// Package cmd provides the RPC compatibility CLI commands.
package cmd

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

type clientConfig struct {
	BaselineURL  string
	TargetURL    string
	BaselineName string
	TargetName   string
}

var (
	baselineURL  string
	targetURL    string
	baselineName string
	targetName   string
	timeout      time.Duration
	verbose      bool
	outputFile   string
	maxRetries   int
	retryDelay   time.Duration

	txTest             bool   // run transaction tests
	txStandardOnly     bool   // skip preconfirmation scenarios
	txPrivateKey       string // transaction test signing key
	txRecipient        string // standard transaction recipient
	txRecipientPreconf string // allowlisted preconfirmation recipient
	txAmount           string // transfer amount in wei
)

var rootCmd = &cobra.Command{
	Use:   "op-rpc-compat",
	Short: "Compare JSON-RPC responses between two clients",
	Long: `Compare the same JSON-RPC requests against a baseline and a target endpoint.
The baseline supplies expected responses for this run; it is not assumed correct.
Both endpoints must serve the same chain.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		config, err := resolveClientConfig(cmd)
		if err != nil {
			return err
		}
		baselineURL, targetURL = config.BaselineURL, config.TargetURL
		baselineName, targetName = config.BaselineName, config.TargetName
		return runTests()
	},
}

func resolveClientConfig(cmd *cobra.Command) (clientConfig, error) {
	var config clientConfig
	var err error
	if config.BaselineURL, err = endpointFlagValue(cmd, "baseline-url", "BASELINE_RPC_URL"); err != nil {
		return config, err
	}
	if config.TargetURL, err = endpointFlagValue(cmd, "target-url", "TARGET_RPC_URL"); err != nil {
		return config, err
	}
	if config.BaselineName, err = endpointFlagValue(cmd, "baseline-name", "BASELINE_NAME"); err != nil {
		return config, err
	}
	if config.TargetName, err = endpointFlagValue(cmd, "target-name", "TARGET_NAME"); err != nil {
		return config, err
	}
	if config.BaselineURL == "" {
		return config, fmt.Errorf("baseline URL is required (--baseline-url or BASELINE_RPC_URL)")
	}
	if config.TargetURL == "" {
		return config, fmt.Errorf("target URL is required (--target-url or TARGET_RPC_URL)")
	}
	if config.BaselineName == "" || config.TargetName == "" {
		return config, fmt.Errorf("baseline and target names must be nonempty")
	}
	return config, nil
}

func endpointFlagValue(cmd *cobra.Command, flagName, envName string) (string, error) {
	value, err := cmd.Flags().GetString(flagName)
	if err != nil {
		return "", err
	}
	if !cmd.Flags().Changed(flagName) {
		if envValue := os.Getenv(envName); envValue != "" {
			value = envValue
		}
	}
	return strings.TrimSpace(value), nil
}

// Execute runs the root command.
func Execute() error {
	return rootCmd.Execute()
}

func init() {
	// Default to Hardhat test account 0.
	defaultPrivateKey := os.Getenv("TX_PRIVATE_KEY")
	if defaultPrivateKey == "" {
		defaultPrivateKey = "0xac0974bec39a17e36ba4a6b4d238ff944bacb478cbed5efcae784d7bf4f2ff80"
	}

	rootCmd.Flags().StringVar(&baselineURL, "baseline-url", "", "Baseline RPC endpoint URL")
	rootCmd.Flags().StringVar(&targetURL, "target-url", "", "Target RPC endpoint URL")
	rootCmd.Flags().StringVar(&baselineName, "baseline-name", "baseline", "Baseline name in reports")
	rootCmd.Flags().StringVar(&targetName, "target-name", "target", "Target name in reports")
	rootCmd.Flags().DurationVar(&timeout, "timeout", 30*time.Second, "Timeout for each RPC request")
	rootCmd.Flags().BoolVarP(&verbose, "verbose", "v", false, "Show detailed output")
	rootCmd.Flags().StringVarP(&outputFile, "output", "o", "report.json", "JSON report output path")
	rootCmd.Flags().IntVar(&maxRetries, "retries", 3, "Number of retries after an RPC request fails")
	rootCmd.Flags().DurationVar(&retryDelay, "retry-delay", 1*time.Second, "Delay between retries")

	rootCmd.Flags().StringVarP(&testFile, "file", "f", "", "Run cases from this file instead of the full corpus")
	rootCmd.Flags().StringVar(&testcasesDir, "testcases-dir", "", "Testcase directory (defaults to the embedded corpus)")
	rootCmd.Flags().StringSliceVar(&excludeFiles, "exclude", nil, "Corpus filename to exclude (repeatable, e.g. --exclude errors_full.json)")

	rootCmd.Flags().BoolVar(&txTest, "tx", false, "Run transaction tests (Legacy, EIP-1559, EIP-7702, and preconfirmation)")
	rootCmd.Flags().BoolVar(&txStandardOnly, "tx-standard-only", false, "Skip preconfirmation scenarios (requires --tx)")
	rootCmd.Flags().StringVar(&txPrivateKey, "tx-private-key", defaultPrivateKey, "Private key used by transaction tests")
	// Hardhat test account 1 is also the recipient in the Python suite.
	rootCmd.Flags().StringVar(&txRecipient, "tx-recipient", "0x70997970c51812dc3a010c7d01b50e0d17dc79c8", "Recipient for standard transactions")
	// The preconfirmation recipient must be in txpool.topreconfs.
	rootCmd.Flags().StringVar(&txRecipientPreconf, "tx-recipient-preconf", "0x71920E3cb420fbD8Ba9a495E6f801c50375ea127", "Allowlisted recipient for preconfirmation transactions")
	rootCmd.Flags().StringVar(&txAmount, "tx-amount", "1000000000000000", "Transfer amount in wei (default 0.001 ETH)")
}
