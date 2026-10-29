package flags

import (
	"github.com/ethereum/go-ethereum/log"
	"github.com/urfave/cli/v2"

	oplog "github.com/ethereum-optimism/optimism/op-service/log"
)

var (
	EthereumHttpUrlFlag = &cli.StringFlag{
		Name:    "ethereum-http-url",
		Value:   "http://127.0.0.1:8545",
		Usage:   "L1 HTTP Endpoint",
		EnvVars: []string{"GAS_PRICE_ORACLE_ETHEREUM_HTTP_URL"},
	}
	LayerTwoHttpUrlFlag = &cli.StringFlag{
		Name:    "layer-two-http-url",
		Value:   "http://127.0.0.1:9545",
		Usage:   "Sequencer HTTP Endpoint",
		EnvVars: []string{"GAS_PRICE_ORACLE_LAYER_TWO_HTTP_URL"},
	}
	L1ChainIDFlag = &cli.Uint64Flag{
		Name:    "l1-chain-id",
		Usage:   "L1 Chain ID",
		EnvVars: []string{"GAS_PRICE_ORACLE_L1_CHAIN_ID"},
	}
	L2ChainIDFlag = &cli.Uint64Flag{
		Name:    "l2-chain-id",
		Usage:   "L2 Chain ID",
		EnvVars: []string{"GAS_PRICE_ORACLE_L2_CHAIN_ID"},
	}
	GasPriceOracleAddressFlag = &cli.StringFlag{
		Name:    "gas-price-oracle-address",
		Usage:   "Address of BVM_GasPriceOracle",
		Value:   "0x420000000000000000000000000000000000000F",
		EnvVars: []string{"GAS_PRICE_ORACLE_GAS_PRICE_ORACLE_ADDRESS"},
	}
	PrivateKeyFlag = &cli.StringFlag{
		Name:    "private-key",
		Usage:   "Private Key corresponding to BVM_GasPriceOracle Owner",
		EnvVars: []string{"GAS_PRICE_ORACLE_PRIVATE_KEY"},
	}
	TransactionGasPriceFlag = &cli.Uint64Flag{
		Name:    "transaction-gas-price",
		Usage:   "Hardcoded tx.gasPrice, not setting it uses gas estimation",
		EnvVars: []string{"GAS_PRICE_ORACLE_TRANSACTION_GAS_PRICE"},
	}
	LogLevelFlag = &cli.GenericFlag{
		Name:    "loglevel",
		Value:   oplog.NewLevelFlagValue(log.LevelInfo),
		Usage:   "log level to emit to the screen",
		EnvVars: []string{"GAS_PRICE_ORACLE_LOG_LEVEL"},
	}
	TokenRatioEpochLengthSecondsFlag = &cli.Uint64Flag{
		Name:    "token-ratio-epoch-length-seconds",
		Value:   15,
		Usage:   "polling time for updating the token ratio",
		EnvVars: []string{"GAS_PRICE_ORACLE_TOKEN_RATIO_EPOCH_LENGTH_SECONDS"},
	}
	TokenRatioSignificanceFactorFlag = &cli.Float64Flag{
		Name:    "token-ratio-significant-factor",
		Value:   0.05,
		Usage:   "only update when the token ratio changes by more than this factor",
		EnvVars: []string{"GAS_PRICE_ORACLE_TOKEN_RATIO_SIGNIFICANT_FACTOR"},
	}
	TokenRatioCexURL = &cli.StringFlag{
		Name:     "token-ratio-cex-url",
		Usage:    "token ratio cex url",
		EnvVars:  []string{"GAS_PRICE_ORACLE_TOKEN_RATIO_CEX_URL"},
		Required: true,
	}
	TokenRatioDexURL = &cli.StringFlag{
		Name:     "token-ratio-dex-url",
		Usage:    "token ratio dex url",
		EnvVars:  []string{"GAS_PRICE_ORACLE_TOKEN_RATIO_DEX_URL"},
		Required: true,
	}
	TokenRatioUpdateFrequencySecond = &cli.Uint64Flag{
		Name:    "token-ratio-update-frequency-second",
		Value:   3,
		Usage:   "token ratio update frequency",
		EnvVars: []string{"GAS_PRICE_ORACLE_TOKEN_RATIO_UPDATE_FREQUENCY"},
	}
	TokenRatioScalarFlag = &cli.Float64Flag{
		Name:    "token-ratio-scalar",
		Value:   1.00,
		Usage:   "token ratio scalar",
		EnvVars: []string{"GAS_PRICE_ORACLE_TOKEN_RATIO_SCALAR"},
	}
	WaitForReceiptFlag = &cli.BoolFlag{
		Name:    "wait-for-receipt",
		Usage:   "wait for receipts when sending transactions",
		EnvVars: []string{"GAS_PRICE_ORACLE_WAIT_FOR_RECEIPT"},
	}
	MetricsEnabledFlag = &cli.BoolFlag{
		Name:    "metrics",
		Usage:   "Enable metrics collection and reporting",
		EnvVars: []string{"GAS_PRICE_ORACLE_METRICS_ENABLE"},
	}
	MetricsHTTPFlag = &cli.StringFlag{
		Name:    "metrics.addr",
		Usage:   "Enable stand-alone metrics HTTP server listening interface",
		Value:   "127.0.0.1",
		EnvVars: []string{"GAS_PRICE_ORACLE_METRICS_HTTP"},
	}
	MetricsPortFlag = &cli.IntFlag{
		Name:    "metrics.port",
		Usage:   "Metrics HTTP server listening port",
		Value:   9107,
		EnvVars: []string{"GAS_PRICE_ORACLE_METRICS_PORT"},
	}
	EnableHsmFlag = &cli.BoolFlag{
		Name:    "enable-hsm",
		Usage:   "Enalbe the hsm",
		EnvVars: []string{"GAS_PRICE_ORACLE_ENABLE_HSM"},
	}
	HsmAPINameFlag = &cli.StringFlag{
		Name:    "hsm-api-name",
		Usage:   "the api name of hsm",
		EnvVars: []string{"GAS_PRICE_ORACLE_HSM_API_NAME"},
	}
	HsmAddressFlag = &cli.StringFlag{
		Name:    "hsm-address",
		Usage:   "the address of hsm key",
		EnvVars: []string{"GAS_PRICE_ORACLE_HSM_ADDRESS"},
	}
	HsmCredenFlag = &cli.StringFlag{
		Name:    "hsm-creden",
		Usage:   "the creden of hsm key",
		EnvVars: []string{"GAS_PRICE_ORACLE_HSM_CREDEN"},
	}
	HsmProviderFlag = &cli.StringFlag{
		Name:    "hsm-provider",
		Usage:   "Managed key provider used when hsm is enabled: \"gcp\" or \"aws-kms\". Defaults to gcp when empty",
		EnvVars: []string{"GAS_PRICE_ORACLE_HSM_PROVIDER"},
	}
	AWSKMSKeyIDFlag = &cli.StringFlag{
		Name:    "aws-kms-key-id",
		Usage:   "Full AWS KMS key ARN (aliases are not accepted). Only used with --hsm-provider=aws-kms; the AWS region is taken from the ARN",
		EnvVars: []string{"GAS_PRICE_ORACLE_AWS_KMS_KEY_ID"},
	}
)

var Flags = []cli.Flag{
	EthereumHttpUrlFlag,
	LayerTwoHttpUrlFlag,
	L1ChainIDFlag,
	L2ChainIDFlag,
	GasPriceOracleAddressFlag,
	PrivateKeyFlag,
	TransactionGasPriceFlag,
	LogLevelFlag,
	TokenRatioSignificanceFactorFlag,
	TokenRatioEpochLengthSecondsFlag,
	TokenRatioCexURL,
	TokenRatioDexURL,
	TokenRatioUpdateFrequencySecond,
	TokenRatioScalarFlag,
	WaitForReceiptFlag,
	EnableHsmFlag,
	HsmAddressFlag,
	HsmAPINameFlag,
	HsmCredenFlag,
	HsmProviderFlag,
	AWSKMSKeyIDFlag,
	MetricsEnabledFlag,
	MetricsHTTPFlag,
	MetricsPortFlag,
}
