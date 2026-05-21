package rpc

import (
	"context"
	"fmt"
	"math/big"
	"time"

	"github.com/rs/zerolog/log"

	"aave_bot/internal/models"
	"aave_bot/internal/store"
	"aave_bot/pkg/math"
)

type Bootstrapper struct {
	graphClient *GraphClient
	rpcClient   *RPCClient
	store       *store.StateStore
	poolAddress string
}

func NewBootstrapper(graph *GraphClient, rpc *RPCClient, store *store.StateStore, poolAddress string) *Bootstrapper {
	return &Bootstrapper{
		graphClient: graph,
		rpcClient:   rpc,
		store:       store,
		poolAddress: poolAddress,
	}
}

func parseAndCleanBigInt(value string) *big.Int {
	num, ok := new(big.Int).SetString(value, 10)
	if !ok {
		return big.NewInt(0)
	}
	if num.Sign() < 0 || new(big.Int).Abs(num).Cmp(big.NewInt(math.DustThreshold)) <= 0 {
		return big.NewInt(0)
	}
	return num
}

func (b *Bootstrapper) Run(ctx context.Context, targetAssets []string) error {
	start := time.Now()
	log.Info().Msg("开始执行引导同步流程")

	anchorBlock, err := b.graphClient.GetAnchorBlock(ctx)
	if err != nil {
		return fmt.Errorf("获取锚点区块失败: %w", err)
	}
	b.store.SetAnchorBlock(anchorBlock)
	log.Info().Uint64("block", anchorBlock).Msg("基线同步锚点已确立")

	indices, configs, err := b.rpcClient.BatchFetchReserveData(ctx, b.poolAddress, targetAssets, anchorBlock)
	if err != nil {
		return fmt.Errorf("原子状态拉取失败: %w", err)
	}

	for asset, idx := range indices {
		log.Debug().Str("asset", asset).Interface("idx", idx).Msg("拉取流动性指数")
		b.store.SetGlobalIndex(asset, idx)
	}
	for asset, cfg := range configs {
		log.Debug().Str("asset", asset).Interface("cfg", cfg).Msg("拉取资产配置")
		b.store.SetAssetConfig(asset, cfg)
	}
	log.Info().Int("assets_count", len(indices)).Interface("GlobalIndex", b.store.GlobalIndices()).Interface("AssetConfig", b.store.AssetConfigs()).Msg("全局储备状态已拉取完成")

	users, err := b.graphClient.GetAllUsersAtBlock(ctx, anchorBlock)
	if err != nil {
		return fmt.Errorf("并发用户数据拉取失败: %w", err)
	}

	emodeCatSet := make(map[uint8]bool)
	for _, u := range users {
		catID := u.GetEModeID()
		if catID != 0 {
			emodeCatSet[catID] = true
		}
	}

	var catIds []uint8
	for cid := range emodeCatSet {
		catIds = append(catIds, cid)
	}

	if len(catIds) > 0 {
		log.Info().Int("emode_categories_count", len(catIds)).Msg("通过 Multicall3 批量拉取链上 E-Mode 分类")
		emodeData, err := b.rpcClient.BatchFetchEModeCategories(ctx, b.poolAddress, catIds, anchorBlock)
		if err != nil {
			return fmt.Errorf("E-Mode 分类批量拉取失败: %w", err)
		}

		for cid, cat := range emodeData {
			log.Debug().Uint8("cid", cid).Interface("cat", cat).Msg("拉取 E-Mode 分类数据")
			b.store.SetEModeCategory(cid, cat)
		}
	}

	totalUserReserves := 0
	for _, u := range users {
		log.Info().Interface("u", u).Msg("The Graph 用户数据")
		b.store.SetUserEMode(u.ID, u.GetEModeID())

		for _, res := range u.Reserves {
			aTokenBal := parseAndCleanBigInt(res.ScaledATokenBalance)
			varDebtBal := parseAndCleanBigInt(res.ScaledVariableDebt)

			if aTokenBal.Sign() == 0 && varDebtBal.Sign() == 0 {
				continue
			}

			b.store.UpsertUserReserve(u.ID, &models.UserReserve{
				AssetAddress:                   res.Reserve.UnderlyingAsset,
				ScaledATokenBalance:            aTokenBal,
				ScaledVariableDebt:             varDebtBal,
				UsageAsCollateralEnabledOnUser: res.UsageAsCollateralEnabledOnUser,
			})
			totalUserReserves++
		}
	}

	duration := time.Since(start)
	log.Info().
		Int("users_count", len(users)).
		Int("total_reserves", totalUserReserves).
		Dur("elapsed", duration).
		Msg("引导同步完成，状态存储已灌入数据")

	return nil
}
