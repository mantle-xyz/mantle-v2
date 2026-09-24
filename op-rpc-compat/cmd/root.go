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
	rootCmd.Flags().DurationVar(&timeout, "timeout", 30*time.Second, "请求超时时间")
	rootCmd.Flags().BoolVarP(&verbose, "verbose", "v", false, "详细输出")
	rootCmd.Flags().StringVarP(&outputFile, "output", "o", "report.json", "输出报告文件路径 (JSON)")
	rootCmd.Flags().IntVar(&maxRetries, "retries", 3, "请求失败重试次数")
	rootCmd.Flags().DurationVar(&retryDelay, "retry-delay", 1*time.Second, "重试间隔")

	rootCmd.Flags().StringVarP(&testFile, "file", "f", "", "指定测试文件路径 (不指定则运行所有)")
	rootCmd.Flags().StringVar(&testcasesDir, "testcases-dir", "", "测试用例目录（默认使用内嵌用例）")
	rootCmd.Flags().StringSliceVar(&excludeFiles, "exclude", nil, "排除的文件名 (可多次指定，如 --exclude errors_full.json)")

	rootCmd.Flags().BoolVar(&txTest, "tx", false, "运行交易测试（Legacy/EIP-1559/EIP-7702，包含标准和预确认方式）")
	rootCmd.Flags().BoolVar(&txStandardOnly, "tx-standard-only", false, "仅运行标准交易测试，跳过预确认场景（需与 --tx 同时使用）")
	rootCmd.Flags().StringVar(&txPrivateKey, "tx-private-key", defaultPrivateKey, "交易测试私钥")
	// Hardhat test account 1 is also the recipient in the Python suite.
	rootCmd.Flags().StringVar(&txRecipient, "tx-recipient", "0x70997970c51812dc3a010c7d01b50e0d17dc79c8", "交易接收地址（标准方式）")
	// The preconfirmation recipient must be in txpool.topreconfs.
	rootCmd.Flags().StringVar(&txRecipientPreconf, "tx-recipient-preconf", "0x71920E3cb420fbD8Ba9a495E6f801c50375ea127", "预确认交易接收地址（白名单地址）")
	rootCmd.Flags().StringVar(&txAmount, "tx-amount", "1000000000000000", "转账金额（wei，默认 0.001 ETH）")
}
