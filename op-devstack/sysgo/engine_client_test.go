package sysgo

import (
	"context"
	"encoding/json"
	"errors"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/beacon/engine"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/rpc"
	"github.com/stretchr/testify/require"
)

type retryPayloadEngine struct {
	firstError  error
	firstStatus string
	called      bool
	payloads    chan map[string]json.RawMessage
}

func (s *retryPayloadEngine) NewPayloadV5(_ context.Context, payload map[string]json.RawMessage, _ []common.Hash, _ *common.Hash, _ []hexutil.Bytes) (engine.PayloadStatusV1, error) {
	s.payloads <- payload
	if !s.called {
		s.called = true
		return engine.PayloadStatusV1{Status: s.firstStatus}, s.firstError
	}
	return engine.PayloadStatusV1{Status: engine.VALID}, nil
}

func TestEngineClientNewPayloadV5Retry(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status string
		err    error
	}{
		{name: "after_rpc_error", err: errors.New("temporary engine failure")},
		{name: "after_syncing", status: engine.SYNCING},
		// A later Seal step (blob storage or forkchoice update) may fail even
		// after newPayload returned VALID, requiring the same payload again.
		{name: "after_valid", status: engine.VALID},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := rpc.NewServer()
			t.Cleanup(server.Stop)
			backend := &retryPayloadEngine{
				firstError: tc.err, firstStatus: tc.status,
				payloads: make(chan map[string]json.RawMessage, 2),
			}
			require.NoError(t, server.RegisterName("engine", backend))
			client := rpc.DialInProc(server)
			t.Cleanup(client.Close)
			e := &engineClient{inner: client, blockAccessLists: make(map[common.Hash]hexutil.Bytes)}

			data := engine.ExecutableData{BlockHash: common.HexToHash("0x1234"), BaseFeePerGas: big.NewInt(1)}
			bal := hexutil.Bytes{0xc0}
			e.putBlockAccessList(data.BlockHash, bal)
			root := common.Hash{}
			status, err := e.NewPayloadV5(context.Background(), data, []common.Hash{}, &root, []hexutil.Bytes{})
			if tc.err != nil {
				require.EqualError(t, err, tc.err.Error())
			} else {
				require.NoError(t, err)
				require.Equal(t, tc.status, status.Status)
			}
			first := <-backend.payloads

			status, err = e.NewPayloadV5(context.Background(), data, []common.Hash{}, &root, []hexutil.Bytes{})
			require.NoError(t, err, "retry must reach the engine with the original BAL")
			require.Equal(t, engine.VALID, status.Status)
			second := <-backend.payloads
			require.Equal(t, first, second, "retry must send the same payload")
			var receivedBAL hexutil.Bytes
			require.NoError(t, json.Unmarshal(second["blockAccessList"], &receivedBAL))
			require.Equal(t, bal, receivedBAL)
		})
	}
}

func TestEngineClientBlockAccessListCacheBounded(t *testing.T) {
	e := &engineClient{blockAccessLists: make(map[common.Hash]hexutil.Bytes)}
	bal := hexutil.Bytes{0xc0}
	hashAt := func(i int) common.Hash { return common.BigToHash(big.NewInt(int64(i))) }

	for i := 0; i < maxBlockAccessLists; i++ {
		e.putBlockAccessList(hashAt(i), bal)
	}
	// Repeatedly retrieving and submitting the same payload must not grow
	// the cache or evict other blocks still awaiting submission.
	for i := 0; i < 2*maxBlockAccessLists; i++ {
		e.putBlockAccessList(hashAt(0), bal)
		_, err := e.newExecutableDataV5(engine.ExecutableData{BlockHash: hashAt(0)})
		require.NoError(t, err)
	}
	require.Len(t, e.blockAccessLists, maxBlockAccessLists)
	require.Len(t, e.balOrder, maxBlockAccessLists)
	for i := 0; i < maxBlockAccessLists; i++ {
		_, err := e.newExecutableDataV5(engine.ExecutableData{BlockHash: hashAt(i)})
		require.NoError(t, err, "all retained payloads must remain available for submission")
	}

	// New blocks must still evict the oldest entries, whether or not they
	// were submitted, so discarded builds cannot accumulate indefinitely.
	for i := maxBlockAccessLists; i < 2*maxBlockAccessLists; i++ {
		e.putBlockAccessList(hashAt(i), bal)
		require.Len(t, e.blockAccessLists, maxBlockAccessLists)
		require.Len(t, e.balOrder, maxBlockAccessLists)
		_, err := e.newExecutableDataV5(engine.ExecutableData{BlockHash: hashAt(i - maxBlockAccessLists)})
		require.ErrorContains(t, err, "missing blockAccessList")
		_, err = e.newExecutableDataV5(engine.ExecutableData{BlockHash: hashAt(i)})
		require.NoError(t, err)
	}
}
