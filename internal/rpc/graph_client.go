package rpc

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"aave_bot/internal/models"

	"github.com/rs/zerolog/log"
)

// GraphClient 封装 The Graph API 的 GraphQL 查询功能
type GraphClient struct {
	endpoint string
	apiKey   string
	client   *http.Client
}

func NewGraphClient(endpoint, apiKey string) *GraphClient {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.MaxIdleConns = 100
	t.MaxConnsPerHost = 100
	t.MaxIdleConnsPerHost = 100

	return &GraphClient{
		endpoint: endpoint,
		apiKey:   apiKey,
		client: &http.Client{
			Timeout:   30 * time.Second,
			Transport: t,
		},
	}
}

func (g *GraphClient) doQuery(ctx context.Context, query string, variables map[string]interface{}) ([]byte, error) {
	reqBody, err := json.Marshal(map[string]interface{}{"query": query, "variables": variables})
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, g.endpoint, bytes.NewBuffer(reqBody))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if g.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+g.apiKey)
	}

	resp, err := g.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Graph 查询失败，状态码: %d", resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}

// GetAnchorBlock 获取 The Graph 当前已同步的最新区块高度
func (g *GraphClient) GetAnchorBlock(ctx context.Context) (uint64, error) {
	query := `{ _meta { block { number } } }`
	respData, err := g.doQuery(ctx, query, nil)
	if err != nil {
		return 0, err
	}

	var result struct {
		Data struct {
			Meta struct {
				Block struct {
					Number uint64 `json:"number"`
				} `json:"block"`
			} `json:"_meta"`
		} `json:"data"`
	}

	if err := json.Unmarshal(respData, &result); err != nil {
		return 0, err
	}
	if result.Data.Meta.Block.Number == 0 {
		return 0, fmt.Errorf("_meta 区块号解析失败")
	}
	return result.Data.Meta.Block.Number, nil
}

func (g *GraphClient) fetchUserPartition(ctx context.Context, blockNumber uint64, prefixGt, prefixLt string, limit int) ([]models.GraphUserResponse, error) {
	query := `
	query GetActiveBorrowers($blockNumber: Int!, $lastId: String!, $prefixLt: String!, $limit: Int!) {
		users(
			block: { number: $blockNumber }
			first: $limit
			orderBy: id
			orderDirection: asc
			where: { id_gt: $lastId, id_lt: $prefixLt, borrowedReservesCount_gt: 0 }
		) {
			id
			eModeCategoryId {
						id
					}
			reserves {
				reserve { underlyingAsset }
				scaledATokenBalance
				scaledVariableDebt
				usageAsCollateralEnabledOnUser
			}
		}
	}`

	var allPartitionUsers []models.GraphUserResponse
	lastID := prefixGt

	for {
		variables := map[string]interface{}{
			"blockNumber": blockNumber,
			"lastId":      lastID,
			"prefixLt":    prefixLt,
			"limit":       limit,
		}

		var respData []byte
		var err error
		maxRetries := 3
		for attempt := 0; attempt < maxRetries; attempt++ {
			respData, err = g.doQuery(ctx, query, variables)
			if err == nil {
				break
			}
			time.Sleep(time.Duration(1<<attempt) * time.Second)
		}
		if err != nil {
			return nil, fmt.Errorf("分区 %s-%s 查询重试后仍然失败: %w", prefixGt, prefixLt, err)
		}

		var result struct {
			Data struct {
				Users []models.GraphUserResponse `json:"users"`
			} `json:"data"`
			Errors []interface{} `json:"errors"`
		}

		if err := json.Unmarshal(respData, &result); err != nil {
			return nil, err
		}
		if len(result.Errors) > 0 {
			return nil, fmt.Errorf("GraphQL 返回错误: %v", result.Errors)
		}

		users := result.Data.Users
		if len(users) == 0 {
			break
		}

		allPartitionUsers = append(allPartitionUsers, users...)
		lastID = users[len(users)-1].ID
	}

	return allPartitionUsers, nil
}

// GetAllUsersAtBlock 并发获取指定高度下的全量借款人状态
func (g *GraphClient) GetAllUsersAtBlock(ctx context.Context, blockNumber uint64) ([]models.GraphUserResponse, error) {
	partitions := []struct {
		gt string
		lt string
	}{
		{"0x0", "0x1"}, {"0x1", "0x2"},
		{"0x2", "0x3"}, {"0x3", "0x4"},
		{"0x4", "0x5"}, {"0x5", "0x6"}, {"0x6", "0x7"}, {"0x7", "0x8"},
		{"0x8", "0x9"}, {"0x9", "0xa"}, {"0xa", "0xb"}, {"0xb", "0xc"},
		{"0xc", "0xd"}, {"0xd", "0xe"}, {"0xe", "0xf"}, {"0xf", "0xffffffffffffffffffffffffffffffffffffffff"},
	}

	errChan := make(chan error, len(partitions))
	resultChan := make(chan []models.GraphUserResponse, len(partitions))

	fetchCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	limitPerPage := 1000

	for _, p := range partitions {
		go func(prefixGt, prefixLt string) {
			users, err := g.fetchUserPartition(fetchCtx, blockNumber, prefixGt, prefixLt, limitPerPage)
			if err != nil {
				errChan <- err
				cancel()
				return
			}
			resultChan <- users
		}(p.gt, p.lt)
	}

	var allUsers []models.GraphUserResponse
	var firstErr error

	for i := 0; i < len(partitions); i++ {
		select {
		case err := <-errChan:
			if firstErr == nil {
				firstErr = err
			}
		case users := <-resultChan:
			allUsers = append(allUsers, users...)
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}

	if firstErr != nil {
		return nil, firstErr
	}

	log.Debug().Int("total_users", len(allUsers)).Msg("已通过 The Graph 并发拉取全部用户数据")
	return allUsers, nil
}
