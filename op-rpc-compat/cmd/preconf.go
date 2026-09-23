package cmd

import (
	"context"
	"os"

	"github.com/ethereum-optimism/optimism/op-rpc-compat/pkg/preconf"

	"github.com/spf13/cobra"
)

var (
	preconfSequencerURL string
	preconfRethURL      string
	preconfGethVerURL   string
	preconfOpNodeURL    string
	preconfL1URL        string
	preconfFunderKey    string
	preconfAddr1Key     string
	preconfAddr3Key     string
	preconfHeavy        bool
	preconfStressCount  int
	preconfOnly         string
)

var preconfCmd = &cobra.Command{
	Use:   "preconf",
	Short: "Mantle 预确认测试（eth_sendRawTransactionWithPreconf）",
	Long: `对 Mantle 预确认做裸 JSON-RPC 测试（不依赖 op-geth fork 的 typed client）：

  - valid_native_success       — 白名单原生转账 preconf 返回 success
  - reason_*                    — 各失败原因：allowance insufficient / out of gas /
                                  intrinsic gas too low / insufficient funds
  - predicted_block_matches     — preconf 预测的 blockHeight == 实际落块
  - geth_reth_parity_null_logs  — 同一笔 revert 交易，geth 序列器与 reth 转发节点返回
                                  的 receipt.logs 形状逐字节一致（守护 logs:null 修复）

前置：devnet 走 op-geth sequencer 路线（task up-all，含 preconf 白名单）。
合约与账户资金由本命令幂等 setup（缺失且 funder nonce 0 时部署；funder 低则从 dev 账户补）。`,
	RunE: func(cmd *cobra.Command, args []string) error {
		runner, err := preconf.NewRunner(preconf.Config{
			SequencerURL: preconfSequencerURL,
			RethURL:      preconfRethURL,
			GethVerURL:   preconfGethVerURL,
			OpNodeURL:    preconfOpNodeURL,
			L1URL:        preconfL1URL,
			FunderKey:    preconfFunderKey,
			Addr1Key:     preconfAddr1Key,
			Addr3Key:     preconfAddr3Key,
			Heavy:        preconfHeavy,
			StressCount:  preconfStressCount,
			Only:         preconfOnly,
		})
		if err != nil {
			return err
		}
		return runner.Run(context.Background())
	},
}

func init() {
	rootCmd.AddCommand(preconfCmd)
	preconfCmd.Flags().StringVar(&preconfSequencerURL, "sequencer", preconfEnvOr("SEQUENCER_RPC_URL", "http://127.0.0.1:9545"), "op-geth sequencer preconf 端点（geth 侧）")
	preconfCmd.Flags().StringVar(&preconfRethURL, "reth", preconfEnvOr("RETH_RPC_URL", "http://127.0.0.1:29545"), "op-reth 转发节点 preconf 端点")
	preconfCmd.Flags().StringVar(&preconfGethVerURL, "geth-verifier", preconfEnvOr("GETH_RPC_URL", "http://127.0.0.1:19545"), "op-geth verifier（转发）端点，用于 verifier 层 parity")
	preconfCmd.Flags().StringVar(&preconfOpNodeURL, "op-node", preconfEnvOr("OP_NODE_RPC_URL", "http://127.0.0.1:9745"), "sequencer op-node admin RPC，用于 A17 停摆测试（admin_stopSequencer）")
	preconfCmd.Flags().StringVar(&preconfL1URL, "l1", preconfEnvOr("L1_RPC_URL", "http://127.0.0.1:38545"), "L1 RPC 端点（deposit 排序用例）")
	preconfCmd.Flags().StringVar(&preconfFunderKey, "funder-key", preconfEnvOr("PRECONF_FUNDER_KEY", "0xac0974bec39a17e36ba4a6b4d238ff944bacb478cbed5efcae784d7bf4f2ff80"), "funder 私钥（0xf39F…2266，白名单发送者）")
	preconfCmd.Flags().StringVar(&preconfAddr1Key, "addr1-key", preconfEnvOr("PRECONF_ADDR1_KEY", "0xe474bfa0d1520cf4b161b382db9f527c39ac16b6d9a8351f091bd406f739a691"), "Addr1 私钥（0x6F18…BDc5，TestPay 调用的白名单发送者）")
	preconfCmd.Flags().StringVar(&preconfAddr3Key, "addr3-key", preconfEnvOr("PRECONF_ADDR3_KEY", "0x654c6b97f400c2facec28bcb2ae04f2bf99e007bd6e41b2ce221481e30840e49"), "Addr3 私钥（0x918a…EC29，ERC20 token owner，approve TestPay）")
	preconfCmd.Flags().BoolVar(&preconfHeavy, "heavy", false, "运行重吞吐套件（stress）")
	preconfCmd.Flags().IntVar(&preconfStressCount, "stress-count", 200, "stress 场景的 preconf 交易笔数（--heavy 时生效）")
	preconfCmd.Flags().StringVar(&preconfOnly, "only", "", "只跑指定名字的场景（其余跳过；setup 仍执行）。用于短超时 profile 下单跑 A8 等")
}

func preconfEnvOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
