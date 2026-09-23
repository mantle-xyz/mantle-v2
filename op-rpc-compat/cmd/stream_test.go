package cmd

import (
	"fmt"
	"testing"

	"github.com/ethereum/go-ethereum/common"
)

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

	// 初次从链上取到 10
	first := tracker.Next(10)
	if first != 10 {
		t.Fatalf("expected first nonce 10, got %d", first)
	}
	tracker.MarkAccepted(first)

	// 链上 pending 仍旧滞后返回 10，本地应继续使用 11，避免重复交易
	second := tracker.Next(10)
	if second != 11 {
		t.Fatalf("expected second nonce 11 with lagged chain nonce, got %d", second)
	}
}

func TestSendNonceTracker_AdvanceToHigherChainNonce(t *testing.T) {
	var tracker sendNonceTracker

	n := tracker.Next(20)
	tracker.MarkAccepted(n)

	// 链上出现更高 pending nonce 时，应追上链上状态
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
