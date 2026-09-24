package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/ethereum-optimism/optimism/op-rpc-compat/pkg/rpc"
	"github.com/ethereum-optimism/optimism/op-rpc-compat/pkg/tx"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/spf13/cobra"
)

const (
	defaultGPOTarget      = "0x420000000000000000000000000000000000000F"
	defaultSenderPrivKey0 = "0xac0974bec39a17e36ba4a6b4d238ff944bacb478cbed5efcae784d7bf4f2ff80"
)

var (
	streamRPC        string
	streamWatchRPC   string
	streamSend       bool
	streamWatch      bool
	streamPrivateKey string
	streamRecipient  string
	streamAmount     string
	streamGasLimit   uint64
	streamLeadMS     int

	streamSendPollInterval  time.Duration
	streamFallbackBlockTime time.Duration

	streamStartBlock        string
	streamWatchPollInterval time.Duration
	streamGPOAddress        string
)

var streamCmd = &cobra.Command{
	Use:   "stream",
	Short: "Send transactions and monitor block ordering",
	Long: `Send transactions near block boundaries and monitor GPO transaction ordering.
Provide the send and watch RPC endpoints explicitly or through environment variables.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		sendURL, watchURL, err := resolveStreamEndpoints(cmd, streamSend, streamWatch)
		if err != nil {
			return err
		}
		streamRPC, streamWatchRPC = sendURL, watchURL
		return runStream()
	},
}

func resolveStreamEndpoints(cmd *cobra.Command, send, watch bool) (string, string, error) {
	sendURL, err := endpointFlagValue(cmd, "rpc", "STREAM_RPC_URL")
	if err != nil {
		return "", "", err
	}
	watchURL, err := endpointFlagValue(cmd, "watch-rpc", "STREAM_WATCH_RPC_URL")
	if err != nil {
		return "", "", err
	}
	if send && sendURL == "" {
		return "", "", fmt.Errorf("send mode requires --rpc or STREAM_RPC_URL")
	}
	if watch && watchURL == "" {
		watchURL = sendURL
	}
	if watch && watchURL == "" {
		return "", "", fmt.Errorf("watch mode requires --watch-rpc, STREAM_WATCH_RPC_URL, or --rpc")
	}
	return sendURL, watchURL, nil
}

func init() {
	rootCmd.AddCommand(streamCmd)

	defaultPrivateKey := os.Getenv("TX_PRIVATE_KEY")
	if defaultPrivateKey == "" {
		defaultPrivateKey = defaultSenderPrivKey0
	}

	streamCmd.Flags().StringVar(&streamRPC, "rpc", "", "RPC endpoint used to send transactions")
	streamCmd.Flags().BoolVar(&streamSend, "send", true, "持续发送交易（尽量贴近每个区块尾部）")
	streamCmd.Flags().BoolVar(&streamWatch, "watch", true, "持续监听区块交易并检查 GPO 交易顺序")

	streamCmd.Flags().StringVar(&streamPrivateKey, "private-key", defaultPrivateKey, "发送交易的私钥（仅 --send 生效）")
	streamCmd.Flags().StringVar(&streamRecipient, "to", "", "交易接收地址（仅 --send 生效，默认发送给自己）")
	streamCmd.Flags().StringVar(&streamAmount, "amount", "1", "转账金额（wei，仅 --send 生效）")
	streamCmd.Flags().Uint64Var(&streamGasLimit, "gas-limit", 21000, "gas limit（仅 --send 生效）")
	streamCmd.Flags().IntVar(&streamLeadMS, "lead-ms", 1200, "预计下个区块前多少毫秒发送（仅 --send 生效）")
	streamCmd.Flags().DurationVar(&streamSendPollInterval, "send-poll", 200*time.Millisecond, "发送模式轮询新区块间隔")
	streamCmd.Flags().DurationVar(&streamFallbackBlockTime, "block-time", 2*time.Second, "发送模式的默认区块时间（用于初始预测）")

	streamCmd.Flags().StringVar(&streamWatchRPC, "watch-rpc", "", "RPC endpoint to monitor (defaults to --rpc)")
	streamCmd.Flags().StringVar(&streamStartBlock, "start-block", "latest", "监听起始块号（latest/十进制/0x十六进制）")
	streamCmd.Flags().DurationVar(&streamWatchPollInterval, "watch-poll", 1*time.Second, "监听模式轮询间隔")
	streamCmd.Flags().StringVar(&streamGPOAddress, "gpo-address", defaultGPOTarget, "GPO 合约地址")
}

func runStream() error {
	if !streamSend && !streamWatch {
		return fmt.Errorf("至少指定一个模式: --send 或 --watch")
	}

	if streamLeadMS < 0 {
		return fmt.Errorf("--lead-ms 不能小于 0")
	}
	if streamGasLimit == 0 {
		return fmt.Errorf("--gas-limit 不能为 0")
	}
	if streamSendPollInterval <= 0 || streamWatchPollInterval <= 0 {
		return fmt.Errorf("轮询间隔必须大于 0")
	}
	if streamFallbackBlockTime <= 0 {
		return fmt.Errorf("--block-time 必须大于 0")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var wg sync.WaitGroup
	errCh := make(chan error, 2)

	if streamSend {
		sendCfg, err := buildSendConfig()
		if err != nil {
			return err
		}

		fmt.Printf("[send] rpc=%s from=%s to=%s amount=%s gas_limit=%d lead=%dms\n",
			streamRPC, sendCfg.tester.Builder().Address().Hex(), sendCfg.to.Hex(), sendCfg.amount.String(), streamGasLimit, streamLeadMS)

		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := runSendLoop(ctx, sendCfg); err != nil {
				errCh <- err
			}
		}()
	}

	if streamWatch {
		watchRPC := streamWatchRPC
		if watchRPC == "" {
			watchRPC = streamRPC
		}

		if !common.IsHexAddress(streamGPOAddress) {
			return fmt.Errorf("无效的 --gpo-address: %s", streamGPOAddress)
		}
		gpoAddr := common.HexToAddress(streamGPOAddress)

		watchClient := rpc.NewClient(watchRPC, "watch", timeout)
		startAt, err := resolveStartBlock(ctx, watchClient, streamStartBlock)
		if err != nil {
			return err
		}

		fmt.Printf("[watch] rpc=%s start_block=%d gpo=%s\n", watchRPC, startAt, gpoAddr.Hex())

		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := runWatchLoop(ctx, watchClient, startAt, gpoAddr); err != nil {
				errCh <- err
			}
		}()
	}

	fmt.Println("stream 已启动，Ctrl+C 退出")

	select {
	case <-ctx.Done():
		wg.Wait()
		return nil
	case err := <-errCh:
		stop()
		wg.Wait()
		return err
	}
}

type sendConfig struct {
	tester *tx.Tester
	client *rpc.Client
	to     common.Address
	amount *big.Int
}

// sendNonceTracker keeps a local nonce so a stale pending nonce cannot cause duplicate sends.
type sendNonceTracker struct {
	initialized bool
	nextNonce   uint64
}

func (t *sendNonceTracker) Next(chainPendingNonce uint64) uint64 {
	if !t.initialized || chainPendingNonce > t.nextNonce {
		t.nextNonce = chainPendingNonce
		t.initialized = true
	}
	return t.nextNonce
}

func (t *sendNonceTracker) MarkAccepted(nonce uint64) {
	next := nonce + 1
	if !t.initialized || next > t.nextNonce {
		t.nextNonce = next
		t.initialized = true
	}
}

func (t *sendNonceTracker) Reset(chainPendingNonce uint64) {
	t.nextNonce = chainPendingNonce
	t.initialized = true
}

func buildSendConfig() (*sendConfig, error) {
	amount, ok := new(big.Int).SetString(streamAmount, 10)
	if !ok || amount.Sign() < 0 {
		return nil, fmt.Errorf("无效的 --amount: %s", streamAmount)
	}
	if strings.TrimSpace(streamPrivateKey) == "" {
		return nil, fmt.Errorf("--private-key 不能为空")
	}

	streamPair := rpc.NewClientPair(streamRPC, "stream", streamRPC, "stream", 30*time.Second)
	tester, err := tx.NewTester(streamPair, streamPrivateKey, nil)
	if err != nil {
		return nil, fmt.Errorf("初始化发送器失败: %w", err)
	}

	to, err := resolveSendRecipient(streamRecipient, tester.Builder().Address())
	if err != nil {
		return nil, err
	}

	return &sendConfig{
		tester: tester,
		client: tester.BaselineClient(),
		to:     to,
		amount: amount,
	}, nil
}

func resolveSendRecipient(raw string, sender common.Address) (common.Address, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return sender, nil
	}
	if !common.IsHexAddress(trimmed) {
		return common.Address{}, fmt.Errorf("无效的 --to 地址: %s", raw)
	}
	return common.HexToAddress(trimmed), nil
}

func runSendLoop(ctx context.Context, cfg *sendConfig) error {
	ticker := time.NewTicker(streamSendPollInterval)
	defer ticker.Stop()

	var (
		seenBlock      bool
		lastBlockNum   uint64
		lastBlockTs    uint64
		estimatedDelta = streamFallbackBlockTime
		nonceTracker   sendNonceTracker
	)

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			head, err := getBlockHeader(ctx, cfg.client, "latest")
			if err != nil {
				fmt.Printf("[send] 获取 latest 区块失败: %v\n", err)
				continue
			}
			if head == nil {
				continue
			}
			if seenBlock && head.Number == lastBlockNum {
				continue
			}

			if seenBlock && head.Timestamp > lastBlockTs {
				observed := time.Duration(head.Timestamp-lastBlockTs) * time.Second
				estimatedDelta = blendInterval(estimatedDelta, observed)
			}

			seenBlock = true
			lastBlockNum = head.Number
			lastBlockTs = head.Timestamp

			waitFor := estimatedDelta - time.Duration(streamLeadMS)*time.Millisecond
			if waitFor < 0 {
				waitFor = 0
			}

			if waitFor > 0 {
				timer := time.NewTimer(waitFor)
				select {
				case <-ctx.Done():
					timer.Stop()
					return nil
				case <-timer.C:
				}
			}

			latestNum, err := getLatestBlockNumber(ctx, cfg.client)
			if err == nil && latestNum > lastBlockNum {
				continue
			}

			if head.BaseFeePerGas == nil || head.BaseFeePerGas.Sign() <= 0 {
				fmt.Printf("[send] 区块 %d 无 baseFeePerGas，跳过本轮（需 EIP-1559 链）\n", head.Number)
				continue
			}

			chainPendingNonce, err := cfg.tester.GetNonce(ctx, cfg.client, cfg.tester.Builder().Address())
			if err != nil {
				fmt.Printf("[send] 获取 nonce 失败 block=%d err=%v\n", lastBlockNum, err)
				continue
			}
			nonce := nonceTracker.Next(chainPendingNonce)

			sendCtx, cancel := context.WithTimeout(ctx, timeout)
			txHash, gasPrice, err := sendOneEIP1559TxNoPriorityFee(sendCtx, cfg, nonce, head.BaseFeePerGas)
			cancel()
			if err != nil {
				if isAlreadyKnownSendError(err) {
					nonceTracker.MarkAccepted(nonce)
					fmt.Printf("[send] block=%d nonce=%d err=already known，按已发送处理\n", lastBlockNum, nonce)
					continue
				}
				if isNonceSyncSendError(err) {
					refreshedNonce, nonceErr := cfg.tester.GetNonce(ctx, cfg.client, cfg.tester.Builder().Address())
					if nonceErr == nil {
						nonceTracker.Reset(refreshedNonce)
						fmt.Printf("[send] nonce 状态已重置为 %d（发送失败后重同步）\n", refreshedNonce)
					}
				}
				fmt.Printf("[send] 发送失败 block=%d nonce=%d err=%v\n", lastBlockNum, nonce, err)
				continue
			}

			nonceTracker.MarkAccepted(nonce)
			fmt.Printf("[send] block=%d nonce=%d baseFee=%s tx=%s\n", lastBlockNum, nonce, gasPrice.String(), txHash.Hex())
		}
	}
}

// sendOneEIP1559TxNoPriorityFee sends an EIP-1559 transaction with no priority fee and caps the fee at the base fee.
func sendOneEIP1559TxNoPriorityFee(ctx context.Context, cfg *sendConfig, nonce uint64, baseFee *big.Int) (common.Hash, *big.Int, error) {
	zeroTip := big.NewInt(0)
	params := &tx.TxParams{
		From:                 cfg.tester.Builder().Address(),
		To:                   &cfg.to,
		Value:                cfg.amount,
		Gas:                  streamGasLimit,
		Nonce:                nonce,
		ChainID:              cfg.tester.ChainID(),
		MaxFeePerGas:         baseFee,
		MaxPriorityFeePerGas: zeroTip,
	}
	signedTx, err := cfg.tester.Builder().BuildAndSign(tx.TxTypeEIP1559, params)
	if err != nil {
		return common.Hash{}, nil, fmt.Errorf("构建交易失败: %w", err)
	}

	txHash, err := cfg.tester.SendRawTransaction(ctx, cfg.client, signedTx, "stream_send")
	if err != nil {
		return common.Hash{}, nil, fmt.Errorf("发送交易失败: %w", err)
	}

	return txHash, baseFee, nil
}

func isAlreadyKnownSendError(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(strings.ToLower(err.Error()), "already known")
}

func isNonceSyncSendError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	for _, marker := range []string{
		"nonce too low",
		"nonce too high",
		"nonce has already been used",
		"replacement transaction underpriced",
		"transaction underpriced",
	} {
		if strings.Contains(msg, marker) {
			return true
		}
	}
	return false
}

func blendInterval(prev, current time.Duration) time.Duration {
	if current <= 0 {
		return prev
	}
	blended := (prev*7 + current*3) / 10
	min := 500 * time.Millisecond
	max := 30 * time.Second
	if blended < min {
		return min
	}
	if blended > max {
		return max
	}
	return blended
}

func runWatchLoop(ctx context.Context, client *rpc.Client, startAt uint64, gpo common.Address) error {
	next := startAt
	ticker := time.NewTicker(streamWatchPollInterval)
	defer ticker.Stop()

	for {
		latest, err := getLatestBlockNumber(ctx, client)
		if err != nil {
			fmt.Printf("[watch] 获取 blockNumber 失败: %v\n", err)
		} else {
			for next <= latest {
				block, err := getBlockWithTxs(ctx, client, next)
				if err != nil {
					fmt.Printf("[watch] 获取区块 %d 失败: %v\n", next, err)
					break
				}
				if block == nil {
					break
				}

				analysis := analyzeGPOTxOrder(block.Transactions, gpo)
				if analysis.HasGPOTx {
					fmt.Printf("[watch] block=%d txs=%d gpo_count=%d first_idx=%d last_idx=%d after_first=%t after_last=%t first_hash=%s last_hash=%s\n",
						block.Number, len(block.Transactions), analysis.GPOCount, analysis.FirstGPOIndex, analysis.LastGPOIndex,
						analysis.HasTxAfterFirstGPO, analysis.HasTxAfterLastGPO, shortenHash(analysis.FirstGPOTxHash), shortenHash(analysis.LastGPOTxHash))
				} else {
					fmt.Printf("[watch] block=%d txs=%d gpo_count=0\n", block.Number, len(block.Transactions))
				}

				next++
			}
		}

		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

func resolveStartBlock(ctx context.Context, client *rpc.Client, raw string) (uint64, error) {
	start := strings.TrimSpace(strings.ToLower(raw))
	switch start {
	case "", "latest":
		latest, err := getLatestBlockNumber(ctx, client)
		if err != nil {
			return 0, fmt.Errorf("读取 latest 区块失败: %w", err)
		}
		return latest + 1, nil
	default:
		parsed, err := parseBlockNumber(start)
		if err != nil {
			return 0, fmt.Errorf("无效的 --start-block: %s", raw)
		}
		return parsed, nil
	}
}

func parseBlockNumber(v string) (uint64, error) {
	if strings.HasPrefix(v, "0x") {
		return hexutil.DecodeUint64(v)
	}
	return strconv.ParseUint(v, 10, 64)
}

type blockHeader struct {
	Number        uint64
	Hash          string
	Timestamp     uint64
	BaseFeePerGas *big.Int // available on EIP-1559 blocks; used to send without a priority fee
}

type blockTx struct {
	Hash string  `json:"hash"`
	To   *string `json:"to"`
}

type blockWithTxs struct {
	Number       uint64
	Hash         string
	Timestamp    uint64
	Transactions []blockTx
}

type rawBlockHeader struct {
	Number        string `json:"number"`
	Hash          string `json:"hash"`
	Timestamp     string `json:"timestamp"`
	BaseFeePerGas string `json:"baseFeePerGas,omitempty"`
}

type rawBlockWithTxs struct {
	Number       string    `json:"number"`
	Hash         string    `json:"hash"`
	Timestamp    string    `json:"timestamp"`
	Transactions []blockTx `json:"transactions"`
}

func getLatestBlockNumber(ctx context.Context, client *rpc.Client) (uint64, error) {
	resp := client.Call(ctx, rpc.NewRequest("eth_blockNumber", nil))
	if err := unwrapRPCError(resp); err != nil {
		return 0, err
	}

	var blockHex string
	if err := json.Unmarshal(resp.Response.Result, &blockHex); err != nil {
		return 0, fmt.Errorf("解析 blockNumber 失败: %w", err)
	}
	blockNum, err := hexutil.DecodeUint64(blockHex)
	if err != nil {
		return 0, fmt.Errorf("解析 blockNumber(16进制)失败: %w", err)
	}
	return blockNum, nil
}

func getBlockHeader(ctx context.Context, client *rpc.Client, tag string) (*blockHeader, error) {
	resp := client.Call(ctx, rpc.NewRequest("eth_getBlockByNumber", []interface{}{tag, false}))
	if err := unwrapRPCError(resp); err != nil {
		return nil, err
	}
	if string(resp.Response.Result) == "null" {
		return nil, nil
	}

	var raw rawBlockHeader
	if err := json.Unmarshal(resp.Response.Result, &raw); err != nil {
		return nil, fmt.Errorf("解析区块头失败: %w", err)
	}
	num, err := hexutil.DecodeUint64(raw.Number)
	if err != nil {
		return nil, fmt.Errorf("解析区块号失败: %w", err)
	}
	ts, err := hexutil.DecodeUint64(raw.Timestamp)
	if err != nil {
		return nil, fmt.Errorf("解析区块时间戳失败: %w", err)
	}
	h := &blockHeader{Number: num, Hash: raw.Hash, Timestamp: ts}
	if raw.BaseFeePerGas != "" {
		bf, err := hexutil.DecodeBig(raw.BaseFeePerGas)
		if err == nil {
			h.BaseFeePerGas = bf
		}
	}
	return h, nil
}

func getBlockWithTxs(ctx context.Context, client *rpc.Client, number uint64) (*blockWithTxs, error) {
	blockTag := hexutil.EncodeUint64(number)
	resp := client.Call(ctx, rpc.NewRequest("eth_getBlockByNumber", []interface{}{blockTag, true}))
	if err := unwrapRPCError(resp); err != nil {
		return nil, err
	}
	if string(resp.Response.Result) == "null" {
		return nil, nil
	}

	var raw rawBlockWithTxs
	if err := json.Unmarshal(resp.Response.Result, &raw); err != nil {
		return nil, fmt.Errorf("解析区块交易失败: %w", err)
	}

	num, err := hexutil.DecodeUint64(raw.Number)
	if err != nil {
		return nil, fmt.Errorf("解析区块号失败: %w", err)
	}
	ts, err := hexutil.DecodeUint64(raw.Timestamp)
	if err != nil {
		return nil, fmt.Errorf("解析区块时间戳失败: %w", err)
	}

	return &blockWithTxs{
		Number:       num,
		Hash:         raw.Hash,
		Timestamp:    ts,
		Transactions: raw.Transactions,
	}, nil
}

func unwrapRPCError(resp *rpc.ResponseWithMeta) error {
	if resp == nil {
		return fmt.Errorf("空响应")
	}
	if resp.Error != nil {
		return resp.Error
	}
	if resp.Response == nil {
		return fmt.Errorf("缺少 RPC 响应体")
	}
	if resp.Response.Error != nil {
		return fmt.Errorf("RPC error: code=%d message=%s", resp.Response.Error.Code, resp.Response.Error.Message)
	}
	return nil
}

type gpoTxOrderResult struct {
	HasGPOTx           bool
	GPOCount           int
	FirstGPOIndex      int
	LastGPOIndex       int
	HasTxAfterFirstGPO bool
	HasTxAfterLastGPO  bool
	FirstGPOTxHash     string
	LastGPOTxHash      string
}

func analyzeGPOTxOrder(txs []blockTx, gpo common.Address) gpoTxOrderResult {
	result := gpoTxOrderResult{
		FirstGPOIndex: -1,
		LastGPOIndex:  -1,
	}

	for idx, txn := range txs {
		if txn.To == nil || !common.IsHexAddress(*txn.To) {
			continue
		}
		if common.HexToAddress(*txn.To) != gpo {
			continue
		}

		result.HasGPOTx = true
		result.GPOCount++

		if result.FirstGPOIndex == -1 {
			result.FirstGPOIndex = idx
			result.FirstGPOTxHash = txn.Hash
		}
		result.LastGPOIndex = idx
		result.LastGPOTxHash = txn.Hash
	}

	if result.HasGPOTx {
		result.HasTxAfterFirstGPO = result.FirstGPOIndex < len(txs)-1
		result.HasTxAfterLastGPO = result.LastGPOIndex < len(txs)-1
	}

	return result
}

func shortenHash(h string) string {
	if len(h) <= 10 {
		return h
	}
	return h[:10] + "..."
}
