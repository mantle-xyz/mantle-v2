package rpc

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
)

// SharedBlockHash identifies a canonical block tag on both endpoints.
func SharedBlockHash(ctx context.Context, pair *ClientPair, tag string) (string, error) {
	if tag == "pending" {
		return "", fmt.Errorf("pending state has no shared canonical block snapshot")
	}
	baseline, err := blockHashForTag(ctx, pair.Baseline, tag)
	if err != nil {
		return "", fmt.Errorf("baseline %s snapshot: %w", tag, err)
	}
	target, err := blockHashForTag(ctx, pair.Target, tag)
	if err != nil {
		return "", fmt.Errorf("target %s snapshot: %w", tag, err)
	}
	if baseline != target {
		return "", fmt.Errorf("%s points to different blocks: %s vs %s", tag, baseline, target)
	}
	return baseline, nil
}

func blockHashForTag(ctx context.Context, client *Client, tag string) (string, error) {
	response := client.Call(ctx, NewRequest("eth_getBlockByNumber", []any{tag, false}))
	if response == nil || response.Error != nil || response.Response == nil || response.Response.Error != nil {
		return "", fmt.Errorf("cannot read block")
	}
	var block struct {
		Hash string `json:"hash"`
	}
	if err := json.Unmarshal(response.Response.Result, &block); err != nil ||
		len(block.Hash) != 66 || !strings.HasPrefix(block.Hash, "0x") {
		return "", fmt.Errorf("block has no canonical hash")
	}
	decoded, err := hex.DecodeString(block.Hash[2:])
	if err != nil || len(decoded) != 32 {
		return "", fmt.Errorf("block has no canonical hash")
	}
	return strings.ToLower(block.Hash), nil
}
