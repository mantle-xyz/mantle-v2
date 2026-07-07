package eth

import (
	"context"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/event"
	"github.com/stretchr/testify/require"
)

type blockRefHeadSource struct {
	refs             chan L1BlockRef
	headerSubscribed bool
	refSubscribed    bool
}

func (s *blockRefHeadSource) SubscribeNewHead(ctx context.Context, ch chan<- *types.Header) (ethereum.Subscription, error) {
	s.headerSubscribed = true
	return event.NewSubscription(func(quit <-chan struct{}) error {
		<-quit
		return nil
	}), nil
}

func (s *blockRefHeadSource) SubscribeNewHeadBlockRef(ctx context.Context, ch chan<- L1BlockRef) (ethereum.Subscription, error) {
	s.refSubscribed = true
	return event.NewSubscription(func(quit <-chan struct{}) error {
		for {
			select {
			case ref := <-s.refs:
				ch <- ref
			case <-quit:
				return nil
			}
		}
	}), nil
}

func TestWatchHeadChangesPrefersBlockRefSubscription(t *testing.T) {
	src := &blockRefHeadSource{refs: make(chan L1BlockRef, 1)}
	got := make(chan L1BlockRef, 1)
	sub, err := WatchHeadChanges(context.Background(), src, func(ctx context.Context, sig L1BlockRef) {
		got <- sig
	})
	require.NoError(t, err)
	defer sub.Unsubscribe()
	require.True(t, src.refSubscribed)
	require.False(t, src.headerSubscribed)

	ref := L1BlockRef{
		Hash:       common.HexToHash("0x1234"),
		Number:     1,
		ParentHash: common.HexToHash("0xabcd"),
		Time:       2,
	}
	src.refs <- ref

	select {
	case actual := <-got:
		require.Equal(t, ref, actual)
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for head signal")
	}
}
