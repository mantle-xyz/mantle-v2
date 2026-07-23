package eth

import (
	"context"
	"errors"
	"log/slog"
	"math/big"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/event"
	"github.com/stretchr/testify/require"

	"github.com/ethereum-optimism/optimism/op-service/testlog"
)

type blockRefHeadSource struct {
	refs             chan L1BlockRef
	headers          chan *types.Header
	headerSubscribed bool
	refSubscribed    bool
	// refSubErr makes SubscribeNewHeadBlockRef fail at setup, the way
	// client.PollingClient rejects a channel type it does not accept.
	refSubErr error
}

func (s *blockRefHeadSource) SubscribeNewHead(ctx context.Context, ch chan<- *types.Header) (ethereum.Subscription, error) {
	s.headerSubscribed = true
	return event.NewSubscription(func(quit <-chan struct{}) error {
		for {
			select {
			case header := <-s.headers:
				ch <- header
			case <-quit:
				return nil
			}
		}
	}), nil
}

func (s *blockRefHeadSource) SubscribeNewHeadBlockRef(ctx context.Context, ch chan<- L1BlockRef) (ethereum.Subscription, error) {
	s.refSubscribed = true
	if s.refSubErr != nil {
		return nil, s.refSubErr
	}
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
	sub, err := WatchHeadChanges(context.Background(), testlog.Logger(t, slog.LevelInfo), src, func(ctx context.Context, sig L1BlockRef) {
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

// TestWatchHeadChangesFallsBackWhenBlockRefSubscriptionRejected covers the transport that cannot
// serve the preferred subscription shape. An op-node pointed at an http(s) L1 endpoint runs
// through client.PollingClient, whose Subscribe accepts only a chan<- *types.Header and rejects
// the block-ref subscription at setup (pinned by
// sources.TestEthClient_SubscribeNewHeadBlockRefRejectedByPollingTransport).
//
// Head tracking must survive that: without the fallback the setup error reaches
// event.ResubscribeErr in op-node/node/node.go, which retries a call that can never succeed, and
// the L1 head silently stops advancing for the whole life of the process.
func TestWatchHeadChangesFallsBackWhenBlockRefSubscriptionRejected(t *testing.T) {
	src := &blockRefHeadSource{
		refs:      make(chan L1BlockRef, 1),
		headers:   make(chan *types.Header, 1),
		refSubErr: errors.New("invalid channel type"),
	}
	got := make(chan L1BlockRef, 1)
	sub, err := WatchHeadChanges(context.Background(), testlog.Logger(t, slog.LevelInfo), src, func(ctx context.Context, sig L1BlockRef) {
		got <- sig
	})
	require.NoError(t, err, "a rejected block-ref subscription must fall back, not fail the watcher")
	defer sub.Unsubscribe()
	require.True(t, src.refSubscribed, "the preferred shape must be attempted first")
	require.True(t, src.headerSubscribed, "the plain header subscription must take over")

	header := &types.Header{
		Number:     big.NewInt(3),
		ParentHash: common.HexToHash("0xabcd"),
		Time:       7,
	}
	src.headers <- header

	select {
	case actual := <-got:
		// The fallback derives the ref locally, so the hash is whatever this build's
		// types.Header computes -- that is the cost the warning log records.
		require.Equal(t, header.Hash(), actual.Hash)
		require.Equal(t, uint64(3), actual.Number)
		require.Equal(t, header.ParentHash, actual.ParentHash)
		require.Equal(t, uint64(7), actual.Time)
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for head signal from the fallback path")
	}
}
