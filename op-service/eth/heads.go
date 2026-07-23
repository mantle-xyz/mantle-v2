package eth

import (
	"context"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/event"
	"github.com/ethereum/go-ethereum/log"
)

// HeadSignalFn is used as callback function to accept head-signals
type HeadSignalFn func(ctx context.Context, sig L1BlockRef)

type NewHeadSource interface {
	SubscribeNewHead(ctx context.Context, ch chan<- *types.Header) (ethereum.Subscription, error)
}

type NewHeadBlockRefSource interface {
	SubscribeNewHeadBlockRef(ctx context.Context, ch chan<- L1BlockRef) (ethereum.Subscription, error)
}

// WatchHeadChanges wraps a new-head subscription from NewHeadSource to feed the given Tracker.
// The ctx is only used to create the subscription, and does not affect the returned subscription.
//
// Two subscription shapes are supported. A source that implements NewHeadBlockRefSource is
// preferred: it delivers L1BlockRef directly, decoded by the source itself, which is what lets a
// client that understands newer header fields (EIP-7928 blockAccessListHash, EIP-7843 slotNumber)
// report a correct block hash. The plain NewHeadSource path decodes into a *types.Header and
// derives the ref locally, which is only correct as long as the local header type knows every
// field the chain uses. Both share one subscription loop below.
//
// The preferred shape is not available on every transport, so a rejected block-ref subscription
// falls back to the plain one rather than failing. An http(s) endpoint is wrapped in
// client.PollingClient (see op-service/client/rpc.go NewRPCWithClient), and its Subscribe accepts
// only a chan<- *types.Header, so a source subscribing with its own header type is rejected at
// setup. Without the fallback that error reaches event.ResubscribeErr in the callers, which
// retries a call that can never succeed -- L1 head tracking stops advancing for as long as the
// endpoint stays http. The warning is what keeps the downgrade from being silent.
func WatchHeadChanges(ctx context.Context, log log.Logger, src NewHeadSource, fn HeadSignalFn) (ethereum.Subscription, error) {
	watchHeaders := func() (ethereum.Subscription, error) {
		return watchHeadChanges(
			func(ch chan<- *types.Header) (ethereum.Subscription, error) {
				return src.SubscribeNewHead(ctx, ch)
			},
			func(header *types.Header) L1BlockRef {
				return L1BlockRef{
					Hash:       header.Hash(),
					Number:     header.Number.Uint64(),
					ParentHash: header.ParentHash,
					Time:       header.Time,
				}
			},
			fn,
		)
	}

	refSrc, ok := src.(NewHeadBlockRefSource)
	if !ok {
		return watchHeaders()
	}

	sub, err := watchHeadChanges(
		func(ch chan<- L1BlockRef) (ethereum.Subscription, error) {
			return refSrc.SubscribeNewHeadBlockRef(ctx, ch)
		},
		func(ref L1BlockRef) L1BlockRef { return ref },
		fn,
	)
	if err == nil {
		return sub, nil
	}
	log.Warn("block-ref head subscription unavailable, falling back to locally decoded headers; "+
		"head block hashes will be wrong if the chain uses header fields this build does not know",
		"err", err)
	return watchHeaders()
}

// watchHeadChanges runs the head-subscription loop over any element type, converting each
// received value to an L1BlockRef with toRef before handing it to fn.
func watchHeadChanges[T any](
	subscribe func(chan<- T) (ethereum.Subscription, error),
	toRef func(T) L1BlockRef,
	fn HeadSignalFn,
) (ethereum.Subscription, error) {
	headChanges := make(chan T, 10)
	sub, err := subscribe(headChanges)
	if err != nil {
		return nil, err
	}
	return event.NewSubscription(func(quit <-chan struct{}) error {
		eventsCtx, eventsCancel := context.WithCancel(context.Background())
		defer sub.Unsubscribe()
		defer eventsCancel()

		// We can handle a quit signal while fn is running, by closing the ctx.
		go func() {
			select {
			case <-quit:
				eventsCancel()
			case <-eventsCtx.Done(): // don't wait for quit signal if we closed for other reasons.
				return
			}
		}()

		for {
			select {
			case v := <-headChanges:
				fn(eventsCtx, toRef(v))
			case <-eventsCtx.Done():
				return nil
			case err := <-sub.Err(): // if the underlying subscription fails, stop
				return err
			}
		}
	}), nil
}

type L1BlockRefsSource interface {
	L1BlockRefByLabel(ctx context.Context, label BlockLabel) (L1BlockRef, error)
}

// PollBlockChanges opens a polling loop to fetch the L1 block reference with the given label,
// on provided interval and with request timeout. Results are returned with provided callback fn,
// which may block to pause/back-pressure polling.
func PollBlockChanges(log log.Logger, src L1BlockRefsSource, fn HeadSignalFn,
	label BlockLabel, interval time.Duration, timeout time.Duration) ethereum.Subscription {
	return event.NewSubscription(func(quit <-chan struct{}) error {
		if interval <= 0 {
			log.Warn("polling of block is disabled", "interval", interval, "label", label)
			<-quit
			return nil
		}
		eventsCtx, eventsCancel := context.WithCancel(context.Background())
		defer eventsCancel()
		// We can handle a quit signal while fn is running, by closing the ctx.
		go func() {
			select {
			case <-quit:
				eventsCancel()
			case <-eventsCtx.Done(): // don't wait for quit signal if we closed for other reasons.
				return
			}
		}()

		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				reqCtx, reqCancel := context.WithTimeout(eventsCtx, timeout)
				ref, err := src.L1BlockRefByLabel(reqCtx, label)
				reqCancel()
				if err != nil {
					log.Warn("failed to poll L1 block", "label", label, "err", err)
				} else {
					fn(eventsCtx, ref)
				}
			case <-eventsCtx.Done():
				return nil
			}
		}
	})
}
