// Package filters checks node-local filter behavior without comparing generated IDs.
package filters

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/ethereum-optimism/optimism/op-rpc-compat/pkg/rpc"
)

func IsCreateMethod(method string) bool {
	switch method {
	case "eth_newBlockFilter", "eth_newFilter", "eth_newPendingTransactionFilter":
		return true
	}
	return false
}

// Verify creates, queries, and removes one filter on each endpoint independently.
func Verify(ctx context.Context, pair *rpc.ClientPair, request *rpc.Request) (*rpc.CompareResult, error) {
	if !IsCreateMethod(request.Method) {
		return nil, fmt.Errorf("unsupported filter creation method %q", request.Method)
	}
	baseline, baselineErr := verifyEndpoint(ctx, pair.Baseline, request)
	target, targetErr := verifyEndpoint(ctx, pair.Target, request)
	return &rpc.CompareResult{
		Request: request, BaselineResponse: baseline, TargetResponse: target,
	}, errors.Join(wrapSide("baseline", baselineErr), wrapSide("target", targetErr))
}

func wrapSide(side string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", side, err)
}

func verifyEndpoint(ctx context.Context, client *rpc.Client, request *rpc.Request) (*rpc.ResponseWithMeta, error) {
	created := client.Call(ctx, request)
	if err := responseError(created); err != nil {
		return created, fmt.Errorf("create filter: %w", err)
	}
	var filterID string
	if err := json.Unmarshal(created.Response.Result, &filterID); err != nil ||
		!strings.HasPrefix(filterID, "0x") || len(filterID) <= 2 {
		return created, fmt.Errorf("create filter returned invalid ID")
	}

	var problems []error
	changes := client.Call(ctx, rpc.NewRequest("eth_getFilterChanges", []any{filterID}))
	if err := responseError(changes); err != nil {
		problems = append(problems, fmt.Errorf("query filter: %w", err))
	} else if result := bytes.TrimSpace(changes.Response.Result); len(result) == 0 || result[0] != '[' {
		problems = append(problems, fmt.Errorf("query filter returned a non-array result"))
	}

	uninstalled := client.Call(ctx, rpc.NewRequest("eth_uninstallFilter", []any{filterID}))
	if err := responseError(uninstalled); err != nil {
		problems = append(problems, fmt.Errorf("uninstall filter: %w", err))
	} else if !bytes.Equal(bytes.TrimSpace(uninstalled.Response.Result), []byte("true")) {
		problems = append(problems, fmt.Errorf("uninstall filter did not return true"))
	}
	return created, errors.Join(problems...)
}

func responseError(response *rpc.ResponseWithMeta) error {
	if response == nil {
		return fmt.Errorf("missing response")
	}
	if response.Error != nil {
		return response.Error
	}
	if response.Response == nil {
		return fmt.Errorf("missing JSON-RPC response")
	}
	if response.Response.Error != nil {
		return fmt.Errorf("RPC error %d: %s", response.Response.Error.Code, response.Response.Error.Message)
	}
	return nil
}
