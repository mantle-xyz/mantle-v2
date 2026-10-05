package tx

import (
	"context"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ethereum-optimism/optimism/op-rpc-compat/pkg/diff"
	"github.com/ethereum-optimism/optimism/op-rpc-compat/pkg/report"
	"github.com/ethereum-optimism/optimism/op-rpc-compat/pkg/rpc"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
)

func TestTxpoolRejectionNamesLogicalClients(t *testing.T) {
	serve := func(accept bool) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var request struct {
				ID     int    `json:"id"`
				Method string `json:"method"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Errorf("decode request: %v", err)
				return
			}
			response := map[string]any{"jsonrpc": "2.0", "id": request.ID}
			switch request.Method {
			case "eth_chainId":
				response["result"] = "0x1388"
			case "eth_getTransactionCount":
				response["result"] = "0x0"
			case "eth_sendRawTransaction":
				if accept {
					response["result"] = "0x1"
				} else {
					response["error"] = map[string]any{"code": -32000, "message": "nonce too low"}
				}
			default:
				t.Errorf("unexpected method %q", request.Method)
			}
			w.Header().Set("Content-Type", "application/json")
			if err := json.NewEncoder(w).Encode(response); err != nil {
				t.Errorf("encode response: %v", err)
			}
		}))
	}
	baseline := serve(true)
	defer baseline.Close()
	target := serve(false)
	defer target.Close()
	pair := rpc.NewClientPair(baseline.URL, "old-reth", target.URL, "new-reth", time.Second)
	const privateKey = "0xac0974bec39a17e36ba4a6b4d238ff944bacb478cbed5efcae784d7bf4f2ff80"
	tester, err := NewTester(pair, privateKey, nil)
	if err != nil {
		t.Fatal(err)
	}
	result := tester.TestTxpoolRejection(t.Context(), "rejection", func(nonce uint64) (*types.Transaction, error) {
		to := common.Address{}
		return types.NewTx(&types.LegacyTx{Nonce: nonce, Gas: 21_000, GasPrice: big.NewInt(1), To: &to, Value: big.NewInt(0)}), nil
	}, "nonce too low")
	if !strings.Contains(result.Error, "old-reth") || strings.Contains(result.Error, "geth should") {
		t.Fatalf("rejection error = %q", result.Error)
	}
}

func TestNewTesterKeepsLogicalClientNames(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request rpc.Request
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": "0x1388"})
	}))
	defer server.Close()
	pair := rpc.NewClientPair(server.URL, "old-reth", server.URL, "new-reth", time.Second)
	const privateKey = "0xac0974bec39a17e36ba4a6b4d238ff944bacb478cbed5efcae784d7bf4f2ff80"
	tester, err := NewTester(pair, privateKey, nil)
	if err != nil {
		t.Fatal(err)
	}
	if tester.BaselineClient().Name() != "old-reth" || tester.TargetClient().Name() != "new-reth" {
		t.Fatalf("tester client names = %q, %q", tester.BaselineClient().Name(), tester.TargetClient().Name())
	}
}

func TestIsAlreadyKnownError(t *testing.T) {
	if !isAlreadyKnownError(errors.New("RPC error: code=-32000, message=already known")) {
		t.Fatal("expected already-known RPC error to be recognized")
	}
	if isAlreadyKnownError(errors.New("RPC error: code=-32000, message=nonce too low")) {
		t.Fatal("nonce-too-low error must not be recognized as already known")
	}
	if isAlreadyKnownError(nil) {
		t.Fatal("nil error must not be recognized as already known")
	}
}

func TestComparisonRecordsTargetTransportFailure(t *testing.T) {
	baseline := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			ID     int    `json:"id"`
			Method string `json:"method"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		result := any("0x1")
		if request.Method == "eth_getTransactionReceipt" {
			result = map[string]any{"status": "0x1", "gasUsed": "0x5208", "blockNumber": "0x1"}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": result})
	}))
	defer baseline.Close()
	closedTarget := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	closedTargetURL := closedTarget.URL
	closedTarget.Close()

	for _, tc := range []struct {
		name string
		run  func(*Tester) error
	}{
		{name: "balance", run: func(tester *Tester) error {
			_, err := tester.GetBalanceAndCompare(context.Background(), common.Address{}, "0x1", "balance")
			return err
		}},
		{name: "receipt", run: func(tester *Tester) error {
			_, err := tester.CompareReceipts(context.Background(), "receipt", common.Hash{}, common.Hash{})
			return err
		}},
		{name: "receipt_poll", run: func(tester *Tester) error {
			_, err := tester.WaitForReceiptAndCompare(context.Background(), common.Hash{}, time.Second, "receipt_poll")
			return err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pair := rpc.NewClientPair(baseline.URL, "baseline", closedTargetURL, "target", time.Second)
			reporter := report.NewReporter(report.EndpointMetadata{}, report.EndpointMetadata{}, false)
			tester := &Tester{baselineClient: pair.Baseline, targetClient: pair.Target, reporter: reporter}
			_ = tc.run(tester)
			if !reporter.HasFailures() {
				t.Fatal("transport failure was not recorded")
			}
		})
	}
}

func TestReceiptNullAfterGracePeriodFails(t *testing.T) {
	serve := func(receipt any) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var request rpc.Request
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Errorf("decode request: %v", err)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": receipt})
		}))
	}
	baseline := serve(map[string]any{"status": "0x1", "gasUsed": "0x5208", "blockNumber": "0x1"})
	defer baseline.Close()
	target := serve(nil)
	defer target.Close()
	pair := rpc.NewClientPair(baseline.URL, "baseline", target.URL, "target", time.Second)
	reporter := report.NewReporter(report.EndpointMetadata{}, report.EndpointMetadata{}, false)
	tester := &Tester{baselineClient: pair.Baseline, targetClient: pair.Target, reporter: reporter}
	if _, err := tester.WaitForReceiptAndCompare(t.Context(), common.Hash{}, time.Second, "null_target"); err != nil {
		t.Fatal(err)
	}
	result := reporter.Generate().Results[0]
	if result.Status != report.StatusFail || !reporter.HasFailures() || len(result.Differences) != 1 ||
		result.Differences[0].Type != diff.DiffTypeNull || result.Differences[0].Severity != diff.SeverityFail {
		t.Fatalf("null receipt result = %+v", result)
	}
}

func TestCallContractInvalidTargetResponseIsReported(t *testing.T) {
	baseline := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request rpc.Request
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		var result any = "0x"
		if request.Method == "eth_getBlockByNumber" {
			result = map[string]any{"hash": "0x" + strings.Repeat("11", 32)}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": result})
	}))
	defer baseline.Close()
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request rpc.Request
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		if request.Method == "eth_getBlockByNumber" {
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID,
				"result": map[string]any{"hash": "0x" + strings.Repeat("11", 32)}})
			return
		}
		_, _ = w.Write([]byte(`{"jsonrpc":`))
	}))
	defer target.Close()
	pair := rpc.NewClientPair(baseline.URL, "baseline", target.URL, "target", time.Second)
	reporter := report.NewReporter(report.EndpointMetadata{}, report.EndpointMetadata{}, false)
	tester := &Tester{baselineClient: pair.Baseline, targetClient: pair.Target, reporter: reporter}
	_, err := tester.CallContract(t.Context(), common.Address{}, common.Address{}, nil, "invalid_target")
	if err == nil || !reporter.HasFailures() {
		t.Fatalf("invalid target response error = %v, report = %+v", err, reporter.Generate())
	}
	if got := reporter.Generate().Results[0]; got.Status != report.StatusFail {
		t.Fatalf("contract call result = %+v", got)
	}
}

func TestCompareReceiptsDoesNotDropFeeDifferences(t *testing.T) {
	serve := func(fee string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var request rpc.Request
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Errorf("decode request: %v", err)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID,
				"result": map[string]any{"status": "0x1", "gasUsed": "0x5208", "l1Fee": fee}})
		}))
	}
	baseline := serve("0x1")
	defer baseline.Close()
	target := serve("0x2")
	defer target.Close()
	pair := rpc.NewClientPair(baseline.URL, "baseline", target.URL, "target", time.Second)
	reporter := report.NewReporter(report.EndpointMetadata{}, report.EndpointMetadata{}, false)
	tester := &Tester{baselineClient: pair.Baseline, targetClient: pair.Target, reporter: reporter}
	passed, err := tester.CompareReceipts(t.Context(), "fees", common.Hash{}, common.Hash{})
	if err == nil || passed || !reporter.HasFailures() {
		t.Fatalf("fee difference passed: passed=%v err=%v report=%+v", passed, err, reporter.Generate())
	}
	result := reporter.Generate().Results[0]
	if len(result.Differences) == 0 || result.Differences[0].Pointer != "/result/l1Fee" {
		t.Fatalf("fee difference missing: %+v", result)
	}
}

func TestCompareReceiptsIdenticalErrorsAreNotReceipts(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request rpc.Request
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID,
			"error": map[string]any{"code": -32601, "message": "method not found"}})
	}))
	defer server.Close()
	pair := rpc.NewClientPair(server.URL, "baseline", server.URL, "target", time.Second)
	reporter := report.NewReporter(report.EndpointMetadata{}, report.EndpointMetadata{}, false)
	tester := &Tester{baselineClient: pair.Baseline, targetClient: pair.Target, reporter: reporter}
	passed, err := tester.CompareReceipts(t.Context(), "rpc_error", common.Hash{}, common.Hash{})
	if passed || err == nil || !reporter.HasFailures() {
		t.Fatalf("identical RPC errors counted as receipts: passed=%v err=%v report=%+v", passed, err, reporter.Generate())
	}
}

func TestEstimateGasPropagatesRPCFailures(t *testing.T) {
	for _, tc := range []struct {
		name     string
		response func(int) map[string]any
	}{
		{"RPC error", func(id int) map[string]any {
			return map[string]any{"jsonrpc": "2.0", "id": id,
				"error": map[string]any{"code": -32000, "message": "estimate failed"}}
		}},
		{"invalid quantity", func(id int) map[string]any { return map[string]any{"jsonrpc": "2.0", "id": id, "result": "not-hex"} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request rpc.Request
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Errorf("decode request: %v", err)
					return
				}
				_ = json.NewEncoder(w).Encode(tc.response(request.ID))
			}))
			defer server.Close()
			tester := &Tester{}
			client := rpc.NewClient(server.URL, "baseline", time.Second)
			if gas, err := tester.EstimateGas(t.Context(), client, map[string]interface{}{}); err == nil || gas != 0 {
				t.Fatalf("estimate failure = gas %d, error %v", gas, err)
			}
		})
	}
}

func TestNativeTransferStopsOnGasEstimateFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request rpc.Request
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		response := map[string]any{"jsonrpc": "2.0", "id": request.ID}
		switch request.Method {
		case "eth_chainId":
			response["result"] = "0x539"
		case "eth_getBlockByNumber":
			response["result"] = map[string]any{"hash": "0x" + strings.Repeat("11", 32)}
		case "eth_getBalance", "eth_getTransactionCount":
			response["result"] = "0x0"
		case "eth_gasPrice":
			response["result"] = "0x1"
		case "eth_estimateGas":
			response["error"] = map[string]any{"code": -32000, "message": "estimate failed"}
		default:
			t.Errorf("unexpected method %q", request.Method)
		}
		_ = json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()
	pair := rpc.NewClientPair(server.URL, "baseline", server.URL, "target", time.Second)
	const privateKey = "0xac0974bec39a17e36ba4a6b4d238ff944bacb478cbed5efcae784d7bf4f2ff80"
	tester, err := NewTester(pair, privateKey, nil)
	if err != nil {
		t.Fatal(err)
	}
	result := tester.TestNativeTransfer(t.Context(), common.Address{}, big.NewInt(1), TxTypeLegacy, false)
	if result.Passed || !strings.Contains(result.Error, "estimate gas") {
		t.Fatalf("native transfer ignored estimation failure: %+v", result)
	}
}

func TestStatefulReadsRequireSharedLatestBlock(t *testing.T) {
	var businessCalls atomic.Int32
	serve := func(hash string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var request rpc.Request
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Errorf("decode request: %v", err)
				return
			}
			var result any
			switch request.Method {
			case "eth_getBlockByNumber":
				result = map[string]any{"hash": hash}
			case "eth_getBalance":
				businessCalls.Add(1)
				result = "0x1"
			case "eth_call":
				businessCalls.Add(1)
				result = "0x"
			default:
				t.Errorf("unexpected method %q", request.Method)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": result})
		}))
	}
	baseline := serve("0x" + strings.Repeat("11", 32))
	defer baseline.Close()
	target := serve("0x" + strings.Repeat("22", 32))
	defer target.Close()
	pair := rpc.NewClientPair(baseline.URL, "baseline", target.URL, "target", time.Second)
	for _, tc := range []struct {
		name string
		run  func(*Tester) error
	}{
		{"balance", func(tester *Tester) error {
			_, err := tester.GetBalanceAndCompare(t.Context(), common.Address{}, "latest", "balance")
			return err
		}},
		{"contract", func(tester *Tester) error {
			_, err := tester.CallContract(t.Context(), common.Address{}, common.Address{}, nil, "contract")
			return err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reporter := report.NewReporter(report.EndpointMetadata{}, report.EndpointMetadata{}, false)
			tester := &Tester{baselineClient: pair.Baseline, targetClient: pair.Target, reporter: reporter}
			if err := tc.run(tester); !IsInconclusive(err) {
				t.Fatalf("different heads error = %v, want inconclusive", err)
			}
			if got := reporter.Generate().Results[0]; got.Status != report.StatusInconclusive || !reporter.HasFailures() {
				t.Fatalf("different-head result = %+v", got)
			}
		})
	}
	if businessCalls.Load() != 0 {
		t.Fatalf("business RPCs sent across different heads: %d", businessCalls.Load())
	}
}
