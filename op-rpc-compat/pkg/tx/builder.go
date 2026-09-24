package tx

import (
	"crypto/ecdsa"
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/holiman/uint256"
)

// Builder constructs and signs transactions for a chain.
type Builder struct {
	chainID    *big.Int
	privateKey *ecdsa.PrivateKey
	address    common.Address
}

// NewBuilder creates a builder from a hex-encoded private key.
func NewBuilder(chainID *big.Int, privateKeyHex string) (*Builder, error) {
	if len(privateKeyHex) > 2 && privateKeyHex[:2] == "0x" {
		privateKeyHex = privateKeyHex[2:]
	}

	privateKey, err := crypto.HexToECDSA(privateKeyHex)
	if err != nil {
		return nil, fmt.Errorf("解析私钥失败: %w", err)
	}

	publicKey := privateKey.Public()
	publicKeyECDSA, ok := publicKey.(*ecdsa.PublicKey)
	if !ok {
		return nil, fmt.Errorf("无法获取公钥")
	}
	address := crypto.PubkeyToAddress(*publicKeyECDSA)

	return &Builder{
		chainID:    chainID,
		privateKey: privateKey,
		address:    address,
	}, nil
}

// Address returns the sender address.
func (b *Builder) Address() common.Address {
	return b.address
}

// PrivateKey returns the signing key.
func (b *Builder) PrivateKey() *ecdsa.PrivateKey {
	return b.privateKey
}

// BuildLegacyTx constructs a legacy transaction.
func (b *Builder) BuildLegacyTx(params *TxParams) (*types.Transaction, error) {
	tx := types.NewTx(&types.LegacyTx{
		Nonce:    params.Nonce,
		GasPrice: params.GasPrice,
		Gas:      params.Gas,
		To:       params.To,
		Value:    params.Value,
		Data:     params.Data,
	})
	return tx, nil
}

// BuildEIP1559Tx constructs an EIP-1559 transaction.
func (b *Builder) BuildEIP1559Tx(params *TxParams) (*types.Transaction, error) {
	tx := types.NewTx(&types.DynamicFeeTx{
		ChainID:   b.chainID,
		Nonce:     params.Nonce,
		GasTipCap: params.MaxPriorityFeePerGas,
		GasFeeCap: params.MaxFeePerGas,
		Gas:       params.Gas,
		To:        params.To,
		Value:     params.Value,
		Data:      params.Data,
	})
	return tx, nil
}

// BuildEIP7702Tx constructs an EIP-7702 transaction.
func (b *Builder) BuildEIP7702Tx(params *TxParams) (*types.Transaction, error) {
	if params.To == nil {
		return nil, fmt.Errorf("EIP-7702 交易需要指定接收地址")
	}

	tx := types.NewTx(&types.SetCodeTx{
		ChainID:   uint256.MustFromBig(b.chainID),
		Nonce:     params.Nonce,
		GasTipCap: uint256.MustFromBig(params.MaxPriorityFeePerGas),
		GasFeeCap: uint256.MustFromBig(params.MaxFeePerGas),
		Gas:       params.Gas,
		To:        *params.To,
		Value:     uint256.MustFromBig(params.Value),
		Data:      params.Data,
		AuthList:  params.AuthList,
	})
	return tx, nil
}

// SignSetCodeAuth signs an EIP-7702 authorization.
func (b *Builder) SignSetCodeAuth(auth types.SetCodeAuthorization) (types.SetCodeAuthorization, error) {
	return types.SignSetCode(b.privateKey, auth)
}

// SignTx signs a transaction for the configured chain.
func (b *Builder) SignTx(tx *types.Transaction) (*types.Transaction, error) {
	signer := types.LatestSignerForChainID(b.chainID)
	return types.SignTx(tx, signer, b.privateKey)
}

// SignTxUnprotected signs with HomesteadSigner (v=27/28, without a chain ID).
//
// The result has no EIP-155 replay protection and can be replayed on any chain.
// It is used to test rejection of unprotected legacy transactions by the txpool.
func (b *Builder) SignTxUnprotected(tx *types.Transaction) (*types.Transaction, error) {
	return types.SignTx(tx, types.HomesteadSigner{}, b.privateKey)
}

// BuildAndSign constructs a transaction of the requested type and signs it.
func (b *Builder) BuildAndSign(txType TxType, params *TxParams) (*types.Transaction, error) {
	var tx *types.Transaction
	var err error

	switch txType {
	case TxTypeLegacy:
		tx, err = b.BuildLegacyTx(params)
	case TxTypeEIP1559:
		tx, err = b.BuildEIP1559Tx(params)
	case TxTypeEIP7702:
		tx, err = b.BuildEIP7702Tx(params)
	default:
		return nil, fmt.Errorf("不支持的交易类型: %v", txType)
	}

	if err != nil {
		return nil, err
	}

	return b.SignTx(tx)
}
