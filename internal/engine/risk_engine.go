package engine

import (
	"container/heap"
	"context"
	"math/big"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"

	"aave_bot/internal/models"
	"aave_bot/internal/rpc"
	"aave_bot/internal/store"
	"aave_bot/pkg/math"
)

const (
	// minDebtThreshold 是考虑清算的最小债务金额（USD）。
	minDebtThreshold = 100000000 // 0.1 USD

	// watchdogTickInterval 检查边界用户的间隔。
	watchdogTickInterval = 2 * time.Second

	// watchdogHFThreshold 触发重新计算的健康因子阈值。
	watchdogHFThreshold = 102 // 1.02，稍后将应用 WAD 精度

	// sanityCheckTolerance 本地和链上健康因子差异的容差。
	sanityCheckTolerance = 1000 // 0.001 (0.1%)

	// liquidationCooldown 清算失败后的冷却期。
	liquidationCooldown = 10 * time.Second

	// noProfitCooldown 可清算但无利润仓位的冷却期。
	noProfitCooldown = 60 * time.Second
)

// RiskEngine 高风险用户的优先队列。
type RiskEngine struct {
	store          *store.StateStore
	taskQueue      chan string
	workerCount    int
	rpcClient      *rpc.RPCClient
	strategyEngine *StrategyEngine
	poolAddress    string
	txExecutor     *TxExecutor
}

func NewRiskEngine(store *store.StateStore, workerCount int, rpcClient *rpc.RPCClient, poolAddress string, strategyEngine *StrategyEngine, txExecutor *TxExecutor) *RiskEngine {
	return &RiskEngine{
		store:          store,
		taskQueue:      make(chan string, 50000),
		workerCount:    workerCount,
		rpcClient:      rpcClient,
		poolAddress:    poolAddress,
		strategyEngine: strategyEngine,
		txExecutor:     txExecutor,
	}
}

func (r *RiskEngine) watchdogLoop(ctx context.Context) {
	ticker := time.NewTicker(watchdogTickInterval)
	defer ticker.Stop()

	threshold := new(big.Int).Mul(big.NewInt(watchdogHFThreshold), math.WAD)
	threshold.Div(threshold, big.NewInt(100))

	for {
		select {
		case <-ctx.Done():
			log.Info().Msg("RiskEngine 看门狗已关闭")
			return
		case <-ticker.C:
			r.store.Mu().RLock()

			heapSlice := *r.store.RiskHeap()
			var targets []string

			now := time.Now().Unix()
			for _, item := range heapSlice {
				if item.HealthFactor != nil && item.HealthFactor.Cmp(threshold) <= 0 && now >= item.NextCheckAt {
					targets = append(targets, item.UserAddress)
				}
			}
			r.store.Mu().RUnlock()

			if len(targets) > 0 {
				log.Debug().Int("count", len(targets)).Msg("看门狗触发边界用户健康因子重算")
				for _, addr := range targets {
					r.Enqueue(addr)
				}
			}
		}
	}
}

func (r *RiskEngine) Start(ctx context.Context) {
	log.Info().Int("workers", r.workerCount).Msg("启动 RiskEngine 工作线程池...")
	heap.Init(r.store.RiskHeap())

	for i := 0; i < r.workerCount; i++ {
		go r.workerLoop(ctx, i)
	}
	go r.watchdogLoop(ctx)
}

func (r *RiskEngine) workerLoop(ctx context.Context, workerID int) {
	minDebt := big.NewInt(minDebtThreshold)

	for {
		select {
		case <-ctx.Done():
			return
		case userAddr := <-r.taskQueue:
			traceID := uuid.New().String()
			calcLog := log.With().Str("trace_id", traceID).Str("user", userAddr).Logger()

			snapshot := r.calculateHF(calcLog, userAddr)

			if snapshot == nil || snapshot.TotalDebtBase.Cmp(minDebt) < 0 {
				r.updateHeap(userAddr, nil)
				continue
			}

			if snapshot.HealthFactor.Cmp(math.HFLiquidationThreshold) < 0 {
				calcLog.Info().
					Int("worker", workerID).
					Uint64("block", snapshot.CalculatedAtBlock).
					Str("debt_usd", math.FormatUSD(snapshot.TotalDebtBase)).
					Msg("本地判定满足清算条件，发起链上验证")

				verifyCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
				acctDataChain, positionChain, err := r.rpcClient.FetchUserPositionBatchAtBlock(verifyCtx, r.poolAddress, userAddr, snapshot.CalculatedAtBlock, r.store)
				cancel()

				if err != nil {
					calcLog.Error().Err(err).Msg("链上验证 RPC 调用失败，放弃本轮清算")
					continue
				}

				hfDiff := new(big.Int).Sub(snapshot.HealthFactor, acctDataChain.HealthFactor)
				hfDiff.Abs(hfDiff)
				tolerance := new(big.Int).Div(math.WAD, big.NewInt(sanityCheckTolerance))

				isLiquidatableOnChain := acctDataChain.HealthFactor.Cmp(math.HFLiquidationThreshold) < 0

				// 基于链上数据（真实值）的决策逻辑。
				if isLiquidatableOnChain {
					if hfDiff.Cmp(tolerance) <= 0 {
						calcLog.Warn().Str("hf_chain", math.FormatHF(acctDataChain.HealthFactor)).Msg("状态完美同步，继续执行清算")
					} else {
						calcLog.Warn().
							Str("hf_local", math.FormatHF(snapshot.HealthFactor)).
							Str("hf_chain", math.FormatHF(acctDataChain.HealthFactor)).
							Msg("状态不同步，但目标在链上确实可清算")
					}

					r.store.Mu().Lock()
					r.store.UserPositions()[userAddr] = positionChain
					r.store.Mu().Unlock()

					recipe := r.strategyEngine.CalculateOptimalRecipe(userAddr, positionChain, acctDataChain.HealthFactor)

					if recipe != nil && recipe.ExpectedProfitUSD.Sign() > 0 {
						calcLog.Info().
							Str("debt", recipe.DebtAsset).
							Str("col", recipe.CollateralAsset).
							Str("profit_usd", math.FormatUSD(recipe.ExpectedProfitUSD)).
							Msg("最优清算路径已计算，推送至内存池")

						err := r.txExecutor.SendLiquidation(verifyCtx, recipe, calcLog)

						if err != nil {
							calcLog.Warn().Err(err).Msg("清算流水线中断，进入冷却期")

							r.store.Mu().Lock()
							if item, exists := r.store.HeapLookup()[userAddr]; exists {
								item.NextCheckAt = time.Now().Add(liquidationCooldown).Unix()
							}
							r.store.Mu().Unlock()

							r.updateHeap(userAddr, acctDataChain.HealthFactor)

						} else {
							calcLog.Info().Msg("清算交易已广播，施加乐观锁")
							fakeSafeHF := new(big.Int).Exp(big.NewInt(10), big.NewInt(20), nil) // 100.0
							r.updateHeap(userAddr, fakeSafeHF)
						}

					} else {
						calcLog.Warn().Msg("目标可清算，但未找到有利可图的路径，进入冷却期")

						r.store.Mu().Lock()
						if item, exists := r.store.HeapLookup()[userAddr]; exists {
							item.NextCheckAt = time.Now().Add(noProfitCooldown).Unix()
						}
						r.store.Mu().Unlock()

						r.updateHeap(userAddr, acctDataChain.HealthFactor)
					}
				} else {
					calcLog.Info().Str("hf_chain", math.FormatHF(acctDataChain.HealthFactor)).Msg("误报，目标在链上安全")
					r.store.Mu().Lock()
					r.store.UserPositions()[userAddr] = positionChain
					r.store.Mu().Unlock()
					r.updateHeap(userAddr, acctDataChain.HealthFactor)
				}
				continue
			}

			r.updateHeap(userAddr, snapshot.HealthFactor)
		}
	}
}

// calculateHF 计算用户的健康因子。
func (r *RiskEngine) calculateHF(calcLog zerolog.Logger, userAddr string) *models.RiskSnapshot {
	r.store.Mu().RLock()
	user, exists := r.store.UserPositions()[userAddr]
	if !exists {
		r.store.Mu().RUnlock()
		return nil
	}
	eModeFetched := user.EModeFetched
	eModeCategory := user.EModeCategoryId
	r.store.Mu().RUnlock()

	snapshot := r.computeHFMath(calcLog, userAddr, eModeFetched, eModeCategory)
	if snapshot == nil {
		return nil
	}

	minDebt := big.NewInt(minDebtThreshold)
	if snapshot.TotalDebtBase.Cmp(minDebt) < 0 {
		return snapshot
	}

	// 仅当用户处于风险状态时才延迟加载 E-Mode。
	if !eModeFetched && snapshot.HealthFactor.Cmp(math.HFSafetyThreshold) < 0 {
		calcLog.Debug().Msg("健康因子偏低，从链上延迟加载 E-Mode 信息")

		catId, err := r.rpcClient.FetchUserEMode(context.Background(), r.poolAddress, userAddr)
		if err == nil {
			if catId > 0 {
				r.store.Mu().RLock()
				_, catExists := r.store.EModeCategories()[catId]
				r.store.Mu().RUnlock()

				if !catExists {
					calcLog.Info().Uint8("catId", catId).Msg("延迟加载缺失的 E-Mode 分类配置")
					catConfig, errFetch := r.rpcClient.FetchEModeCategory(context.Background(), r.poolAddress, catId)
					if errFetch == nil && catConfig != nil {
						r.store.Mu().Lock()
						r.store.EModeCategories()[catId] = catConfig
						r.store.Mu().Unlock()
					} else {
						calcLog.Warn().Err(errFetch).Uint8("catId", catId).Msg("E-Mode 配置拉取失败，放弃本轮计算")
						return nil
					}
				}
			}

			r.store.Mu().Lock()
			user.EModeCategoryId = catId
			user.EModeFetched = true
			r.store.Mu().Unlock()

			snapshot = r.computeHFMath(calcLog, userAddr, true, catId)
			if catId > 0 {
				calcLog.Info().Uint8("eMode", catId).Msg("E-Mode 已激活，重新计算真实健康因子")
			}
		} else {
			calcLog.Warn().Err(err).Msg("E-Mode 延迟加载失败，放弃本轮计算")
			return nil
		}
	}

	return snapshot
}

// computeHFMath 执行实际的健康因子计算。
func (r *RiskEngine) computeHFMath(calcLog zerolog.Logger, userAddr string, eModeFetched bool, userEModeCat uint8) *models.RiskSnapshot {
	r.store.Mu().RLock()
	user := r.store.UserPositions()[userAddr]
	if user == nil {
		r.store.Mu().RUnlock()
		calcLog.Warn().Msg("内存中未找到用户持仓数据")
		return nil
	}

	pricesCopy := make(map[string]string)
	for k, v := range r.store.Prices() {
		if v != nil {
			pricesCopy[k] = v.String()
		}
	}

	indicesCopy := make(map[string]map[string]string)
	for k, v := range r.store.GlobalIndices() {
		if v != nil {
			indicesCopy[k] = map[string]string{
				"liqIdx":       v.LiquidityIndex.String(),
				"varBorrowIdx": v.VariableBorrowIndex.String(),
			}
		}
	}

	reservesCopy := make(map[string]map[string]string)
	for k, v := range user.Reserves {
		if v != nil {
			reservesCopy[k] = map[string]string{
				"aToken": v.ScaledATokenBalance.String(),
				"debt":   v.ScaledVariableDebt.String(),
			}
		}
	}
	userCopy := map[string]interface{}{
		"UserAddress": user.UserAddress,
		"Reserves":    reservesCopy,
	}
	r.store.Mu().RUnlock()

	rawCollateralBase := big.NewInt(0)
	totalCollateralBase := big.NewInt(0)
	totalDebtBase := big.NewInt(0)
	startBlock := r.store.GetCurrentBlock()

	calcLog.Info().
		Str("event", "hf_calc_start").
		Interface("user", userCopy).
		Uint64("block", startBlock).
		Interface("globalIndices", indicesCopy).
		Interface("prices", pricesCopy).
		Bool("eModeFetched", eModeFetched).
		Uint8("userEModeCat", userEModeCat).
		Msg("开始计算健康因子")

	r.store.Mu().RLock()
	defer r.store.Mu().RUnlock()

	for _, res := range user.Reserves {
		asset := res.AssetAddress

		if res.ScaledATokenBalance.Sign() == 0 && res.ScaledVariableDebt.Sign() == 0 {
			continue
		}

		price := r.store.Prices()[asset]
		idx := r.store.GlobalIndices()[asset]
		cfg := r.store.AssetConfigs()[asset]

		if price == nil || idx == nil || cfg == nil {
			calcLog.Warn().
				Str("asset", asset).
				Msg("资产状态数据缺失，终止健康因子计算以避免错误")
			return nil
		}

		assetUnit := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(cfg.Decimals)), nil)
		ltToUse := cfg.LiquidationThreshold

		if eModeFetched && userEModeCat != 0 {
			if eModeCategory, ok := r.store.EModeCategories()[userEModeCat]; ok {
				shifted := new(big.Int).Rsh(eModeCategory.CollateralBitmap, uint(cfg.ReserveID))
				bitCheck := new(big.Int).And(shifted, big.NewInt(1))

				if bitCheck.Cmp(big.NewInt(1)) == 0 {
					ltToUse = eModeCategory.LiquidationThreshold
				}
			}
		}

		if res.UsageAsCollateralEnabledOnUser && res.ScaledATokenBalance.Sign() > 0 {
			actualBal := math.RayMul(res.ScaledATokenBalance, idx.LiquidityIndex)
			value := new(big.Int).Mul(actualBal, price)
			value.Div(value, assetUnit)

			rawCollateralBase.Add(rawCollateralBase, value)

			colValueWithLT := new(big.Int).Mul(value, big.NewInt(int64(ltToUse)))
			totalCollateralBase.Add(totalCollateralBase, colValueWithLT)
		}

		if res.ScaledVariableDebt.Sign() > 0 {
			actualDebt := math.RayMul(res.ScaledVariableDebt, idx.VariableBorrowIndex)
			value := new(big.Int).Mul(actualDebt, price)
			value.Div(value, assetUnit)
			totalDebtBase.Add(totalDebtBase, value)
		}
	}

	if totalDebtBase.Sign() == 0 {
		return nil // 无债务，无清算风险。
	}

	hf := new(big.Int).Mul(totalCollateralBase, math.WAD)
	hf.Div(hf, big.NewInt(10000)) // 调整 BPS 缩放
	hf.Div(hf, totalDebtBase)

	impliedCurrentLTBps := big.NewInt(0)
	if rawCollateralBase.Sign() > 0 {
		impliedCurrentLTBps = new(big.Int).Mul(totalCollateralBase, big.NewInt(10000))
		impliedCurrentLTBps.Div(impliedCurrentLTBps, rawCollateralBase)
	}

	calcLog.Info().
		Str("event", "hf_calc_end").
		Str("healthFactor_formatted", math.FormatHF(hf)).
		Msg("健康因子计算完成")

	return &models.RiskSnapshot{
		HealthFactor:        hf,
		RawCollateralBase:   rawCollateralBase,
		TotalCollateralBase: totalCollateralBase,
		TotalDebtBase:       totalDebtBase,
		CalculatedAtBlock:   startBlock,
	}
}

// updateHeap 更新用户在风险堆中的位置。
// 如果健康因子高于安全阈值，则将用户从堆中移除。
func (r *RiskEngine) updateHeap(userAddr string, hf *big.Int) {
	r.store.Mu().Lock()
	defer r.store.Mu().Unlock()

	item, exists := r.store.HeapLookup()[userAddr]
	if hf == nil || hf.Cmp(math.HFSafetyThreshold) >= 0 {
		if exists {
			heap.Remove(r.store.RiskHeap(), item.Index)
			delete(r.store.HeapLookup(), userAddr)
		}
		return
	}

	if exists {
		item.HealthFactor = hf
		heap.Fix(r.store.RiskHeap(), item.Index)
	} else {
		newItem := &models.UserRiskItem{UserAddress: userAddr, HealthFactor: hf}
		heap.Push(r.store.RiskHeap(), newItem)
		r.store.HeapLookup()[userAddr] = newItem
	}
}

// Enqueue 将用户地址添加到任务队列。
func (r *RiskEngine) Enqueue(userAddr string) {
	select {
	case r.taskQueue <- userAddr:
	default:
	}
}

// EnqueueByAsset 将订阅特定资产的所有用户添加到任务队列。
func (r *RiskEngine) EnqueueByAsset(asset string) {
	users := r.store.GetSubscribers(asset)
	for _, u := range users {
		r.Enqueue(u)
	}
}
