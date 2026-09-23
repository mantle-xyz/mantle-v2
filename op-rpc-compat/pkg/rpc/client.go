// Package rpc 提供 JSON-RPC 客户端功能
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

// Request 标准 JSON-RPC 2.0 请求
type Request struct {
	JSONRPC string      `json:"jsonrpc"`
	Method  string      `json:"method"`
	Params  interface{} `json:"params,omitempty"`
	ID      int         `json:"id"`
}

// Response 标准 JSON-RPC 2.0 响应
type Response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      interface{}     `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
}

// RPCError JSON-RPC 错误对象
type RPCError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

// Client RPC 客户端
type Client struct {
	httpClient *http.Client
	url        string
	name       string // 客户端名称，用于报告
}

// ClientPair 一对 RPC 客户端（用于对比）
type ClientPair struct {
	Primary   *Client // 通常是 geth
	Secondary *Client // 通常是 reth
}

// CompareResult 对比请求的结果
type CompareResult struct {
	Request          *Request
	PrimaryResponse  *ResponseWithMeta
	SecondaryResponse *ResponseWithMeta
}

// ResponseWithMeta 带元数据的响应
type ResponseWithMeta struct {
	Response *Response
	RawBody  []byte
	Duration time.Duration
	Error    error // HTTP/网络错误，非 RPC 错误
}

var requestID int64

// NewClient 创建新的 RPC 客户端
func NewClient(url, name string, timeout time.Duration) *Client {
	return &Client{
		httpClient: &http.Client{
			Timeout: timeout,
		},
		url:  url,
		name: name,
	}
}

// NewClientPair 创建客户端对
func NewClientPair(primaryURL, secondaryURL string, timeout time.Duration) *ClientPair {
	return &ClientPair{
		Primary:   NewClient(primaryURL, "geth", timeout),
		Secondary: NewClient(secondaryURL, "reth", timeout),
	}
}

// NewRequest 创建新的 RPC 请求
func NewRequest(method string, params interface{}) *Request {
	id := int(atomic.AddInt64(&requestID, 1))
	return &Request{
		JSONRPC: "2.0",
		Method:  method,
		Params:  params,
		ID:      id,
	}
}

// Call 发送 RPC 请求并返回响应
func (c *Client) Call(ctx context.Context, req *Request) *ResponseWithMeta {
	result := &ResponseWithMeta{}
	start := time.Now()

	// 序列化请求
	reqBody, err := json.Marshal(req)
	if err != nil {
		result.Error = fmt.Errorf("序列化请求失败: %w", err)
		return result
	}

	// 创建 HTTP 请求
	httpReq, err := http.NewRequestWithContext(ctx, "POST", c.url, bytes.NewReader(reqBody))
	if err != nil {
		result.Error = fmt.Errorf("创建 HTTP 请求失败: %w", err)
		return result
	}
	httpReq.Header.Set("Content-Type", "application/json")

	// 发送请求
	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		result.Error = fmt.Errorf("HTTP 请求失败: %w", err)
		result.Duration = time.Since(start)
		return result
	}
	defer resp.Body.Close()

	// 读取响应体
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		result.Error = fmt.Errorf("读取响应失败: %w", err)
		result.Duration = time.Since(start)
		return result
	}

	result.RawBody = body
	result.Duration = time.Since(start)

	// 解析 JSON-RPC 响应
	var rpcResp Response
	if err := json.Unmarshal(body, &rpcResp); err != nil {
		result.Error = fmt.Errorf("解析响应失败: %w (body: %s)", err, string(body))
		return result
	}

	result.Response = &rpcResp
	return result
}

// CallWithRetry 带重试的 RPC 请求
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

// Compare 同时向两个客户端发送相同请求并收集响应
func (cp *ClientPair) Compare(ctx context.Context, req *Request) *CompareResult {
	result := &CompareResult{
		Request: req,
	}

	var wg sync.WaitGroup
	wg.Add(2)

	// 并行发送请求
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

// CompareWithRetry 带重试的对比请求
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

// Name 返回客户端名称
func (c *Client) Name() string {
	return c.name
}

// URL 返回客户端 URL
func (c *Client) URL() string {
	return c.url
}

