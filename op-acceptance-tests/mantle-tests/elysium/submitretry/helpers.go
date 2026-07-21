package submitretry

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"fmt"
	"math/big"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/ethereum-optimism/optimism/op-devstack/devtest"
	"github.com/ethereum-optimism/optimism/op-devstack/sysgo"
	"github.com/ethereum-optimism/optimism/op-service/apis"
	opcrypto "github.com/ethereum-optimism/optimism/op-service/crypto"
	"github.com/ethereum-optimism/optimism/op-service/eth"
	"github.com/ethereum-optimism/optimism/op-service/txmgr"
	"github.com/ethereum-optimism/optimism/op-service/txmgr/metrics"
	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/rpc"
)

// configureDevstackEnvVars runs the L1 EL as an external Amsterdam-capable geth
// subprocess so the L2 genuinely consumes a Glamsterdam L1. It auto-locates a
// mise-installed geth (mirroring the fusaka suite) so these tests are self-sufficient
// and do NOT require SYSGO_GETH_EXEC_PATH to be exported by the runner. No-op if
// DevstackL1ELKindEnvVar or GethExecPathEnvVar is already set.
func configureDevstackEnvVars() func() {
	if _, ok := os.LookupEnv(sysgo.DevstackL1ELKindEnvVar); ok {
		return func() {}
	}
	if _, ok := os.LookupEnv(sysgo.GethExecPathEnvVar); ok {
		return func() {}
	}

	cmd := exec.Command("mise", "which", "geth")
	buf := bytes.NewBuffer([]byte{})
	cmd.Stdout = buf
	if err := cmd.Run(); err != nil {
		fmt.Printf("Failed to find mise-installed geth: %v\n", err)
		return func() {}
	}
	execPath := strings.TrimSpace(buf.String())
	fmt.Println("Found mise-installed geth:", execPath)

	_ = os.Setenv(sysgo.GethExecPathEnvVar, execPath)
	_ = os.Setenv(sysgo.DevstackL1ELKindEnvVar, "geth")
	return func() {
		_ = os.Unsetenv(sysgo.GethExecPathEnvVar)
		_ = os.Unsetenv(sysgo.DevstackL1ELKindEnvVar)
	}
}

// elBackend adapts the devstack's L1 EL client to txmgr.ETHBackend, so these tests can drive the
// REAL op-batcher/op-proposer submission code (op-service/txmgr) against the devstack's
// Glamsterdam L1. The devstack only hands out an apis.EthClient, not an RPC URL, so the few
// methods that client does not expose directly are issued over its raw RPC handle.
type elBackend struct {
	cl apis.EthClient
}

var _ txmgr.ETHBackend = (*elBackend)(nil)

func (b *elBackend) BlockNumber(ctx context.Context) (uint64, error) {
	info, err := b.cl.InfoByLabel(ctx, eth.Unsafe)
	if err != nil {
		return 0, err
	}
	return info.NumberU64(), nil
}

func (b *elBackend) CallContract(ctx context.Context, msg ethereum.CallMsg, blockNumber *big.Int) ([]byte, error) {
	block := rpc.LatestBlockNumber
	if blockNumber != nil {
		block = rpc.BlockNumber(blockNumber.Int64())
	}
	return b.cl.Call(ctx, msg, block)
}

func (b *elBackend) TransactionReceipt(ctx context.Context, txHash common.Hash) (*types.Receipt, error) {
	return b.cl.TransactionReceipt(ctx, txHash)
}

func (b *elBackend) SendTransaction(ctx context.Context, tx *types.Transaction) error {
	return b.cl.SendTransaction(ctx, tx)
}

func (b *elBackend) HeaderByNumber(ctx context.Context, number *big.Int) (*types.Header, error) {
	var (
		info eth.BlockInfo
		err  error
	)
	if number == nil {
		info, err = b.cl.InfoByLabel(ctx, eth.Unsafe)
	} else {
		info, err = b.cl.InfoByNumber(ctx, number.Uint64())
	}
	if err != nil {
		return nil, err
	}
	return info.Header(), nil
}

func (b *elBackend) SuggestGasTipCap(ctx context.Context) (*big.Int, error) {
	var tip hexutil.Big
	if err := b.cl.RPC().CallContext(ctx, &tip, "eth_maxPriorityFeePerGas"); err != nil {
		return nil, err
	}
	return (*big.Int)(&tip), nil
}

func (b *elBackend) BlobBaseFee(ctx context.Context) (*big.Int, error) {
	var fee hexutil.Big
	if err := b.cl.RPC().CallContext(ctx, &fee, "eth_blobBaseFee"); err != nil {
		return nil, err
	}
	return (*big.Int)(&fee), nil
}

func (b *elBackend) NonceAt(ctx context.Context, account common.Address, blockNumber *big.Int) (uint64, error) {
	return b.cl.NonceAt(ctx, account, blockNumber)
}

func (b *elBackend) PendingNonceAt(ctx context.Context, account common.Address) (uint64, error) {
	return b.cl.PendingNonceAt(ctx, account)
}

func (b *elBackend) EstimateGas(ctx context.Context, msg ethereum.CallMsg) (uint64, error) {
	return b.cl.EstimateGas(ctx, msg)
}

func (b *elBackend) Close() {}

// newTxMgr builds a SimpleTxManager over the given backend, signing with the given key. Timings
// are compressed relative to production defaults (which wait 48s before bumping) so the retry
// path is exercised within an acceptance-test budget; the retry LOGIC under test is untouched.
func newTxMgr(t devtest.T, backend txmgr.ETHBackend, priv *ecdsa.PrivateKey, from common.Address, chainID *big.Int) *txmgr.SimpleTxManager {
	bindSigner := opcrypto.PrivateKeySignerFn(priv, chainID)
	cfg := &txmgr.Config{
		Backend: backend,
		ChainID: chainID,
		Signer: func(_ context.Context, addr common.Address, tx *types.Transaction) (*types.Transaction, error) {
			return bindSigner(addr, tx)
		},
		From:                      from,
		NetworkTimeout:            10 * time.Second,
		ReceiptQueryInterval:      250 * time.Millisecond,
		NumConfirmations:          1,
		SafeAbortNonceTooLowCount: 3,
		TxNotInMempoolTimeout:     90 * time.Second,
		TxSendTimeout:             120 * time.Second,
		RetryInterval:             time.Second,
		MaxRetries:                10,
	}
	// Bump after 2s instead of the 48s production default: without this the test would spend most
	// of its budget waiting for the first bump ticker.
	cfg.ResubmissionTimeout.Store(int64(2 * time.Second))
	cfg.FeeLimitMultiplier.Store(10)
	// Floor the fee knobs at 1 wei so the deliberately-underpriced first attempt below is not
	// silently lifted back to a mineable fee by the min-tip/min-basefee enforcement.
	cfg.MinTipCap.Store(big.NewInt(1))
	cfg.MinBaseFee.Store(big.NewInt(1))
	cfg.MinBlobTxFee.Store(big.NewInt(1))

	m, err := txmgr.NewSimpleTxManagerFromConfig("acceptance", t.Logger(), &metrics.NoopTxMetrics{}, cfg)
	t.Require().NoError(err, "build tx manager over the Glamsterdam L1")
	return m
}
