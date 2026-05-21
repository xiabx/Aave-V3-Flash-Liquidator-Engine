package oracle

import (
	"aave_bot/pkg/abis"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"strings"
	"time"

	"aave_bot/internal/engine"
	"aave_bot/internal/rpc"
	"aave_bot/internal/store"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/rs/zerolog/log"
)

// OracleFeeder 高频价格喂价器，负责从链上预言机获取实时资产价格
type OracleFeeder struct {
	rpcClient     *rpc.RPCClient     // 多节点 RPC 客户端
	store         *store.StateStore  // 内存状态机 (用于存储/读取价格)
	oracleAddress string             // Aave V3 PriceOracle 合约地址 (小写)
	targetAssets  []string           // 目标监控资产地址列表
	riskEngine    *engine.RiskEngine // 风险计算引擎 (用于价格变动扇出)
	oracleABI     abi.ABI
}

// NewOracleFeeder 创建新的 OracleFeeder 实例
func NewOracleFeeder(rpcClient *rpc.RPCClient, store *store.StateStore, oracleAddress string, assets []string, riskEngine *engine.RiskEngine) *OracleFeeder {
	parsedABI, err := abi.JSON(bytes.NewReader(abis.OracleABI))
	if err != nil {
		log.Fatal().Err(err).Msg("预言机 ABI 解析失败")
	}

	return &OracleFeeder{
		rpcClient:     rpcClient,
		store:         store,
		oracleAddress: strings.ToLower(oracleAddress),
		targetAssets:  assets,
		riskEngine:    riskEngine,
		oracleABI:     parsedABI,
	}
}

// fetchAndStorePrices 执行一次完整的价格拉取与存储流程
// 返回值: error 为 nil 表示成功，否则表示 RPC 调用或解析失败
func (o *OracleFeeder) fetchAndStorePrices(ctx context.Context) error {
	start := time.Now()

	var assetAddresses []common.Address
	for _, asset := range o.targetAssets {
		assetAddresses = append(assetAddresses, common.HexToAddress(asset))
	}

	calldata, err := o.oracleABI.Pack("getAssetsPrices", assetAddresses)
	if err != nil {
		return fmt.Errorf("failed to pack getAssetsPrices: %w", err)
	}

	params := []interface{}{
		map[string]string{"to": o.oracleAddress, "data": hexutil.Encode(calldata)},
		"latest",
	}

	rawResult, err := o.rpcClient.CallWithRetry(ctx, "eth_call", params)
	if err != nil {
		return fmt.Errorf("oracle rpc request failed: %w", err)
	}

	var resultHex string
	if err := json.Unmarshal(rawResult, &resultHex); err != nil {
		return fmt.Errorf("decode oracle response failed: %w", err)
	}

	returnData, err := hexutil.Decode(resultHex)
	if err != nil {
		return fmt.Errorf("failed to decode return data: %w", err)
	}

	unpacked, err := o.oracleABI.Unpack("getAssetsPrices", returnData)
	if err != nil {
		return fmt.Errorf("failed to unpack prices: %w", err)
	}

	prices := unpacked[0].([]*big.Int)

	var changedAssets []string

	type priceRecord struct {
		Old string `json:"oldPrice"`
		New string `json:"newPrice"`
	}

	priceSummary := make(map[string]priceRecord)

	for i, asset := range o.targetAssets {
		if i < len(prices) {
			newPrice := prices[i]
			oldPrice := o.store.GetAssetPrice(asset)

			if oldPrice == nil || oldPrice.Cmp(newPrice) != 0 {
				o.store.SetAssetPrice(asset, newPrice)
				changedAssets = append(changedAssets, asset)

				oldPriceStr := "0"
				if oldPrice != nil {
					oldPriceStr = oldPrice.String()
				}
				priceSummary[asset] = priceRecord{
					Old: oldPriceStr,
					New: newPrice.String(),
				}
			}
		}
	}

	if len(priceSummary) > 0 {
		log.Info().
			Int("changedCount", len(changedAssets)).
			Interface("summary", priceSummary).
			Msg("资产价格已更新")
	}

	for _, asset := range changedAssets {
		o.riskEngine.EnqueueByAsset(asset)
		log.Info().Str("asset", asset).Msg("价格已更新（触发扇出扫描）")
	}

	log.Info().Str("latency", time.Since(start).String()).Msg("预言机心跳完成")
	return nil
}

func (o *OracleFeeder) Start(ctx context.Context) {
	log.Info().Msg("启动高频价格喂价器...")

	if err := o.fetchAndStorePrices(ctx); err != nil {
		log.Warn().Err(err).Msg("初始预言机价格拉取失败")
	}

	ticker := time.NewTicker(2 * time.Second)

	go func() {
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				log.Info().Msg("预言机引擎正在关闭...")
				return
			case <-ticker.C:
				fetchCtx, cancel := context.WithTimeout(ctx, 4000*time.Millisecond)
				err := o.fetchAndStorePrices(fetchCtx)
				cancel()

				if err != nil {
					log.Error().Err(err).Msg("预言机心跳出错")
				}
			}
		}
	}()
}
