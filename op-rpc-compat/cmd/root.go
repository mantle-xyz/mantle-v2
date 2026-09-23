// Package cmd provides the RPC compatibility CLI commands.
package cmd

import (
	"os"
	"time"

	"github.com/spf13/cobra"
)

var (
	gethURL    string
	rethURL    string
	timeout    time.Duration
	verbose    bool
	outputFile string
	maxRetries int
	retryDelay time.Duration

	txTest             bool   // run transaction tests
	txStandardOnly     bool   // skip preconfirmation scenarios
	txPrivateKey       string // transaction test signing key
	txRecipient        string // standard transaction recipient
	txRecipientPreconf string // allowlisted preconfirmation recipient
	txAmount           string // transfer amount in wei
)

var rootCmd = &cobra.Command{
	Use:   "rpc_compat",
	Short: "Ethereum JSON-RPC 一致性对比测试工具",
	Long: `RPC-Compat 用于验证 geth 和 reth (或任意两个以太坊执行层客户端)
在 JSON-RPC 请求与响应上的完全一致性。

一致性包括：
  - 返回值内容
  - 字段结构
  - 数据类型
  - 错误码与错误信息
  - 边界行为（revert / OOG / invalid params）

默认端点:
  - geth: http://127.0.0.1:19545
  - reth: http://127.0.0.1:29545

示例:
  # 全量 RPC 查询测试（默认输出 report.json）
  rpc_compat

  # 交易测试（包含标准方式和预确认方式）
  rpc_compat --tx

  # 运行指定测试文件
  rpc_compat -f testcases/eth_basic.json

  # 使用自定义端点
  rpc_compat --geth http://geth:8545 --reth http://reth:8545

  # 不输出报告文件
  rpc_compat -o ""`,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runTests()
	},
}

// Execute runs the root command.
func Execute() error {
	return rootCmd.Execute()
}

func init() {
	defaultGethURL := os.Getenv("GETH_RPC_URL")
	if defaultGethURL == "" {
		defaultGethURL = "http://127.0.0.1:19545"
	}
	defaultRethURL := os.Getenv("RETH_RPC_URL")
	if defaultRethURL == "" {
		defaultRethURL = "http://127.0.0.1:29545"
	}

	// Default to Hardhat test account 0.
	defaultPrivateKey := os.Getenv("TX_PRIVATE_KEY")
	if defaultPrivateKey == "" {
		defaultPrivateKey = "0xac0974bec39a17e36ba4a6b4d238ff944bacb478cbed5efcae784d7bf4f2ff80"
	}

	rootCmd.Flags().StringVar(&gethURL, "geth", defaultGethURL, "geth RPC 端点 URL")
	rootCmd.Flags().StringVar(&rethURL, "reth", defaultRethURL, "reth RPC 端点 URL")
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
