package cmd

import (
	"fmt"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/spf13/cobra"
)

func streamEndpointCommandForTest() *cobra.Command {
	cmd := &cobra.Command{}
	cmd.Flags().String("rpc", "", "")
	cmd.Flags().String("watch-rpc", "", "")
	return cmd
}

func TestStreamEndpointFlagsHaveNoLocalDefaults(t *testing.T) {
	for _, name := range []string{"rpc", "watch-rpc"} {
		flag := streamCmd.Flags().Lookup(name)
		if flag == nil || flag.DefValue != "" {
			t.Fatalf("--%s must be registered without a local endpoint default: %v", name, flag)
		}
	}
}

func TestStreamEndpointResolution(t *testing.T) {
	t.Setenv("STREAM_RPC_URL", "http://send.example")
	t.Setenv("STREAM_WATCH_RPC_URL", "http://watch.example")
	send, watch, err := resolveStreamEndpoints(streamEndpointCommandForTest(), true, true)
	if err != nil || send != "http://send.example" || watch != "http://watch.example" {
		t.Fatalf("environment endpoints = %q, %q, %v", send, watch, err)
	}
	cmd := streamEndpointCommandForTest()
	if err := cmd.Flags().Set("rpc", "http://flag-send.example"); err != nil {
		t.Fatal(err)
	}
	send, watch, err = resolveStreamEndpoints(cmd, true, true)
	if err != nil || send != "http://flag-send.example" || watch != "http://watch.example" {
		t.Fatalf("flag override endpoints = %q, %q, %v", send, watch, err)
	}
	t.Setenv("STREAM_WATCH_RPC_URL", "")
	send, watch, err = resolveStreamEndpoints(cmd, true, true)
	if err != nil || watch != send {
		t.Fatalf("watch fallback endpoints = %q, %q, %v", send, watch, err)
	}
}

func TestStreamRequiresOnlyActiveModeEndpoints(t *testing.T) {
	t.Setenv("STREAM_RPC_URL", "")
	t.Setenv("STREAM_WATCH_RPC_URL", "")
	t.Setenv("GETH_RPC_URL", "http://legacy-geth.example")
	t.Setenv("RETH_RPC_URL", "http://legacy-reth.example")
	_, _, err := resolveStreamEndpoints(streamEndpointCommandForTest(), true, true)
	if err == nil || !strings.Contains(err.Error(), "--rpc") {
		t.Fatalf("missing send endpoint error = %v", err)
	}
	cmd := streamEndpointCommandForTest()
	if err := cmd.Flags().Set("watch-rpc", "http://watch.example"); err != nil {
		t.Fatal(err)
	}
	_, watch, err := resolveStreamEndpoints(cmd, false, true)
	if err != nil || watch != "http://watch.example" {
		t.Fatalf("watch-only endpoint = %q, %v", watch, err)
	}
}

func TestAnalyzeGPOTxOrder_NoGPO(t *testing.T) {
	gpo := common.HexToAddress("0x420000000000000000000000000000000000000F")
	txs := []blockTx{
		{Hash: "0x1", To: addrPtr("0x1111111111111111111111111111111111111111")},
		{Hash: "0x2", To: addrPtr("0x2222222222222222222222222222222222222222")},
	}

	got := analyzeGPOTxOrder(txs, gpo)

	if got.HasGPOTx {
		t.Fatalf("expected no gpo tx")
	}
	if got.GPOCount != 0 {
		t.Fatalf("expected gpo count 0, got %d", got.GPOCount)
	}
	if got.FirstGPOIndex != -1 || got.LastGPOIndex != -1 {
		t.Fatalf("expected indexes -1/-1, got %d/%d", got.FirstGPOIndex, got.LastGPOIndex)
	}
	if got.HasTxAfterFirstGPO || got.HasTxAfterLastGPO {
		t.Fatalf("expected no tx-after flags for no gpo")
	}
}

func TestAnalyzeGPOTxOrder_GPOAtLast(t *testing.T) {
	gpo := common.HexToAddress("0x420000000000000000000000000000000000000F")
	txs := []blockTx{
		{Hash: "0x1", To: addrPtr("0x1111111111111111111111111111111111111111")},
		{Hash: "0x2", To: addrPtr(gpo.Hex())},
	}

	got := analyzeGPOTxOrder(txs, gpo)

	if !got.HasGPOTx {
		t.Fatalf("expected gpo tx")
	}
	if got.GPOCount != 1 {
		t.Fatalf("expected gpo count 1, got %d", got.GPOCount)
	}
	if got.FirstGPOIndex != 1 || got.LastGPOIndex != 1 {
		t.Fatalf("expected indexes 1/1, got %d/%d", got.FirstGPOIndex, got.LastGPOIndex)
	}
	if got.HasTxAfterFirstGPO || got.HasTxAfterLastGPO {
		t.Fatalf("expected no tx after gpo at tail")
	}
}

func TestAnalyzeGPOTxOrder_GPOInMiddleHasFollowingTx(t *testing.T) {
	gpo := common.HexToAddress("0x420000000000000000000000000000000000000F")
	txs := []blockTx{
		{Hash: "0x1", To: addrPtr("0x1111111111111111111111111111111111111111")},
		{Hash: "0x2", To: addrPtr(gpo.Hex())},
		{Hash: "0x3", To: addrPtr("0x3333333333333333333333333333333333333333")},
	}

	got := analyzeGPOTxOrder(txs, gpo)

	if !got.HasGPOTx {
		t.Fatalf("expected gpo tx")
	}
	if got.GPOCount != 1 {
		t.Fatalf("expected gpo count 1, got %d", got.GPOCount)
	}
	if got.FirstGPOIndex != 1 || got.LastGPOIndex != 1 {
		t.Fatalf("expected indexes 1/1, got %d/%d", got.FirstGPOIndex, got.LastGPOIndex)
	}
	if !got.HasTxAfterFirstGPO || !got.HasTxAfterLastGPO {
		t.Fatalf("expected tx after gpo")
	}
}

func TestAnalyzeGPOTxOrder_MultiGPO_TrackFirstAndLast(t *testing.T) {
	gpo := common.HexToAddress("0x420000000000000000000000000000000000000F")
	txs := []blockTx{
		{Hash: "0x1", To: addrPtr(gpo.Hex())},
		{Hash: "0x2", To: addrPtr("0x2222222222222222222222222222222222222222")},
		{Hash: "0x3", To: addrPtr(gpo.Hex())},
	}

	got := analyzeGPOTxOrder(txs, gpo)

	if !got.HasGPOTx {
		t.Fatalf("expected gpo tx")
	}
	if got.GPOCount != 2 {
		t.Fatalf("expected gpo count 2, got %d", got.GPOCount)
	}
	if got.FirstGPOIndex != 0 || got.LastGPOIndex != 2 {
		t.Fatalf("expected indexes 0/2, got %d/%d", got.FirstGPOIndex, got.LastGPOIndex)
	}
	if !got.HasTxAfterFirstGPO {
		t.Fatalf("expected tx after first gpo")
	}
	if got.HasTxAfterLastGPO {
		t.Fatalf("expected no tx after last gpo")
	}
}

func addrPtr(v string) *string {
	return &v
}

func TestResolveSendRecipient_DefaultToSender(t *testing.T) {
	sender := common.HexToAddress("0xf39Fd6e51aad88F6F4ce6aB8827279cffFb92266")

	got, err := resolveSendRecipient("", sender)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if got != sender {
		t.Fatalf("expected sender address %s, got %s", sender.Hex(), got.Hex())
	}
}

func TestResolveSendRecipient_InvalidAddress(t *testing.T) {
	sender := common.HexToAddress("0xf39Fd6e51aad88F6F4ce6aB8827279cffFb92266")

	_, err := resolveSendRecipient("not-an-address", sender)
	if err == nil {
		t.Fatalf("expected error for invalid recipient address")
	}
}

func TestSendNonceTracker_PreferLocalMonotonicNonceOnLaggedPending(t *testing.T) {
	var tracker sendNonceTracker

	// The first on-chain pending nonce is 10.
	first := tracker.Next(10)
	if first != 10 {
		t.Fatalf("expected first nonce 10, got %d", first)
	}
	tracker.MarkAccepted(first)

	// A stale pending nonce of 10 must not override the local nonce of 11.
	second := tracker.Next(10)
	if second != 11 {
		t.Fatalf("expected second nonce 11 with lagged chain nonce, got %d", second)
	}
}

func TestSendNonceTracker_AdvanceToHigherChainNonce(t *testing.T) {
	var tracker sendNonceTracker

	n := tracker.Next(20)
	tracker.MarkAccepted(n)

	// Follow the chain when its pending nonce moves ahead.
	next := tracker.Next(25)
	if next != 25 {
		t.Fatalf("expected nonce 25 after chain advanced, got %d", next)
	}
}

func TestIsAlreadyKnownSendError(t *testing.T) {
	err := fmt.Errorf("发送交易失败: RPC error: code=-32000, message=failed to forward tx to sequencer, err: 'already known'")
	if !isAlreadyKnownSendError(err) {
		t.Fatalf("expected already known error to be detected")
	}
}
