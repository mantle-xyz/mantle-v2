package tx

import (
	"context"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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
	if !strings.Contains(result.Error, "old-reth") || strings.Contains(result.Error, "geth 应该") {
		t.Fatalf("rejection error = %q", result.Error)
	}
}

func TestNewTesterKeepsLogicalClientNames(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":"0x1388"}`))
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
			_, err := tester.GetBalanceAndCompare(context.Background(), common.Address{}, "latest", "balance")
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
