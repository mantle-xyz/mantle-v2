package oracle

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"

	kms "cloud.google.com/go/kms/apiv1"
	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/log"
	"google.golang.org/api/option"

	"github.com/ethereum-optimism/optimism/gas-oracle/bindings"
	ometrics "github.com/ethereum-optimism/optimism/gas-oracle/metrics"
	"github.com/ethereum-optimism/optimism/gas-oracle/tokenratio"
	"github.com/ethereum-optimism/optimism/op-service/hsm"
)

func wrapUpdateTokenRatio(l1Backend bind.ContractTransactor, l2Backend DeployContractBackend, tokenRatio *tokenratio.Client, cfg *Config, awsSigner *hsm.AWSKMSSigner) (func() error, error) {
	if cfg.L2ChainID == nil {
		return nil, errNoChainID
	}

	opts, err := newTransactOpts(cfg, awsSigner)
	if err != nil {
		return nil, err
	}
	return wrapUpdateTokenRatioWithOpts(l2Backend, tokenRatio, cfg, opts)
}

// newTransactOpts builds the signing options for the configured backend.
func newTransactOpts(cfg *Config, awsSigner *hsm.AWSKMSSigner) (*bind.TransactOpts, error) {
	if cfg.L2ChainID == nil {
		return nil, errNoChainID
	}

	var opts *bind.TransactOpts
	var err error
	if cfg.useAWSKMS() {
		// Never fall through to the GCP branch below when aws-kms is selected.
		if awsSigner == nil {
			return nil, errors.New("aws kms signer is not initialized")
		}
		opts = &bind.TransactOpts{
			From:    awsSigner.Address(),
			Signer:  awsSigner.BindSignerFn(cfg.L2ChainID, hsm.DefaultAWSKMSTimeout),
			Context: context.Background(),
		}
	} else if !cfg.EnableHsm {
		if cfg.PrivateKey == nil {
			return nil, errNoPrivateKey
		}
		if cfg.L2ChainID == nil {
			return nil, errNoChainID
		}

		opts, err = bind.NewKeyedTransactorWithChainID(cfg.PrivateKey, cfg.L2ChainID)
		if err != nil {
			return nil, err
		}
	} else {
		seqBytes, err := hex.DecodeString(cfg.HsmCreden)
		if err != nil {
			log.Warn("gasoracle", "decode hsm creden fail", err.Error())
			return nil, err
		}
		apikey := option.WithCredentialsJSON(seqBytes)
		client, err := kms.NewKeyManagementClient(context.Background(), apikey)
		if err != nil {
			log.Warn("gasoracle", "create signer error", err.Error())
			return nil, err
		}
		mk := &hsm.ManagedKey{
			KeyName:      cfg.HsmAPIName,
			EthereumAddr: common.HexToAddress(cfg.HsmAddress),
			Gclient:      client,
		}
		opts, err = mk.NewEthereumTransactorWithChainID(context.Background(), cfg.L2ChainID)
		if err != nil {
			log.Warn("gasoracle", "create signer error", err.Error())
			return nil, err
		}
	}
	// Once https://github.com/ethereum/go-ethereum/pull/23062 is released
	// then we can remove setting the context here
	if opts.Context == nil {
		opts.Context = context.Background()
	}
	// Don't send the transaction using the `contract` so that we can inspect
	// it beforehand
	opts.NoSend = true
	return opts, nil
}

func wrapUpdateTokenRatioWithOpts(l2Backend DeployContractBackend, tokenRatio *tokenratio.Client, cfg *Config, opts *bind.TransactOpts) (func() error, error) {
	// Create a new contract bindings in scope of the updateL2GasPriceFn
	// that is returned from this function
	contract, err := bindings.NewGasPriceOracle(cfg.GasPriceOracleAddress, l2Backend)
	if err != nil {
		return nil, err
	}

	// initialize some metrics
	// initialize fee scalar from contract
	feeScalar, err := contract.Scalar(&bind.CallOpts{
		Context: context.Background(),
	})
	if err != nil {
		return nil, err
	}
	ometrics.UpdateFeeScalar(feeScalar.Int64())

	return func() error {
		lastTokenRatio, err := contract.TokenRatio(&bind.CallOpts{
			Context: context.Background(),
		})
		if err != nil {
			return err
		}
		l1BaseFee, err := contract.L1BaseFee(&bind.CallOpts{
			Context: context.Background(),
		})
		if err != nil {
			return err
		}
		feeScalar, err := contract.Scalar(&bind.CallOpts{
			Context: context.Background(),
		})
		if err != nil {
			return err
		}
		// Update fee scalar & l1 base fee metrics
		ometrics.UpdateFeeScalar(feeScalar.Int64())
		ometrics.UpdateL1BaseFee(l1BaseFee.Int64())

		// NOTE this will return base multiple with coin ratio
		latestRatio := tokenRatio.TokenRatio() * cfg.TokenRatioScalar
		ometrics.UpdateTokenRatio(tokenRatio.TokenRatio())
		ometrics.UpdateTokenRatioWithScalar(latestRatio)
		if !isDifferenceSignificant(lastTokenRatio.Uint64(), uint64(latestRatio), cfg.TokenRatioSignificanceFactor) {
			log.Warn("non significant tokenRatio update", "former", lastTokenRatio, "current", latestRatio)
			return nil
		}

		// Use the configured gas price if it is set,
		// otherwise use gas estimation
		if cfg.GasPrice != nil {
			opts.GasPrice = cfg.GasPrice
		} else {
			gasPrice, err := l2Backend.SuggestGasPrice(opts.Context)
			if err != nil {
				return err
			}
			opts.GasPrice = gasPrice
		}
		// set L1BaseFee to base fee + tip cap, to cover rollup tip cap
		tx, err := contract.SetTokenRatio(opts, big.NewInt(int64(latestRatio)))
		if err != nil {
			return err
		}
		log.Info("updating tokenRatio", "tx.gasPrice", tx.GasPrice(), "tx.gasLimit", tx.Gas(),
			"tx.data", hexutil.Encode(tx.Data()), "tx.to", tx.To().Hex(), "tx.nonce", tx.Nonce())
		if err := l2Backend.SendTransaction(context.Background(), tx); err != nil {
			return fmt.Errorf("cannot update tokenRatio: %w", err)
		}
		log.Info("TokenRatio transaction already sent", "hash", tx.Hash().Hex(), "tokenRatio", int64(latestRatio))
		ometrics.UpdateTokenRatioOnchain(latestRatio)

		if cfg.WaitForReceipt {
			// Wait for the receipt
			receipt, err := waitForReceipt(l2Backend, tx)
			if err != nil {
				return err
			}

			log.Info("TokenRatio transaction confirmed", "hash", tx.Hash().Hex(),
				"gasUsed", receipt.GasUsed, "blockNumber", receipt.BlockNumber)
		}
		return nil
	}, nil
}
