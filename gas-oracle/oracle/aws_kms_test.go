package oracle

import (
	"context"
	"crypto/ecdsa"
	"crypto/x509/pkix"
	"encoding/asn1"
	"math/big"
	"testing"

	awskms "github.com/aws/aws-sdk-go-v2/service/kms"
	kmstypes "github.com/aws/aws-sdk-go-v2/service/kms/types"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/stretchr/testify/require"
	"github.com/urfave/cli/v2"

	"github.com/ethereum-optimism/optimism/gas-oracle/flags"
	"github.com/ethereum-optimism/optimism/op-service/hsm"
)

const testKeyARN = "arn:aws:kms:ap-southeast-1:123456789012:key/1234abcd-12ab-34cd-56ef-1234567890ab"

type fakeKMS struct{ key *ecdsa.PrivateKey }

func (f *fakeKMS) GetPublicKey(_ context.Context, in *awskms.GetPublicKeyInput, _ ...func(*awskms.Options)) (*awskms.GetPublicKeyOutput, error) {
	curve, _ := asn1.Marshal(asn1.ObjectIdentifier{1, 3, 132, 0, 10})
	der, _ := asn1.Marshal(struct {
		Algorithm pkix.AlgorithmIdentifier
		PublicKey asn1.BitString
	}{
		Algorithm: pkix.AlgorithmIdentifier{Algorithm: asn1.ObjectIdentifier{1, 2, 840, 10045, 2, 1}, Parameters: asn1.RawValue{FullBytes: curve}},
		PublicKey: asn1.BitString{Bytes: crypto.FromECDSAPub(&f.key.PublicKey), BitLength: 65 * 8},
	})
	return &awskms.GetPublicKeyOutput{
		KeyId:             in.KeyId,
		KeySpec:           kmstypes.KeySpecEccSecgP256k1,
		KeyUsage:          kmstypes.KeyUsageTypeSignVerify,
		SigningAlgorithms: []kmstypes.SigningAlgorithmSpec{kmstypes.SigningAlgorithmSpecEcdsaSha256},
		PublicKey:         der,
	}, nil
}

func (f *fakeKMS) Sign(_ context.Context, in *awskms.SignInput, _ ...func(*awskms.Options)) (*awskms.SignOutput, error) {
	sig, err := crypto.Sign(in.Message, f.key)
	if err != nil {
		return nil, err
	}
	der, err := asn1.Marshal(struct{ R, S *big.Int }{new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:64])})
	if err != nil {
		return nil, err
	}
	return &awskms.SignOutput{KeyId: in.KeyId, Signature: der, SigningAlgorithm: in.SigningAlgorithm}, nil
}

func awsConfig(addr common.Address) *Config {
	return &Config{
		L2ChainID:   big.NewInt(5000),
		EnableHsm:   true,
		HsmProvider: hsm.ProviderAWSKMS,
		AWSKMSKeyID: testKeyARN,
		HsmAddress:  addr.Hex(),
	}
}

func TestConfigCheckSigner(t *testing.T) {
	addr := common.HexToAddress("0x1111111111111111111111111111111111111111")
	require.NoError(t, awsConfig(addr).CheckSigner())

	legacy := &Config{EnableHsm: true, HsmAddress: addr.Hex(), HsmAPIName: "projects/x", HsmCreden: "7b7d"}
	require.NoError(t, legacy.CheckSigner())
	require.False(t, legacy.useAWSKMS())

	key, err := crypto.GenerateKey()
	require.NoError(t, err)
	local := &Config{PrivateKey: key}
	require.NoError(t, local.CheckSigner())

	invalid := map[string]func(*Config){
		"private key":      func(c *Config) { c.PrivateKey = key },
		"private key flag": func(c *Config) { c.privateKeySet = true },
		"gcp api name":     func(c *Config) { c.HsmAPIName = "projects/x" },
		"gcp creden":       func(c *Config) { c.HsmCreden = "7b7d" },
		"hsm disabled":     func(c *Config) { c.EnableHsm = false },
		"unknown provider": func(c *Config) { c.HsmProvider = "azure" },
		"alias":            func(c *Config) { c.AWSKMSKeyID = "alias/gas-oracle" },
		"key id on gcp":    func(c *Config) { c.HsmProvider = hsm.ProviderGCP },
	}
	for name, mutate := range invalid {
		cfg := awsConfig(addr)
		mutate(cfg)
		require.Error(t, cfg.CheckSigner(), name)
	}
}

func TestNewTransactOptsAWSKMS(t *testing.T) {
	key, err := crypto.GenerateKey()
	require.NoError(t, err)
	addr := crypto.PubkeyToAddress(key.PublicKey)
	cfg := awsConfig(addr)

	// Without an initialized signer the aws-kms selection must fail instead of
	// falling through to the GCP branch (which would decode empty credentials).
	_, err = newTransactOpts(cfg, nil)
	require.ErrorContains(t, err, "aws kms signer is not initialized")

	signer, err := hsm.NewAWSKMSSignerWithClient(context.Background(), &fakeKMS{key: key}, hsm.AWSKMSConfig{
		KeyARN: testKeyARN, ExpectedAddress: addr.Hex(),
	})
	require.NoError(t, err)

	opts, err := newTransactOpts(cfg, signer)
	require.NoError(t, err)
	require.Equal(t, addr, opts.From)
	require.True(t, opts.NoSend)
	require.NotNil(t, opts.Context)

	to := common.HexToAddress("0x420000000000000000000000000000000000000F")
	tx := types.NewTransaction(3, to, big.NewInt(0), 100000, big.NewInt(1), []byte{0x01})
	signed, err := opts.Signer(opts.From, tx)
	require.NoError(t, err)
	sender, err := types.Sender(types.LatestSignerForChainID(cfg.L2ChainID), signed)
	require.NoError(t, err)
	require.Equal(t, addr, sender)
	require.Equal(t, cfg.L2ChainID, signed.ChainId())
}

// ENABLE_HSM forgotten while the AWS KMS options are set and no private key is present:
// the signer check (run before the private key handling in NewConfig) must report the
// AWS KMS alerting message rather than a generic missing-key error.
func TestCheckSignerAWSWithoutEnableHsm(t *testing.T) {
	cfg := awsConfig(common.HexToAddress("0x1111111111111111111111111111111111111111"))
	cfg.EnableHsm = false
	err := cfg.CheckSigner()
	require.ErrorContains(t, err, "aws kms signer init failed")
	require.ErrorContains(t, err, "hsm is not enabled")
}

// An empty GAS_PRICE_ORACLE_PRIVATE_KEY (used to disable a templated key) must not count
// as a configured private key; urfave/cli reports IsSet=true for an empty env var.
func TestNewConfigEmptyPrivateKeyEnvIsNotSet(t *testing.T) {
	t.Setenv("GAS_PRICE_ORACLE_PRIVATE_KEY", "")
	t.Setenv("GAS_PRICE_ORACLE_ENABLE_HSM", "true")
	t.Setenv("GAS_PRICE_ORACLE_HSM_PROVIDER", hsm.ProviderAWSKMS)
	t.Setenv("GAS_PRICE_ORACLE_HSM_ADDRESS", "0x1111111111111111111111111111111111111111")
	t.Setenv("GAS_PRICE_ORACLE_AWS_KMS_KEY_ID", testKeyARN)

	var cfg *Config
	app := cli.NewApp()
	app.Flags = flags.Flags
	app.Action = func(ctx *cli.Context) error {
		require.True(t, ctx.IsSet(flags.PrivateKeyFlag.Name), "precondition: cli treats the empty env var as set")
		cfg = NewConfig(ctx)
		return nil
	}
	require.NoError(t, app.Run([]string{"gas-oracle", "--token-ratio-cex-url=http://127.0.0.1:1", "--token-ratio-dex-url=http://127.0.0.1:2"}))
	require.False(t, cfg.privateKeySet)
	require.NoError(t, cfg.CheckSigner())
	require.True(t, cfg.useAWSKMS())

	// A non-empty private key together with aws-kms is still a conflict.
	cfg.privateKeySet = true
	require.ErrorContains(t, cfg.CheckSigner(), "cannot be combined")
}
