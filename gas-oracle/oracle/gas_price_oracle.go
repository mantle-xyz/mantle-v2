package oracle

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"time"

	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/ethereum/go-ethereum/log"

	"github.com/ethereum-optimism/optimism/gas-oracle/bindings"
	"github.com/ethereum-optimism/optimism/gas-oracle/tokenratio"
	"github.com/ethereum-optimism/optimism/op-service/hsm"
)

var (
	// errInvalidSigningKey represents the error when the signing key used
	// is not the Owner of the contract and therefore cannot update the gasprice
	errInvalidSigningKey = errors.New("invalid signing key")
	// errNoChainID represents the error when the chain id is not provided
	// and it cannot be remotely fetched
	errNoChainID = errors.New("no chain id provided")
	// errNoPrivateKey represents the error when the private key is not provided to
	// the application
	errNoPrivateKey = errors.New("no private key provided")
	// errWrongChainID represents the error when the configured chain id is not
	// correct
	errWrongChainID = errors.New("wrong chain id provided")
)

// GasPriceOracle manages a hot key that can update the L2 Gas Price
type GasPriceOracle struct {
	l1ChainID  *big.Int
	l2ChainID  *big.Int
	ctx        context.Context
	stop       chan struct{}
	contract   *bindings.GasPriceOracle
	l2Backend  DeployContractBackend
	l1Backend  bind.ContractTransactor
	tokenRatio *tokenratio.Client
	config     *Config
	// awsSigner is initialized at startup when the aws-kms hsm provider is selected.
	awsSigner *hsm.AWSKMSSigner
}

// Start runs the GasPriceOracle
func (g *GasPriceOracle) Start() error {
	if g.config.L1ChainID == nil {
		return fmt.Errorf("layer-one: %w", errNoChainID)
	}
	if g.config.L2ChainID == nil {
		return fmt.Errorf("layer-two: %w", errNoChainID)
	}
	var address common.Address
	if !g.config.EnableHsm {
		if g.config.PrivateKey == nil {
			return errNoPrivateKey
		}
		address = crypto.PubkeyToAddress(g.config.PrivateKey.PublicKey)
	} else {
		address = common.HexToAddress(g.config.HsmAddress)
	}

	log.Info("Starting Gas Price Oracle", "l1-chain-id", g.l1ChainID,
		"l2-chain-id", g.l2ChainID, "address", address.Hex())

	go g.TokenRatioLoop()

	return nil
}

func (g *GasPriceOracle) Stop() {
	close(g.stop)
}

func (g *GasPriceOracle) Wait() {
	<-g.stop
}

// ensure makes sure that the configured private key is the operator
// of the `GasPriceOracle`. If it is not the operator, then it will
// not be able to make updates to the token ratio.
func (g *GasPriceOracle) ensure() error {
	operator, err := g.contract.Operator(&bind.CallOpts{
		Context: g.ctx,
	})
	if err != nil {
		return err
	}
	var address common.Address
	if g.config.EnableHsm {
		address = common.HexToAddress(g.config.HsmAddress)
	} else {
		address = crypto.PubkeyToAddress(g.config.PrivateKey.PublicKey)
	}
	if address != operator {
		log.Error("Signing key does not match contract operator", "signer", address.Hex(), "operator", operator.Hex())
		return errInvalidSigningKey
	}
	return nil
}

func (g *GasPriceOracle) TokenRatioLoop() {
	timer := time.NewTicker(time.Duration(g.config.TokenRatioEpochLengthSeconds) * time.Second)
	defer timer.Stop()

	updateTokenRatio, err := wrapUpdateTokenRatio(g.l1Backend, g.l2Backend, g.tokenRatio, g.config, g.awsSigner)
	if err != nil {
		panic(err)
	}
	for {
		select {
		case <-timer.C:
			if err := updateTokenRatio(); err != nil {
				log.Error("cannot update tokenRatio", "message", err)
			}
		case <-g.ctx.Done():
			g.Stop()
		}
	}
}

// NewGasPriceOracle creates a new GasPriceOracle based on a Config
func NewGasPriceOracle(cfg *Config) (*GasPriceOracle, error) {
	if err := cfg.CheckSigner(); err != nil {
		return nil, fmt.Errorf("invalid signer config: %w", err)
	}

	tokenRatioClient, err := NewTokenRatioClient(cfg.EthereumHttpUrl, cfg.LayerTwoHttpUrl, cfg.TokenRatioCexURL, cfg.TokenRatioDexURL,
		cfg.TokenRatioUpdateFrequencySecond)
	if err != nil {
		return nil, err
	}

	// Ensure that we can actually connect to both backends
	log.Info("Connecting to layer two")
	if err := ensureConnection(tokenRatioClient.l2Client); err != nil {
		log.Error("Unable to connect to layer two")
		return nil, err
	}
	log.Info("Connecting to layer one")
	if err := ensureConnection(tokenRatioClient.l1Client); err != nil {
		log.Error("Unable to connect to layer one")
		return nil, err
	}

	address := cfg.GasPriceOracleAddress
	contract, err := bindings.NewGasPriceOracle(address, tokenRatioClient.l2Client)
	if err != nil {
		return nil, err
	}

	// Fetch the current gas price to use as the current price
	currentPrice, err := contract.GasPrice(&bind.CallOpts{
		Context: context.Background(),
	})
	if err != nil {
		return nil, err
	}

	// Create a gas pricer for the gas price updater
	log.Info("Creating GasPricer", "currentPrice", currentPrice)

	l2ChainID, err := tokenRatioClient.l2Client.ChainID(context.Background())
	if err != nil {
		return nil, err
	}
	l1ChainID, err := tokenRatioClient.l1Client.ChainID(context.Background())
	if err != nil {
		return nil, err
	}

	if cfg.L2ChainID != nil {
		if cfg.L2ChainID.Cmp(l2ChainID) != 0 {
			return nil, fmt.Errorf("%w: L2: configured with %d and got %d",
				errWrongChainID, cfg.L2ChainID, l2ChainID)
		}
	} else {
		cfg.L2ChainID = l2ChainID
	}

	if cfg.L1ChainID != nil {
		if cfg.L1ChainID.Cmp(l1ChainID) != 0 {
			return nil, fmt.Errorf("%w: L1: configured with %d and got %d",
				errWrongChainID, cfg.L1ChainID, l1ChainID)
		}
	} else {
		cfg.L1ChainID = l1ChainID
	}

	if !cfg.EnableHsm && cfg.PrivateKey == nil {
		return nil, errNoPrivateKey
	}

	log.Info("Creating GasPriceUpdater")

	if err != nil {
		return nil, err
	}

	gpo := GasPriceOracle{
		l2ChainID:  l2ChainID,
		l1ChainID:  l1ChainID,
		ctx:        context.Background(),
		stop:       make(chan struct{}),
		contract:   contract,
		config:     cfg,
		l2Backend:  tokenRatioClient.l2Client,
		l1Backend:  tokenRatioClient.l1Client,
		tokenRatio: tokenRatioClient.tokenRatio,
	}

	// Initialize the AWS KMS signer synchronously so that configuration, permission
	// and address problems fail startup instead of the background update loop.
	if cfg.useAWSKMS() {
		signer, err := hsm.NewAWSKMSSigner(context.Background(), hsm.AWSKMSConfig{
			KeyARN:          cfg.AWSKMSKeyID,
			ExpectedAddress: cfg.HsmAddress,
			Timeout:         hsm.DefaultAWSKMSTimeout,
			Logger:          log.Root(),
		})
		if err != nil {
			return nil, fmt.Errorf("could not init aws kms signer: %w", err)
		}
		gpo.awsSigner = signer
	}

	if err := gpo.ensure(); err != nil {
		return nil, err
	}

	return &gpo, nil
}

// Ensure that we can actually connect
func ensureConnection(client *ethclient.Client) error {
	t := time.NewTicker(1 * time.Second)
	retries := 0
	defer t.Stop()
	for ; true; <-t.C {
		_, err := client.ChainID(context.Background())
		if err == nil {
			break
		} else {
			retries += 1
			if retries > 90 {
				return err
			}
		}
	}
	return nil
}
