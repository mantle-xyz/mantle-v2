package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"strings"

	"github.com/ethereum-optimism/optimism/op-rpc-compat/pkg/report"
	"github.com/ethereum-optimism/optimism/op-rpc-compat/pkg/rpc"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
)

type endpointIdentity struct {
	metadata report.EndpointMetadata
	chainID  *big.Int
	genesis  common.Hash
}

func preflightEndpoints(ctx context.Context, pair *rpc.ClientPair) (report.EndpointMetadata, report.EndpointMetadata, error) {
	baseline, err := inspectEndpoint(ctx, pair.Baseline)
	if err != nil {
		return report.EndpointMetadata{}, report.EndpointMetadata{}, fmt.Errorf("%s preflight: %w", pair.Baseline.Name(), err)
	}
	target, err := inspectEndpoint(ctx, pair.Target)
	if err != nil {
		return report.EndpointMetadata{}, report.EndpointMetadata{}, fmt.Errorf("%s preflight: %w", pair.Target.Name(), err)
	}
	if baseline.chainID.Cmp(target.chainID) != 0 {
		return report.EndpointMetadata{}, report.EndpointMetadata{}, fmt.Errorf("chain ID mismatch: %s=%s, %s=%s",
			baseline.metadata.Name, baseline.chainID, target.metadata.Name, target.chainID)
	}
	if baseline.genesis != target.genesis {
		return report.EndpointMetadata{}, report.EndpointMetadata{}, fmt.Errorf("genesis hash mismatch: %s=%s, %s=%s",
			baseline.metadata.Name, baseline.genesis, target.metadata.Name, target.genesis)
	}
	return baseline.metadata, target.metadata, nil
}

func inspectEndpoint(ctx context.Context, client *rpc.Client) (endpointIdentity, error) {
	identity := endpointIdentity{metadata: report.EndpointMetadata{Name: client.Name(), URL: client.URL()}}
	chainResult, err := preflightResult(ctx, client, "eth_chainId", []any{})
	if err != nil {
		return identity, err
	}
	var chainHex string
	if err := json.Unmarshal(chainResult, &chainHex); err != nil {
		return identity, fmt.Errorf("eth_chainId returned invalid result: %w", err)
	}
	identity.chainID, err = hexutil.DecodeBig(chainHex)
	if err != nil {
		return identity, fmt.Errorf("eth_chainId returned invalid chain ID %q: %w", chainHex, err)
	}

	blockResult, err := preflightResult(ctx, client, "eth_getBlockByNumber", []any{"0x0", false})
	if err != nil {
		return identity, err
	}
	var block struct {
		Hash *common.Hash `json:"hash"`
	}
	if err := json.Unmarshal(blockResult, &block); err != nil || block.Hash == nil {
		return identity, fmt.Errorf("eth_getBlockByNumber returned invalid genesis block: %v", err)
	}
	identity.genesis = *block.Hash

	versionResult, err := preflightResult(ctx, client, "web3_clientVersion", []any{})
	if err != nil {
		return identity, err
	}
	if err := json.Unmarshal(versionResult, &identity.metadata.ClientVersion); err != nil || strings.TrimSpace(identity.metadata.ClientVersion) == "" {
		return identity, fmt.Errorf("web3_clientVersion returned invalid version: %v", err)
	}
	return identity, nil
}

func preflightResult(ctx context.Context, client *rpc.Client, method string, params []any) (json.RawMessage, error) {
	response := client.Call(ctx, rpc.NewRequest(method, params))
	if response.Error != nil {
		return nil, fmt.Errorf("%s: %w", method, response.Error)
	}
	if response.Response == nil {
		return nil, fmt.Errorf("%s returned no JSON-RPC response", method)
	}
	if response.Response.Error != nil {
		return nil, fmt.Errorf("%s RPC error %d: %s", method, response.Response.Error.Code, response.Response.Error.Message)
	}
	result := bytes.TrimSpace(response.Response.Result)
	if len(result) == 0 || bytes.Equal(result, []byte("null")) {
		return nil, fmt.Errorf("%s returned no result", method)
	}
	return result, nil
}
