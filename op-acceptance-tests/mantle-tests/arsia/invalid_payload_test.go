package arsia

import (
	"math/big"
	"strings"
	"testing"

	"github.com/ethereum-optimism/optimism/op-devstack/devtest"
	"github.com/ethereum-optimism/optimism/op-devstack/presets"
	"github.com/ethereum-optimism/optimism/op-service/eth"
	supervisortypes "github.com/ethereum-optimism/optimism/op-supervisor/supervisor/types"
	"github.com/ethereum/go-ethereum/beacon/engine"
	"github.com/ethereum/go-ethereum/common"
	gethtypes "github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/params"
	"github.com/holiman/uint256"
)

const oversizedBlockRLPSize = uint64(params.MaxBlockSize) + 1

func TestRejectsMalformedExtraDataPayload(gt *testing.T) {
	t := devtest.SerialT(gt)
	sys := presets.NewMinimal(t)
	require := t.Require()

	payload := canonicalPayload(t, sys)
	require.Len(payload.ExecutionPayload.ExtraData, 17, "Arsia payload must use Jovian extraData")

	payload.ExecutionPayload.ExtraData = make([]byte, 9)
	rehashPayload(t, payload)

	sys.L2EL.NewPayloadRaw(payload).IsInvalid()
}

func TestRejectsOversizedBlockPayload(gt *testing.T) {
	t := devtest.SerialT(gt)
	sys := presets.NewMinimal(t)
	require := t.Require()

	payload := canonicalPayload(t, sys)
	block := resizePayloadBlock(t, sys.L2Chain.ChainID().ToBig(), payload, oversizedBlockRLPSize)
	require.Equal(oversizedBlockRLPSize, block.Size(), "must test the first byte above the EIP-7934 limit")

	result := sys.L2EL.NewPayloadRaw(payload).IsInvalid()
	require.NotNil(result.Status.ValidationError, "oversized payload must include a validation error")
	validationError := strings.ToLower(*result.Status.ValidationError)
	require.True(
		strings.Contains(validationError, "size") || strings.Contains(validationError, "large"),
		"payload must be rejected for its RLP size, got: %s",
		validationError,
	)
}

func canonicalPayload(t devtest.T, sys *presets.Minimal) *eth.ExecutionPayloadEnvelope {
	sys.L2CL.Advanced(supervisortypes.LocalUnsafe, 2, 30)
	head := sys.L2EL.BlockRefByLabel(eth.Unsafe)
	t.Require().Greater(head.Number, uint64(0), "payload parent must be available to the EL")
	return sys.L2EL.PayloadByNumber(head.Number)
}

func resizePayloadBlock(
	t devtest.T,
	chainID *big.Int,
	payload *eth.ExecutionPayloadEnvelope,
	targetSize uint64,
) *gethtypes.Block {
	require := t.Require()
	privateKey, err := crypto.HexToECDSA("ac0974bec39a17e36ba4a6b4d238ff944bacb478cbed5efcae784d7bf4f2ff80")
	require.NoError(err)

	originalTransactions := append([]eth.Data(nil), payload.ExecutionPayload.Transactions...)
	dataLen := int(targetSize) - 1_024
	seenLengths := make(map[int]struct{})

	for range 64 {
		require.Greater(dataLen, 0, "calculated transaction data length must be positive")
		if _, seen := seenLengths[dataLen]; seen {
			break
		}
		seenLengths[dataLen] = struct{}{}

		to := common.Address{0x01}
		tx := gethtypes.MustSignNewTx(
			privateKey,
			gethtypes.LatestSignerForChainID(chainID),
			&gethtypes.DynamicFeeTx{
				ChainID:   chainID,
				Nonce:     0,
				GasTipCap: big.NewInt(1),
				GasFeeCap: big.NewInt(1_000_000_000),
				Gas:       uint64(payload.ExecutionPayload.GasLimit),
				To:        &to,
				Data:      make([]byte, dataLen),
			},
		)
		rawTx, err := tx.MarshalBinary()
		require.NoError(err)

		payload.ExecutionPayload.Transactions = append(
			append([]eth.Data(nil), originalTransactions...),
			rawTx,
		)
		rehashPayload(t, payload)
		block := blockFromPayload(t, payload)
		if block.Size() == targetSize {
			return block
		}

		dataLen += int(int64(targetSize) - int64(block.Size()))
	}

	require.FailNow("failed to construct exact-size payload", "target RLP size: %d", targetSize)
	return nil
}

func rehashPayload(t devtest.T, payload *eth.ExecutionPayloadEnvelope) {
	newHash, matches := payload.CheckBlockHash()
	t.Require().False(matches, "mutated payload must change the block hash")
	payload.ExecutionPayload.BlockHash = newHash
	_, matches = payload.CheckBlockHash()
	t.Require().True(matches, "mutated payload block hash must be self-consistent")
}

func blockFromPayload(t devtest.T, payload *eth.ExecutionPayloadEnvelope) *gethtypes.Block {
	p := payload.ExecutionPayload
	txs := make([][]byte, len(p.Transactions))
	for i := range p.Transactions {
		txs[i] = p.Transactions[i]
	}

	var withdrawals []*gethtypes.Withdrawal
	if p.Withdrawals != nil {
		withdrawals = []*gethtypes.Withdrawal(*p.Withdrawals)
	}

	block, err := engine.ExecutableDataToBlock(engine.ExecutableData{
		ParentHash:      p.ParentHash,
		FeeRecipient:    p.FeeRecipient,
		StateRoot:       common.Hash(p.StateRoot),
		ReceiptsRoot:    common.Hash(p.ReceiptsRoot),
		LogsBloom:       p.LogsBloom[:],
		Random:          common.Hash(p.PrevRandao),
		Number:          uint64(p.BlockNumber),
		GasLimit:        uint64(p.GasLimit),
		GasUsed:         uint64(p.GasUsed),
		Timestamp:       uint64(p.Timestamp),
		ExtraData:       p.ExtraData,
		BaseFeePerGas:   (*uint256.Int)(&p.BaseFeePerGas).ToBig(),
		BlockHash:       p.BlockHash,
		Transactions:    txs,
		Withdrawals:     withdrawals,
		ExcessBlobGas:   (*uint64)(p.ExcessBlobGas),
		WithdrawalsRoot: p.WithdrawalsRoot,
		BlobGasUsed:     (*uint64)(p.BlobGasUsed),
	}, nil, payload.ParentBeaconBlockRoot, [][]byte{}, gethtypes.MantleSkadiBlockConfig)
	t.Require().NoError(err)
	return block
}
