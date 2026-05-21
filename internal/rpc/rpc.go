package rpc

import (
	"aave_bot/pkg/abis"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
)

// RPCClient 带有高可用多节点轮询机制的以太坊客户端
type RPCClient struct {
	client        *http.Client // HTTP 客户端
	rpcURLs       []string     // 支持多个节点的地址数组
	currentIndex  uint64
	multicallAddr common.Address
	aaveV3ABI     abi.ABI
	aaveEModeABI  abi.ABI
}

// NewRPCClient 初始化多节点 RPC 客户端
func NewRPCClient(urls []string, multicallAddr common.Address) *RPCClient {
	if len(urls) == 0 {
		panic("RPC URLs 列表不能为空")
	}

	aaveV3Parsed, err := abi.JSON(bytes.NewReader(abis.AaveV36Pool))
	if err != nil {
		panic(fmt.Sprintf("Aave V3 ABI 解析失败: %v", err))
	}

	aaveEModeParsed, err := abi.JSON(bytes.NewReader(abis.AaveEModeABI))
	if err != nil {
		panic(fmt.Sprintf("Aave EMode ABI 解析失败: %v", err))
	}

	return &RPCClient{
		client: &http.Client{
			Timeout: 10 * time.Second, // 全局超时控制
		},
		rpcURLs:       urls,
		currentIndex:  0,
		multicallAddr: multicallAddr,
		aaveV3ABI:     aaveV3Parsed,
		aaveEModeABI:  aaveEModeParsed,
	}
}

// GetNextURL 获取下一个可用的 RPC 节点地址
func (r *RPCClient) GetNextURL() string {
	idx := atomic.AddUint64(&r.currentIndex, 1)
	return r.rpcURLs[idx%uint64(len(r.rpcURLs))]
}

// Client 返回底层 HTTP 客户端引用
func (r *RPCClient) Client() *http.Client {
	return r.client
}

func (r *RPCClient) CallWithRetry(ctx context.Context, method string, params []interface{}) (json.RawMessage, error) {
	reqPayload := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  method,
		"params":  params,
	}
	reqBody, err := json.Marshal(reqPayload)
	if err != nil {
		return nil, fmt.Errorf("RPC 请求序列化失败: %w", err)
	}

	maxRetries := 5
	baseDelay := 200 * time.Millisecond
	var lastErr error
	var result struct {
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Message string `json:"message"`
		} `json:"error"`
	}

	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			time.Sleep(baseDelay * time.Duration(1<<(attempt-1)))
		}

		currentURL := r.GetNextURL()
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, currentURL, bytes.NewReader(reqBody))
		if err != nil {
			lastErr = err
			continue
		}
		req.Header.Set("Content-Type", "application/json")

		resp, err := r.client.Do(req)
		if err != nil {
			lastErr = err
			continue
		}

		respBytes, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			lastErr = err
			continue
		}

		if resp.StatusCode == http.StatusTooManyRequests {
			lastErr = fmt.Errorf("HTTP 429 请求频率过高，节点: %s", currentURL)
			continue
		}
		if resp.StatusCode >= 500 {
			lastErr = fmt.Errorf("HTTP %d 服务器错误，节点: %s", resp.StatusCode, currentURL)
			continue
		}

		err = json.Unmarshal(respBytes, &result)
		if err != nil {
			lastErr = fmt.Errorf("JSON 反序列化错误: %w (响应体: %s)", err, string(respBytes))
			continue
		}

		if result.Error != nil {
			lastErr = fmt.Errorf("节点 %s 交易回滚/错误: %s", currentURL, result.Error.Message)
			continue
		}

		return result.Result, nil
	}

	return nil, fmt.Errorf("RPC 调用经 %d 次重试后仍失败: %w", maxRetries, lastErr)
}
