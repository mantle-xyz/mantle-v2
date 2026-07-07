package sysgo

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"sync"

	"github.com/ethereum-optimism/optimism/op-e2e/e2eutils/geth"
	"github.com/ethereum/go-ethereum/beacon/engine"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/types"
	gn "github.com/ethereum/go-ethereum/node"
	"github.com/ethereum/go-ethereum/rpc"
	gethrpc "github.com/ethereum/go-ethereum/rpc"
)

type engineClient struct {
	inner            *rpc.Client
	mu               sync.Mutex
	blockAccessLists map[common.Hash]hexutil.Bytes
}

func dialEngine(ctx context.Context, endpoint string, jwtSecret [32]byte) (*engineClient, error) {
	engineCl, err := gethrpc.DialOptions(ctx, endpoint, rpc.WithHTTPAuth(gn.NewJWTAuth(jwtSecret)))
	if err != nil {
		return nil, err
	}
	return &engineClient{
		inner:            engineCl,
		blockAccessLists: make(map[common.Hash]hexutil.Bytes),
	}, nil
}

var _ geth.EngineAPI = (*engineClient)(nil)

func (e *engineClient) forkchoiceUpdated(fs engine.ForkchoiceStateV1, pa *engine.PayloadAttributes, method string) (engine.ForkChoiceResponse, error) {
	var x engine.ForkChoiceResponse
	if err := e.inner.CallContext(context.Background(), &x, method, fs, pa); err != nil {
		return engine.ForkChoiceResponse{}, err
	}
	return x, nil
}

func (e *engineClient) ForkchoiceUpdatedV2(_ context.Context, fs engine.ForkchoiceStateV1, pa *engine.PayloadAttributes) (engine.ForkChoiceResponse, error) {
	return e.forkchoiceUpdated(fs, pa, "engine_forkchoiceUpdatedV2")
}

func (e *engineClient) ForkchoiceUpdatedV3(_ context.Context, fs engine.ForkchoiceStateV1, pa *engine.PayloadAttributes) (engine.ForkChoiceResponse, error) {
	return e.forkchoiceUpdated(fs, pa, "engine_forkchoiceUpdatedV3")
}

func (e *engineClient) ForkchoiceUpdatedV4(_ context.Context, fs engine.ForkchoiceStateV1, pa *engine.PayloadAttributes) (engine.ForkChoiceResponse, error) {
	if pa == nil {
		return e.forkchoiceUpdated(fs, nil, "engine_forkchoiceUpdatedV4")
	}
	var x engine.ForkChoiceResponse
	if err := e.inner.CallContext(context.Background(), &x, "engine_forkchoiceUpdatedV4", fs, newPayloadAttributesV4(pa)); err != nil {
		return engine.ForkChoiceResponse{}, err
	}
	return x, nil
}

type payloadAttributesV4 struct {
	Timestamp             hexutil.Uint64      `json:"timestamp"`
	Random                common.Hash         `json:"prevRandao"`
	SuggestedFeeRecipient common.Address      `json:"suggestedFeeRecipient"`
	Withdrawals           []*types.Withdrawal `json:"withdrawals"`
	BeaconRoot            *common.Hash        `json:"parentBeaconBlockRoot"`
	SlotNumber            *hexutil.Uint64     `json:"slotNumber"`
	TargetGasLimit        *hexutil.Uint64     `json:"targetGasLimit"`
}

func newPayloadAttributesV4(pa *engine.PayloadAttributes) *payloadAttributesV4 {
	var slotNumber *hexutil.Uint64
	if pa.SlotNumber != nil {
		slotNumber = (*hexutil.Uint64)(pa.SlotNumber)
	}
	var targetGasLimit *hexutil.Uint64
	if pa.GasLimit != nil {
		targetGasLimit = (*hexutil.Uint64)(pa.GasLimit)
	}
	return &payloadAttributesV4{
		Timestamp:             hexutil.Uint64(pa.Timestamp),
		Random:                pa.Random,
		SuggestedFeeRecipient: pa.SuggestedFeeRecipient,
		Withdrawals:           pa.Withdrawals,
		BeaconRoot:            pa.BeaconRoot,
		SlotNumber:            slotNumber,
		TargetGasLimit:        targetGasLimit,
	}
}

func (e *engineClient) getPayload(id engine.PayloadID, method string) (*engine.ExecutionPayloadEnvelope, error) {
	var result engine.ExecutionPayloadEnvelope
	if err := e.inner.CallContext(context.Background(), &result, method, id); err != nil {
		return nil, err
	}
	return &result, nil
}

func (e *engineClient) GetPayloadV2(id engine.PayloadID) (*engine.ExecutionPayloadEnvelope, error) {
	return e.getPayload(id, "engine_getPayloadV2")
}

func (e *engineClient) GetPayloadV3(id engine.PayloadID) (*engine.ExecutionPayloadEnvelope, error) {
	return e.getPayload(id, "engine_getPayloadV3")
}

func (e *engineClient) GetPayloadV4(id engine.PayloadID) (*engine.ExecutionPayloadEnvelope, error) {
	return e.getPayload(id, "engine_getPayloadV4")
}

func (e *engineClient) GetPayloadV5(id engine.PayloadID) (*engine.ExecutionPayloadEnvelope, error) {
	return e.getPayload(id, "engine_getPayloadV5")
}

func (e *engineClient) GetPayloadV6(id engine.PayloadID) (*engine.ExecutionPayloadEnvelope, error) {
	var result executionPayloadEnvelopeV5
	if err := e.inner.CallContext(context.Background(), &result, "engine_getPayloadV6", id); err != nil {
		return nil, err
	}
	envelope, blockHash, blockAccessList, err := result.toEngine()
	if err != nil {
		return nil, err
	}
	e.mu.Lock()
	e.blockAccessLists[blockHash] = blockAccessList
	e.mu.Unlock()
	return envelope, nil
}

type executionPayloadEnvelopeV5 struct {
	ExecutionPayload      json.RawMessage     `json:"executionPayload"`
	BlockValue            *hexutil.Big        `json:"blockValue"`
	BlobsBundle           *engine.BlobsBundle `json:"blobsBundle"`
	Requests              []hexutil.Bytes     `json:"executionRequests"`
	Override              bool                `json:"shouldOverrideBuilder"`
	Witness               *hexutil.Bytes      `json:"witness,omitempty"`
	ParentBeaconBlockRoot *common.Hash        `json:"parentBeaconBlockRoot,omitempty"`
}

func (e *executionPayloadEnvelopeV5) toEngine() (*engine.ExecutionPayloadEnvelope, common.Hash, hexutil.Bytes, error) {
	var payload engine.ExecutableData
	if err := json.Unmarshal(e.ExecutionPayload, &payload); err != nil {
		return nil, common.Hash{}, nil, err
	}
	var rawPayload map[string]json.RawMessage
	if err := json.Unmarshal(e.ExecutionPayload, &rawPayload); err != nil {
		return nil, common.Hash{}, nil, err
	}
	blockAccessListJSON, ok := rawPayload["blockAccessList"]
	if !ok {
		return nil, common.Hash{}, nil, fmt.Errorf("payload %s missing blockAccessList", payload.BlockHash)
	}
	var amsterdamFields struct {
		BlockHash       common.Hash   `json:"blockHash"`
		BlockAccessList hexutil.Bytes `json:"blockAccessList"`
	}
	if err := json.Unmarshal(blockAccessListJSON, &amsterdamFields.BlockAccessList); err != nil {
		return nil, common.Hash{}, nil, fmt.Errorf("decode blockAccessList: %w", err)
	}
	if err := json.Unmarshal(e.ExecutionPayload, &amsterdamFields); err != nil {
		return nil, common.Hash{}, nil, err
	}
	requests := make([][]byte, len(e.Requests))
	for i, req := range e.Requests {
		requests[i] = req
	}
	var blockValue *big.Int
	if e.BlockValue != nil {
		blockValue = (*big.Int)(e.BlockValue)
	}
	return &engine.ExecutionPayloadEnvelope{
		ExecutionPayload:      &payload,
		BlockValue:            blockValue,
		BlobsBundle:           e.BlobsBundle,
		Requests:              requests,
		Override:              e.Override,
		Witness:               e.Witness,
		ParentBeaconBlockRoot: e.ParentBeaconBlockRoot,
	}, amsterdamFields.BlockHash, amsterdamFields.BlockAccessList, nil
}

func (e *engineClient) NewPayloadV2(ctx context.Context, data engine.ExecutableData) (engine.PayloadStatusV1, error) {
	var result engine.PayloadStatusV1
	if err := e.inner.CallContext(ctx, &result, "engine_newPayloadV2", data); err != nil {
		return engine.PayloadStatusV1{}, err
	}
	return result, nil
}

func (e *engineClient) NewPayloadV3(ctx context.Context, data engine.ExecutableData, versionedHashes []common.Hash, beaconRoot *common.Hash) (engine.PayloadStatusV1, error) {
	var result engine.PayloadStatusV1
	if err := e.inner.CallContext(ctx, &result, "engine_newPayloadV3", data, versionedHashes, beaconRoot); err != nil {
		return engine.PayloadStatusV1{}, err
	}
	return result, nil
}

func (e *engineClient) NewPayloadV4(ctx context.Context, data engine.ExecutableData, versionedHashes []common.Hash, beaconRoot *common.Hash, executionRequests []hexutil.Bytes) (engine.PayloadStatusV1, error) {
	var result engine.PayloadStatusV1
	if err := e.inner.CallContext(ctx, &result, "engine_newPayloadV4", data, versionedHashes, beaconRoot, executionRequests); err != nil {
		return engine.PayloadStatusV1{}, err
	}
	return result, nil
}

func (e *engineClient) NewPayloadV5(ctx context.Context, data engine.ExecutableData, versionedHashes []common.Hash, beaconRoot *common.Hash, executionRequests []hexutil.Bytes) (engine.PayloadStatusV1, error) {
	var result engine.PayloadStatusV1
	payload, err := e.newExecutableDataV5(data)
	if err != nil {
		return engine.PayloadStatusV1{}, err
	}
	if err := e.inner.CallContext(ctx, &result, "engine_newPayloadV5", payload, versionedHashes, beaconRoot, executionRequests); err != nil {
		return engine.PayloadStatusV1{}, err
	}
	return result, nil
}

func (e *engineClient) newExecutableDataV5(data engine.ExecutableData) (map[string]json.RawMessage, error) {
	payloadJSON, err := json.Marshal(data)
	if err != nil {
		return nil, err
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(payloadJSON, &payload); err != nil {
		return nil, err
	}
	e.mu.Lock()
	blockAccessList, ok := e.blockAccessLists[data.BlockHash]
	e.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("missing blockAccessList for payload %s", data.BlockHash)
	}
	blockAccessListJSON, err := json.Marshal(blockAccessList)
	if err != nil {
		return nil, err
	}
	payload["blockAccessList"] = blockAccessListJSON
	return payload, nil
}
