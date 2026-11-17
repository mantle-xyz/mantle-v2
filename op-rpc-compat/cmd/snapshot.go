package cmd

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/ethereum-optimism/optimism/op-rpc-compat/pkg/diff"
	"github.com/ethereum-optimism/optimism/op-rpc-compat/pkg/report"
	"github.com/ethereum-optimism/optimism/op-rpc-compat/pkg/rpc"
)

func caseDynamicTags(tc report.TestCase) []string {
	found := make(map[string]bool)
	for _, tag := range dynamicTags(tc.Params) {
		found[tag] = true
	}
	for _, tag := range tc.SnapshotTags {
		found[tag] = true
	}
	tags := make([]string, 0, len(found))
	for tag := range found {
		tags = append(tags, tag)
	}
	sort.Strings(tags)
	return tags
}

func dynamicTags(params any) []string {
	found := make(map[string]bool)
	var walk func(any)
	walk = func(value any) {
		switch v := value.(type) {
		case string:
			switch v {
			case "latest", "pending", "safe", "finalized":
				found[v] = true
			}
		case []any:
			for _, item := range v {
				walk(item)
			}
		case map[string]any:
			for _, item := range v {
				walk(item)
			}
		}
	}
	walk(params)
	tags := make([]string, 0, len(found))
	for tag := range found {
		tags = append(tags, tag)
	}
	sort.Strings(tags)
	return tags
}

func compareAtStableSnapshot(ctx context.Context, pair *rpc.ClientPair, request *rpc.Request, tags []string,
	attempts int, delay time.Duration) (*rpc.CompareResult, *diff.CompareResult, error, string) {
	for _, tag := range tags {
		if tag == "pending" {
			return nil, nil, nil, "pending state has no shared canonical block snapshot"
		}
	}
	if attempts < 1 {
		attempts = 1
	}
	var lastReason string
	var lastCompared *rpc.CompareResult
	for attempt := 0; attempt < attempts; attempt++ {
		before, err := snapshotFingerprint(ctx, pair, tags)
		if err != nil {
			lastReason = err.Error()
		} else {
			compared := pair.Compare(ctx, request)
			lastCompared = compared
			after, afterErr := snapshotFingerprint(ctx, pair, tags)
			if afterErr == nil && before == after {
				var differences *diff.CompareResult
				var compareErr error
				if rpc.SuccessfulResponse(compared.BaselineResponse) && rpc.SuccessfulResponse(compared.TargetResponse) {
					differences, compareErr = diff.Compare(compared.BaselineResponse.RawBody,
						compared.TargetResponse.RawBody, diff.DefaultOptions())
				}
				return compared, differences, compareErr, ""
			}
			lastReason = "block tag changed during comparison"
			if afterErr != nil {
				lastReason = afterErr.Error()
			}
		}
		if attempt+1 < attempts && delay > 0 {
			select {
			case <-ctx.Done():
				return lastCompared, nil, nil, ctx.Err().Error()
			case <-time.After(delay):
			}
		}
	}
	return lastCompared, nil, nil, lastReason
}

func snapshotFingerprint(ctx context.Context, pair *rpc.ClientPair, tags []string) (string, error) {
	var snapshot strings.Builder
	for _, tag := range tags {
		hash, err := rpc.SharedBlockHash(ctx, pair, tag)
		if err != nil {
			return "", err
		}
		fmt.Fprintf(&snapshot, "%s=%s;", tag, hash)
	}
	return snapshot.String(), nil
}
