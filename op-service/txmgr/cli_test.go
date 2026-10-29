package txmgr

import (
	"math"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/log"
	"github.com/stretchr/testify/require"
	"github.com/urfave/cli/v2"
)

var (
	l1EthRpcValue = "http://localhost:9546"
)

func TestDefaultCLIOptionsMatchDefaultConfig(t *testing.T) {
	cfg := configForArgs()
	defaultCfg := NewCLIConfig(l1EthRpcValue, DefaultBatcherFlagValues)

	require.Equal(t, defaultCfg, cfg)
}

func TestDefaultConfigIsValid(t *testing.T) {
	cfg := NewCLIConfig(l1EthRpcValue, DefaultBatcherFlagValues)
	require.NoError(t, cfg.Check())
}

func configForArgs(args ...string) CLIConfig {
	app := cli.NewApp()
	// txmgr expects the --l1-eth-rpc option to be declared externally
	flags := append(CLIFlags("TEST_"), &cli.StringFlag{
		Name:  L1RPCFlagName,
		Value: l1EthRpcValue,
	})
	app.Flags = flags
	app.Name = "test"
	var config CLIConfig
	app.Action = func(ctx *cli.Context) error {
		config = ReadCLIConfig(ctx)
		return nil
	}
	_ = app.Run(args)
	return config
}

func TestFallbackToOsakaCellProofTimeIfKnown(t *testing.T) {
	// Mantle geth has not yet been updated with upstream. Once it is, we can unskip these tests.
	t.Skip("Skipping TestFallbackToOsakaCellProofTimeIfKnown due to old upstream code of geth")
	// No override, but we detect the L1 is Mainnet
	cellProofTime := fallbackToOsakaCellProofTimeIfKnown(big.NewInt(1), math.MaxUint64)
	require.Equal(t, uint64(1764798551), cellProofTime)

	// No override, but we detect the L1 is Sepolia
	cellProofTime = fallbackToOsakaCellProofTimeIfKnown(big.NewInt(11155111), math.MaxUint64)
	require.Equal(t, uint64(1760427360), cellProofTime)

	// Override is set, so we ignore known L1 config and use the override
	cellProofTime = fallbackToOsakaCellProofTimeIfKnown(big.NewInt(1), 654321)
	require.Equal(t, uint64(654321), cellProofTime)

	// No override set, but L1 Network is not known, so we never use cell proofs
	cellProofTime = fallbackToOsakaCellProofTimeIfKnown(big.NewInt(33), math.MaxUint64)
	require.Equal(t, uint64(18446744073709551615), cellProofTime)
}

const (
	testAWSKMSKeyARN = "arn:aws:kms:ap-southeast-1:123456789012:key/1234abcd-12ab-34cd-56ef-1234567890ab"
	testHsmAddress   = "0x1111111111111111111111111111111111111111"
)

func TestHsmProviderFlags(t *testing.T) {
	cfg := configForArgs("test",
		"--enable-hsm",
		"--hsm-provider=aws-kms",
		"--aws-kms-key-id="+testAWSKMSKeyARN,
		"--hsm-address="+testHsmAddress,
	)
	require.True(t, cfg.EnableHsm)
	require.Equal(t, "aws-kms", cfg.HsmProvider)
	require.Equal(t, testAWSKMSKeyARN, cfg.AWSKMSKeyID)
	require.NoError(t, cfg.Check())
	require.True(t, cfg.hsmSelection().UseAWSKMS())
}

func TestHsmProviderCheck(t *testing.T) {
	awsCfg := func(mutate func(*CLIConfig)) CLIConfig {
		cfg := NewCLIConfig(l1EthRpcValue, DefaultBatcherFlagValues)
		cfg.EnableHsm = true
		cfg.HsmProvider = "aws-kms"
		cfg.AWSKMSKeyID = testAWSKMSKeyARN
		cfg.HsmAddress = testHsmAddress
		if mutate != nil {
			mutate(&cfg)
		}
		return cfg
	}
	require.NoError(t, awsCfg(nil).Check())

	// Existing configurations without the new options keep working.
	legacy := NewCLIConfig(l1EthRpcValue, DefaultBatcherFlagValues)
	legacy.EnableHsm = true
	legacy.HsmAddress = testHsmAddress
	legacy.HsmAPIName = "projects/p/locations/l/keyRings/r/cryptoKeys/k/cryptoKeyVersions/1"
	legacy.HsmCreden = "7b7d"
	require.NoError(t, legacy.Check())
	require.False(t, legacy.hsmSelection().UseAWSKMS())

	invalid := map[string]CLIConfig{
		"private key": awsCfg(func(c *CLIConfig) { c.PrivateKey = "0x01" }),
		"mnemonic":    awsCfg(func(c *CLIConfig) { c.Mnemonic = "test test test" }),
		"remote signer": awsCfg(func(c *CLIConfig) {
			c.SignerCLIConfig.Endpoint = "http://signer"
			c.SignerCLIConfig.Address = testHsmAddress
		}),
		"gcp api name":     awsCfg(func(c *CLIConfig) { c.HsmAPIName = "projects/x" }),
		"gcp creden":       awsCfg(func(c *CLIConfig) { c.HsmCreden = "7b7d" }),
		"hsm disabled":     awsCfg(func(c *CLIConfig) { c.EnableHsm = false }),
		"unknown provider": awsCfg(func(c *CLIConfig) { c.HsmProvider = "azure" }),
		"alias":            awsCfg(func(c *CLIConfig) { c.AWSKMSKeyID = "alias/batcher" }),
		"missing key":      awsCfg(func(c *CLIConfig) { c.AWSKMSKeyID = "" }),
		"zero address":     awsCfg(func(c *CLIConfig) { c.HsmAddress = "0x0000000000000000000000000000000000000000" }),
		"key id on gcp":    awsCfg(func(c *CLIConfig) { c.HsmProvider = "gcp" }),
	}
	for name, cfg := range invalid {
		require.Error(t, cfg.Check(), name)
	}
}

func TestNewSignerFactoryKeepsLegacyPath(t *testing.T) {
	cfg := NewCLIConfig(l1EthRpcValue, DefaultBatcherFlagValues)
	cfg.PrivateKey = "4c0883a69102937d6231471b5dbb6204fe5129617082792ae468d01a3f362318"
	factory, from, closer, err := newSignerFactory(cfg, "", log.New())
	require.NoError(t, err)
	require.NotNil(t, factory)
	require.NotNil(t, closer)
	require.Equal(t, common.HexToAddress("0x2c7536E3605D9C16a7a3D7b1898e529396a65c23"), from)
}
