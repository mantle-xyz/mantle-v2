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
	Baseline *Client
	Target   *Client
}

// CompareResult contains both responses to a comparison request.
type CompareResult struct {
	Request          *Request
	BaselineResponse *ResponseWithMeta
	TargetResponse   *ResponseWithMeta
}

// ResponseWithMeta pairs a response with request metadata.
type ResponseWithMeta struct {
	Response *Response
	RawBody  []byte
	Duration time.Duration
	Error    error // transport error, not a JSON-RPC error
}

// SuccessfulResponse reports whether a request received a successful JSON-RPC response.
func SuccessfulResponse(resp *ResponseWithMeta) bool {
	return resp != nil && resp.Error == nil && resp.Response != nil && resp.Response.Error == nil
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
func NewClientPair(baselineURL, baselineName, targetURL, targetName string, timeout time.Duration) *ClientPair {
	return &ClientPair{
		Baseline: NewClient(baselineURL, baselineName, timeout),
		Target:   NewClient(targetURL, targetName, timeout),
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
		result.Error = fmt.Errorf("encode request: %w", err)
		return result
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", c.url, bytes.NewReader(reqBody))
	if err != nil {
		result.Error = fmt.Errorf("create HTTP request: %w", err)
		return result
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		result.Error = fmt.Errorf("HTTP request failed: %w", err)
		result.Duration = time.Since(start)
		return result
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		result.Error = fmt.Errorf("read response: %w", err)
		result.Duration = time.Since(start)
		return result
	}

	result.RawBody = body
	result.Duration = time.Since(start)
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		result.Error = fmt.Errorf("HTTP status %d", resp.StatusCode)
		return result
	}
	if err := validateResponse(body, req.ID); err != nil {
		result.Error = fmt.Errorf("invalid JSON-RPC response: %w", err)
		return result
	}

	var rpcResp Response
	if err := json.Unmarshal(body, &rpcResp); err != nil {
		result.Error = fmt.Errorf("decode response: %w (body: %s)", err, string(body))
		return result
	}

	result.Response = &rpcResp
	return result
}

func validateResponse(body []byte, requestID int) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil {
		return err
	}
	if fields == nil {
		return fmt.Errorf("response is not an object")
	}
	var version string
	if err := json.Unmarshal(fields["jsonrpc"], &version); err != nil || version != "2.0" {
		return fmt.Errorf("jsonrpc must be 2.0")
	}
	var responseID int
	if err := json.Unmarshal(fields["id"], &responseID); err != nil || responseID != requestID {
		return fmt.Errorf("response id does not match request")
	}
	result, hasResult := fields["result"]
	rpcError, hasError := fields["error"]
	if hasResult == hasError {
		return fmt.Errorf("response must contain exactly one of result or error")
	}
	if hasResult {
		if len(result) == 0 {
			return fmt.Errorf("result is invalid")
		}
		return nil
	}
	var errorFields map[string]json.RawMessage
	if err := json.Unmarshal(rpcError, &errorFields); err != nil || errorFields == nil {
		return fmt.Errorf("error must be an object")
	}
	var code int
	codeJSON := bytes.TrimSpace(errorFields["code"])
	if len(codeJSON) == 0 || bytes.Equal(codeJSON, []byte("null")) {
		return fmt.Errorf("error.code must be an integer")
	}
	if err := json.Unmarshal(codeJSON, &code); err != nil {
		return fmt.Errorf("error.code must be an integer")
	}
	var message string
	messageJSON := bytes.TrimSpace(errorFields["message"])
	if len(messageJSON) == 0 || messageJSON[0] != '"' {
		return fmt.Errorf("error.message must be a string")
	}
	if err := json.Unmarshal(messageJSON, &message); err != nil {
		return fmt.Errorf("error.message must be a string")
	}
	return nil
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
		result.BaselineResponse = cp.Baseline.Call(ctx, req)
	}()

	go func() {
		defer wg.Done()
		result.TargetResponse = cp.Target.Call(ctx, req)
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
		result.BaselineResponse = cp.Baseline.CallWithRetry(ctx, req, maxRetries, retryDelay)
	}()

	go func() {
		defer wg.Done()
		result.TargetResponse = cp.Target.CallWithRetry(ctx, req, maxRetries, retryDelay)
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
