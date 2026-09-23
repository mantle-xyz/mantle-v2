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

// Builder 交易构建器
type Builder struct {
	chainID    *big.Int
	privateKey *ecdsa.PrivateKey
	address    common.Address
}

// NewBuilder 创建交易构建器
func NewBuilder(chainID *big.Int, privateKeyHex string) (*Builder, error) {
	// 解析私钥
	if len(privateKeyHex) > 2 && privateKeyHex[:2] == "0x" {
		privateKeyHex = privateKeyHex[2:]
	}

	privateKey, err := crypto.HexToECDSA(privateKeyHex)
	if err != nil {
		return nil, fmt.Errorf("解析私钥失败: %w", err)
	}

	// 获取地址
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

// Address 返回发送者地址
func (b *Builder) Address() common.Address {
	return b.address
}

// PrivateKey 返回私钥
func (b *Builder) PrivateKey() *ecdsa.PrivateKey {
	return b.privateKey
}

// BuildLegacyTx 构建 Legacy 交易
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

// BuildEIP1559Tx 构建 EIP-1559 交易
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

// BuildEIP7702Tx 构建 EIP-7702 交易
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

// SignSetCodeAuth 签名 EIP-7702 授权
func (b *Builder) SignSetCodeAuth(auth types.SetCodeAuthorization) (types.SetCodeAuthorization, error) {
	return types.SignSetCode(b.privateKey, auth)
}

// SignTx 签名交易
func (b *Builder) SignTx(tx *types.Transaction) (*types.Transaction, error) {
	signer := types.LatestSignerForChainID(b.chainID)
	return types.SignTx(tx, signer, b.privateKey)
}

// SignTxUnprotected 使用 HomesteadSigner 签名交易（v=27/28，无 chain_id 编码）。
//
// 生成的交易没有 EIP-155 重放保护，可在任意链上重放。
// 用于测试 txpool 是否正确拒绝非 EIP-155 legacy 交易。
func (b *Builder) SignTxUnprotected(tx *types.Transaction) (*types.Transaction, error) {
	return types.SignTx(tx, types.HomesteadSigner{}, b.privateKey)
}

// BuildAndSign 构建并签名交易
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

