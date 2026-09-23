// Package rpc provides JSON-RPC clients and paired response collection.
package rpc

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

// Request is a JSON-RPC 2.0 request.
type Request struct {
	JSONRPC string      `json:"jsonrpc"`
	Method  string      `json:"method"`
	Params  interface{} `json:"params,omitempty"`
	ID      int         `json:"id"`
}

// Response is a JSON-RPC 2.0 response.
type Response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      interface{}     `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
}

// RPCError is a JSON-RPC error object.
type RPCError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

// Client sends JSON-RPC requests to an endpoint.
type Client struct {
	httpClient *http.Client
	url        string
	name       string // logical name used in reports
}

// ClientPair holds the two clients being compared.
type ClientPair struct {
	Primary   *Client // typically geth in the source suite
	Secondary *Client // typically reth in the source suite
}

// CompareResult contains both responses to a comparison request.
type CompareResult struct {
	Request          *Request
	PrimaryResponse  *ResponseWithMeta
	SecondaryResponse *ResponseWithMeta
}

// ResponseWithMeta pairs a response with request metadata.
type ResponseWithMeta struct {
	Response *Response
	RawBody  []byte
	Duration time.Duration
	Error    error // transport error, not a JSON-RPC error
}

var requestID int64

// NewClient creates an RPC client.
func NewClient(url, name string, timeout time.Duration) *Client {
	return &Client{
		httpClient: &http.Client{
			Timeout: timeout,
		},
		url:  url,
		name: name,
	}
}

// NewClientPair creates a pair of RPC clients.
func NewClientPair(primaryURL, secondaryURL string, timeout time.Duration) *ClientPair {
	return &ClientPair{
		Primary:   NewClient(primaryURL, "geth", timeout),
		Secondary: NewClient(secondaryURL, "reth", timeout),
	}
}

// NewRequest creates a JSON-RPC request.
func NewRequest(method string, params interface{}) *Request {
	id := int(atomic.AddInt64(&requestID, 1))
	return &Request{
		JSONRPC: "2.0",
		Method:  method,
		Params:  params,
		ID:      id,
	}
}

// Call sends a JSON-RPC request and returns its response.
func (c *Client) Call(ctx context.Context, req *Request) *ResponseWithMeta {
	result := &ResponseWithMeta{}
	start := time.Now()

	reqBody, err := json.Marshal(req)
	if err != nil {
		result.Error = fmt.Errorf("序列化请求失败: %w", err)
		return result
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", c.url, bytes.NewReader(reqBody))
	if err != nil {
		result.Error = fmt.Errorf("创建 HTTP 请求失败: %w", err)
		return result
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		result.Error = fmt.Errorf("HTTP 请求失败: %w", err)
		result.Duration = time.Since(start)
		return result
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		result.Error = fmt.Errorf("读取响应失败: %w", err)
		result.Duration = time.Since(start)
		return result
	}

	result.RawBody = body
	result.Duration = time.Since(start)

	var rpcResp Response
	if err := json.Unmarshal(body, &rpcResp); err != nil {
		result.Error = fmt.Errorf("解析响应失败: %w (body: %s)", err, string(body))
		return result
	}

	result.Response = &rpcResp
	return result
}

// CallWithRetry retries a JSON-RPC request after transport failures.
func (c *Client) CallWithRetry(ctx context.Context, req *Request, maxRetries int, retryDelay time.Duration) *ResponseWithMeta {
	var result *ResponseWithMeta
	for i := 0; i <= maxRetries; i++ {
		result = c.Call(ctx, req)
		if result.Error == nil {
			return result
		}
		if i < maxRetries {
			time.Sleep(retryDelay)
		}
	}
	return result
}

// Compare sends the same request to both clients and collects their responses.
func (cp *ClientPair) Compare(ctx context.Context, req *Request) *CompareResult {
	result := &CompareResult{
		Request: req,
	}

	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		result.PrimaryResponse = cp.Primary.Call(ctx, req)
	}()

	go func() {
		defer wg.Done()
		result.SecondaryResponse = cp.Secondary.Call(ctx, req)
	}()

	wg.Wait()
	return result
}

// CompareWithRetry retries a paired comparison request.
func (cp *ClientPair) CompareWithRetry(ctx context.Context, req *Request, maxRetries int, retryDelay time.Duration) *CompareResult {
	result := &CompareResult{
		Request: req,
	}

	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		result.PrimaryResponse = cp.Primary.CallWithRetry(ctx, req, maxRetries, retryDelay)
	}()

	go func() {
		defer wg.Done()
		result.SecondaryResponse = cp.Secondary.CallWithRetry(ctx, req, maxRetries, retryDelay)
	}()

	wg.Wait()
	return result
}

// Name returns the client's logical name.
func (c *Client) Name() string {
	return c.name
}

// URL returns the client's endpoint URL.
func (c *Client) URL() string {
	return c.url
}
