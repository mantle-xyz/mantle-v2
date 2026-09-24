package cmd

import (
	"context"
	"fmt"
	"os"

	"github.com/ethereum-optimism/optimism/op-rpc-compat/pkg/preconf"

	"github.com/spf13/cobra"
)

var (
	preconfSequencerURL   string
	preconfTargetURL      string
	preconfBaselineVerURL string
	preconfOpNodeURL      string
	preconfL1URL          string
	preconfFunderKey      string
	preconfAddr1Key       string
	preconfAddr3Key       string
	preconfHeavy          bool
	preconfStressCount    int
	preconfOnly           string
)

var preconfCmd = &cobra.Command{
	Use:   "preconf",
	Short: "Run preconfirmation RPC scenarios",
	Long: `Run preconfirmation scenarios against a configured sequencer and verifier endpoints.
The network and preconfirmation allowlists must be prepared before this command runs.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		config, err := resolvePreconfConfig(cmd)
		if err != nil {
			return err
		}
		config.FunderKey = preconfFunderKey
		config.Addr1Key = preconfAddr1Key
		config.Addr3Key = preconfAddr3Key
		config.Heavy = preconfHeavy
		config.StressCount = preconfStressCount
		config.Only = preconfOnly
		runner, err := preconf.NewRunner(config)
		if err != nil {
			return err
		}
		return runner.Run(context.Background())
	},
}

func resolvePreconfConfig(cmd *cobra.Command) (preconf.Config, error) {
	config := preconf.Config{}
	for _, endpoint := range []struct {
		flag, env string
		value     *string
	}{
		{"sequencer-url", "SEQUENCER_RPC_URL", &config.SequencerURL},
		{"baseline-verifier-url", "BASELINE_VERIFIER_RPC_URL", &config.BaselineVerifierURL},
		{"target-verifier-url", "TARGET_VERIFIER_RPC_URL", &config.TargetVerifierURL},
		{"op-node-url", "OP_NODE_RPC_URL", &config.OpNodeURL},
		{"l1-url", "L1_RPC_URL", &config.L1URL},
	} {
		value, err := endpointFlagValue(cmd, endpoint.flag, endpoint.env)
		if err != nil {
			return config, err
		}
		*endpoint.value = value
	}
	if config.SequencerURL == "" {
		return config, fmt.Errorf("sequencer URL is required (--sequencer-url or SEQUENCER_RPC_URL)")
	}
	if config.BaselineVerifierURL == "" {
		return config, fmt.Errorf("baseline verifier URL is required (--baseline-verifier-url or BASELINE_VERIFIER_RPC_URL)")
	}
	if config.TargetVerifierURL == "" {
		return config, fmt.Errorf("target verifier URL is required (--target-verifier-url or TARGET_VERIFIER_RPC_URL)")
	}
	return config, nil
}

func init() {
	rootCmd.AddCommand(preconfCmd)
	preconfCmd.Flags().StringVar(&preconfSequencerURL, "sequencer-url", "", "Sequencer RPC endpoint URL")
	preconfCmd.Flags().StringVar(&preconfTargetURL, "target-verifier-url", "", "Target verifier RPC endpoint URL")
	preconfCmd.Flags().StringVar(&preconfBaselineVerURL, "baseline-verifier-url", "", "Baseline verifier RPC endpoint URL")
	preconfCmd.Flags().StringVar(&preconfOpNodeURL, "op-node-url", "", "Sequencer op-node admin RPC endpoint URL")
	preconfCmd.Flags().StringVar(&preconfL1URL, "l1-url", "", "L1 RPC endpoint URL")
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
