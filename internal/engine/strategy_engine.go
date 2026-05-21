package engine

import (
	"math/big"

	"github.com/rs/zerolog/log"

	"aave_bot/internal/models"
	"aave_bot/internal/store"
	"aave_bot/pkg/math"
)

// LiquidationRecipe 包含执行一次清算所需的所有精确参数
type LiquidationRecipe struct {
	UserAddress          string
	DebtAsset            string
	CollateralAsset      string
	DebtToCover          *big.Int // 需要闪电贷借入的精确数量 (包含资产精度)
	ExpectedSeizedAmount *big.Int // 清算人实际可获得的抵押物数量 (已扣除协议费，包含资产精度)
	ExpectedProfitUSD    *big.Int // 预期净利润 (USD 价值，8位精度)
	LiquidationBonusBps  uint16   // 实际使用的清算奖励 (BPS)
}

// StrategyEngine 清算策略计算引擎
type StrategyEngine struct {
	store *store.StateStore
}

func NewStrategyEngine(s *store.StateStore) *StrategyEngine {
	return &StrategyEngine{store: s}
}

// V3.6 常量映射 (基于 Base 链 USD 计价，8位精度)
var (
	CloseFactorHfThreshold         = new(big.Int).Mul(big.NewInt(95), new(big.Int).Exp(big.NewInt(10), big.NewInt(16), nil)) // 0.95e18
	MinBaseMaxCloseFactorThreshold = big.NewInt(2000_00000000)                                                               // 2000 USD
	MinLeftoverBase                = big.NewInt(1000_00000000)                                                               // 1000 USD
	DefaultLiquidationCloseFactor  = big.NewInt(5000)                                                                        // 50%
	PercentageFactor               = big.NewInt(10000)                                                                       // 100% in BPS
)

// CalculateOptimalRecipe 计算给定高危用户的最优清算路径
func (s *StrategyEngine) CalculateOptimalRecipe(userAddr string, position *models.UserPosition, hf *big.Int) *LiquidationRecipe {
	s.store.Mu().RLock()
	defer s.store.Mu().RUnlock()

	var bestRecipe *LiquidationRecipe
	highestProfit := big.NewInt(0)

	// 闪电贷费率
	flashloanFeeBps := big.NewInt(5)

	type assetData struct {
		address   string
		balance   *big.Int
		price     *big.Int
		decimals  uint64
		reserveID uint16
	}

	var debts []assetData
	var collaterals []assetData
	totalDebtInBase := big.NewInt(0)
	totalCollateralInBase := big.NewInt(0)

	for assetAddr, reserve := range position.Reserves {
		price := s.store.Prices()[assetAddr]
		idx := s.store.GlobalIndices()[assetAddr]
		cfg := s.store.AssetConfigs()[assetAddr]

		if price == nil || idx == nil || cfg == nil {
			continue
		}

		if reserve.ScaledVariableDebt.Sign() > 0 {
			actualDebt := math.RayMul(reserve.ScaledVariableDebt, idx.VariableBorrowIndex)
			debts = append(debts, assetData{assetAddr, actualDebt, price, cfg.Decimals, cfg.ReserveID})

			debtBase := valueInBaseCurrency(actualDebt, price, cfg.Decimals)
			totalDebtInBase.Add(totalDebtInBase, debtBase)
		}

		if reserve.UsageAsCollateralEnabledOnUser && reserve.ScaledATokenBalance.Sign() > 0 {
			actualCol := math.RayMul(reserve.ScaledATokenBalance, idx.LiquidityIndex)
			collaterals = append(collaterals, assetData{assetAddr, actualCol, price, cfg.Decimals, cfg.ReserveID})

			colBase := valueInBaseCurrency(actualCol, price, cfg.Decimals)
			totalCollateralInBase.Add(totalCollateralInBase, colBase)
		}
	}

	for _, debt := range debts {
		debtBase := valueInBaseCurrency(debt.balance, debt.price, debt.decimals)

		maxLiquidatableDebt := new(big.Int).Set(debt.balance) // 默认允许 100% 清算

		// 如果总抵押和总债务均 >= 2000 USD，且 HF > 0.95，则触发 50% 限制
		if totalCollateralInBase.Cmp(MinBaseMaxCloseFactorThreshold) >= 0 &&
			totalDebtInBase.Cmp(MinBaseMaxCloseFactorThreshold) >= 0 &&
			hf.Cmp(CloseFactorHfThreshold) > 0 {

			// 计算全局默认允许清算的债务基准 (总债务的 50%)
			totalDefaultLiquidatableBase := new(big.Int).Mul(totalDebtInBase, DefaultLiquidationCloseFactor)
			totalDefaultLiquidatableBase.Div(totalDefaultLiquidatableBase, PercentageFactor)

			// 如果当前单项债务的价值超过了总债务的 50%，则该单项债务只能被清算 totalDefaultLiquidatableBase等值的数量
			if debtBase.Cmp(totalDefaultLiquidatableBase) > 0 {
				debtUnit := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(debt.decimals)), nil)
				maxLiquidatableDebt = new(big.Int).Mul(totalDefaultLiquidatableBase, debtUnit)
				maxLiquidatableDebt.Div(maxLiquidatableDebt, debt.price)
			}
		}

		for _, col := range collaterals {
			colBase := valueInBaseCurrency(col.balance, col.price, col.decimals)

			maxLiquidatableDebt := new(big.Int).Set(debt.balance)

			if colBase.Cmp(MinBaseMaxCloseFactorThreshold) >= 0 &&
				debtBase.Cmp(MinBaseMaxCloseFactorThreshold) >= 0 &&
				hf.Cmp(CloseFactorHfThreshold) > 0 {

				totalDefaultLiquidatableBase := new(big.Int).Mul(totalDebtInBase, DefaultLiquidationCloseFactor)
				totalDefaultLiquidatableBase.Div(totalDefaultLiquidatableBase, PercentageFactor)

				if debtBase.Cmp(totalDefaultLiquidatableBase) > 0 {
					debtUnit := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(debt.decimals)), nil)
					maxLiquidatableDebt = new(big.Int).Mul(totalDefaultLiquidatableBase, debtUnit)
					maxLiquidatableDebt.Div(maxLiquidatableDebt, debt.price)
				}
			}

			// 确定实际生效的 Liquidation Bonus 和 Protocol Fee
			bonusBps := uint16(0)
			protocolFeeBps := uint16(0)

			cfg := s.store.AssetConfigs()[col.address]
			if cfg != nil {
				bonusBps = cfg.LiquidationBonus
				protocolFeeBps = cfg.LiquidationProtocolFee
			}

			// E-Mode 覆盖逻辑
			if position.EModeFetched && position.EModeCategoryId != 0 {
				eModeCat := s.store.EModeCategories()[position.EModeCategoryId]
				if eModeCat != nil {
					debtBit := new(big.Int).And(new(big.Int).Rsh(eModeCat.CollateralBitmap, uint(debt.reserveID)), big.NewInt(1))
					colBit := new(big.Int).And(new(big.Int).Rsh(eModeCat.CollateralBitmap, uint(col.reserveID)), big.NewInt(1))

					if debtBit.Cmp(big.NewInt(1)) == 0 && colBit.Cmp(big.NewInt(1)) == 0 {
						bonusBps = eModeCat.LiquidationBonus
					}
				}
			}

			debtUnit := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(debt.decimals)), nil)
			colUnit := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(col.decimals)), nil)
			bonusFactor := big.NewInt(int64(bonusBps))

			baseCollateral := new(big.Int).Mul(debt.price, maxLiquidatableDebt)
			baseCollateral.Mul(baseCollateral, colUnit)
			denom := new(big.Int).Mul(col.price, debtUnit)
			baseCollateral.Div(baseCollateral, denom)

			maxCollateralToLiquidate := new(big.Int).Mul(baseCollateral, bonusFactor)
			maxCollateralToLiquidate.Div(maxCollateralToLiquidate, PercentageFactor)

			actualDebtToCover := new(big.Int).Set(maxLiquidatableDebt)
			actualCollateralToLiquidate := new(big.Int).Set(maxCollateralToLiquidate)

			if maxCollateralToLiquidate.Cmp(col.balance) > 0 {
				// 抵押物不足以完全覆盖，将耗尽该抵押物，并反推实际能偿还的债务 (PercentDivCeil)
				actualCollateralToLiquidate.Set(col.balance)

				debtNeededBase := new(big.Int).Mul(col.price, actualCollateralToLiquidate)
				debtNeededBase.Mul(debtNeededBase, debtUnit)
				debtNeededDenom := new(big.Int).Mul(debt.price, colUnit)
				debtNeededBase.Div(debtNeededBase, debtNeededDenom)

				actualDebtToCover = percentDivCeil(debtNeededBase, bonusFactor)
			}

			// 扣除协议费用
			liquidationProtocolFee := big.NewInt(0)
			if protocolFeeBps > 0 {
				bonusPortion := new(big.Int).Mul(actualCollateralToLiquidate, PercentageFactor)
				bonusPortion.Div(bonusPortion, bonusFactor)
				bonusCollateral := new(big.Int).Sub(actualCollateralToLiquidate, bonusPortion)

				liquidationProtocolFee.Mul(bonusCollateral, big.NewInt(int64(protocolFeeBps)))
				liquidationProtocolFee.Div(liquidationProtocolFee, PercentageFactor)
			}

			// 清算人实际入账的抵押物
			botReceivesCollateral := new(big.Int).Sub(actualCollateralToLiquidate, liquidationProtocolFee)

			totalColLiquidated := new(big.Int).Add(actualCollateralToLiquidate, liquidationProtocolFee)
			if actualDebtToCover.Cmp(debt.balance) < 0 && totalColLiquidated.Cmp(col.balance) < 0 {

				leftoverDebt := new(big.Int).Sub(debt.balance, actualDebtToCover)
				leftoverDebtBase := valueInBaseCurrency(leftoverDebt, debt.price, debt.decimals)

				leftoverCol := new(big.Int).Sub(col.balance, actualCollateralToLiquidate)
				leftoverColBase := valueInBaseCurrency(leftoverCol, col.price, col.decimals)

				// 如果任一残留资产价值小于 1000 USD，合约会直接 Revert，必须跳过此组合
				if leftoverDebtBase.Cmp(MinLeftoverBase) < 0 || leftoverColBase.Cmp(MinLeftoverBase) < 0 {
					continue
				}
			}
			costFactor := big.NewInt(int64(10000 + flashloanFeeBps.Int64()))
			debtCostAmt := new(big.Int).Mul(actualDebtToCover, costFactor)
			debtCostAmt.Div(debtCostAmt, PercentageFactor)
			costUSD := valueInBaseCurrency(debtCostAmt, debt.price, debt.decimals)

			// 提取纯闪电贷费用用于日志展示
			flashloanFeeAmt := new(big.Int).Mul(actualDebtToCover, flashloanFeeBps)
			flashloanFeeAmt.Div(flashloanFeeAmt, PercentageFactor)
			flashloanFeeUSD := valueInBaseCurrency(flashloanFeeAmt, debt.price, debt.decimals)

			idealRevenueUSD := valueInBaseCurrency(botReceivesCollateral, col.price, col.decimals)
			estimatedDexSlippageBps := big.NewInt(50)
			slippageFactor := big.NewInt(int64(10000 - estimatedDexSlippageBps.Int64()))
			actualRevenueUSD := new(big.Int).Mul(idealRevenueUSD, slippageFactor)
			actualRevenueUSD.Div(actualRevenueUSD, PercentageFactor)

			// 提取纯滑点损失用于日志展示
			slippageLossUSD := new(big.Int).Sub(idealRevenueUSD, actualRevenueUSD)

			grossProfitUSD := new(big.Int).Sub(actualRevenueUSD, costUSD)
			// 1000000 = 0.01 USD (8位精度)
			estimatedGasCostUSD := big.NewInt(1000000)
			netProfitUSD := new(big.Int).Sub(grossProfitUSD, estimatedGasCostUSD)

			formatUSD := func(val *big.Int) float64 {
				f, _ := new(big.Float).SetInt(val).Float64()
				return f / 1e8
			}

			debtAddrSub := debt.address
			if len(debtAddrSub) > 8 {
				debtAddrSub = debtAddrSub[:8] + "..."
			}
			colAddrSub := col.address
			if len(colAddrSub) > 8 {
				colAddrSub = colAddrSub[:8] + "..."
			}

			log.Debug().
				Str("debt", debtAddrSub).
				Str("col", colAddrSub).
				Str("coverDebt", actualDebtToCover.String()).
				Str("seizeCol", botReceivesCollateral.String()).
				Uint16("bonusBPS", bonusBps).
				Float64("idealRev", formatUSD(idealRevenueUSD)).
				Float64("slipLoss", formatUSD(slippageLossUSD)).
				Float64("actualRev", formatUSD(actualRevenueUSD)).
				Float64("debtCost", formatUSD(valueInBaseCurrency(actualDebtToCover, debt.price, debt.decimals))).
				Float64("flashFee", formatUSD(flashloanFeeUSD)).
				Float64("gasEst", formatUSD(estimatedGasCostUSD)).
				Float64("grossProfit", formatUSD(grossProfitUSD)).
				Float64("netProfit", formatUSD(netProfitUSD)).
				Msg("资产组合已测算")

			if netProfitUSD.Cmp(highestProfit) > 0 {
				highestProfit.Set(netProfitUSD)
				bestRecipe = &LiquidationRecipe{
					UserAddress:          userAddr,
					DebtAsset:            debt.address,
					CollateralAsset:      col.address,
					DebtToCover:          actualDebtToCover,
					ExpectedSeizedAmount: botReceivesCollateral,
					ExpectedProfitUSD:    netProfitUSD,
					LiquidationBonusBps:  bonusBps,
				}
			}
		}
	}

	if bestRecipe != nil {
		debtAddrSub := bestRecipe.DebtAsset
		if len(debtAddrSub) > 8 {
			debtAddrSub = debtAddrSub[:8]
		}
		colAddrSub := bestRecipe.CollateralAsset
		if len(colAddrSub) > 8 {
			colAddrSub = colAddrSub[:8]
		}

		log.Info().
			Float64("profitUSD", float64(bestRecipe.ExpectedProfitUSD.Int64())/1e8).
			Str("combo", debtAddrSub+" -> "+colAddrSub).
			Msg("找到最优清算路径")
	} else {
		log.Info().Msg("未找到可盈利的组合（净利润 <= 0），放弃本次清算")
	}

	return bestRecipe
}

// valueInBaseCurrency 计算资产金额对应的 USD 价值 (返回 8 位精度)
func valueInBaseCurrency(amount, price *big.Int, decimals uint64) *big.Int {
	unit := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(decimals)), nil)
	val := new(big.Int).Mul(amount, price)
	return val.Div(val, unit)
}

func percentDivCeil(value, percentage *big.Int) *big.Int {
	if value.Sign() == 0 || percentage.Sign() == 0 {
		return big.NewInt(0)
	}
	num := new(big.Int).Mul(value, PercentageFactor)
	num.Add(num, percentage)
	num.Sub(num, big.NewInt(1))
	return num.Div(num, percentage)
}
