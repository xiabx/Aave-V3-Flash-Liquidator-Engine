package engine

import (
	"math/big"
	"testing"

	"github.com/stretchr/testify/assert"

	"aave_bot/internal/models"
	"aave_bot/internal/store"
	"aave_bot/pkg/math"
)

func TestMathHelpers(t *testing.T) {
	t.Run("valueInBaseCurrency", func(t *testing.T) {
		tests := []struct {
			name     string
			amount   *big.Int
			price    *big.Int
			decimals uint64
			expected *big.Int
		}{
			{
				name:     "1 WETH at 3000 USD (18 decimals)",
				amount:   new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil), // 1e18
				price:    big.NewInt(3000_00000000),                             // 预言机价格（8 位精度）
				decimals: 18,
				expected: big.NewInt(3000_00000000),
			},
			{
				name:     "100 USDC at 1 USD (6 decimals)",
				amount:   new(big.Int).Exp(big.NewInt(10), big.NewInt(8), nil), // 100 * 10^6
				price:    big.NewInt(1_00000000),
				decimals: 6,
				expected: big.NewInt(100_00000000),
			},
		}

		for _, tc := range tests {
			t.Run(tc.name, func(t *testing.T) {
				actual := valueInBaseCurrency(tc.amount, tc.price, tc.decimals)
				assert.Equal(t, tc.expected.String(), actual.String(), "Base currency calculation mismatch")
			})
		}
	})

	t.Run("percentDivCeil", func(t *testing.T) {
		val := big.NewInt(1000)
		pct := big.NewInt(10500)

		expected := big.NewInt(953)
		actual := percentDivCeil(val, pct)

		assert.Equal(t, 0, expected.Cmp(actual), "PercentDivCeil calculation error")
	})
}

func TestCalculateOptimalRecipe_Basic(t *testing.T) {
	st := store.NewStateStore()

	wethAddr := "0xc02aaa39b223fe8d0a0e5c4f27ead9083c756cc2"
	usdcAddr := "0xa0b86991c6218b36c1d19d4a2e9eb0ce3606eb48"

	st.SetAssetPrice(wethAddr, big.NewInt(3000_00000000)) // 3000 USD
	st.SetAssetPrice(usdcAddr, big.NewInt(1_00000000))    // 1 USD

	// 初始化全局流动性指数与借款指数
	st.SetGlobalIndex(wethAddr, &models.GlobalIndex{
		AssetAddress:        wethAddr,
		LiquidityIndex:      math.RAY,
		VariableBorrowIndex: math.RAY,
	})
	st.SetGlobalIndex(usdcAddr, &models.GlobalIndex{
		AssetAddress:        usdcAddr,
		LiquidityIndex:      math.RAY,
		VariableBorrowIndex: math.RAY,
	})

	// 初始化资产静态配置
	st.SetAssetConfig(wethAddr, &models.AssetConfig{
		Decimals:               18,
		ReserveID:              1,
		LiquidationBonus:       10500,
		LiquidationProtocolFee: 1000,
	})
	st.SetAssetConfig(usdcAddr, &models.AssetConfig{
		Decimals:               6,
		ReserveID:              2,
		LiquidationBonus:       10500,
		LiquidationProtocolFee: 1000,
	})

	// 构造高危用户持仓 (抵押 WETH，借出 USDC)
	userAddr := "0xvulnerableuser"
	hf := big.NewInt(900000000000000000) // 0.9 HF

	colBalance := new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil)
	debtBalance := new(big.Int).Mul(big.NewInt(2800), new(big.Int).Exp(big.NewInt(10), big.NewInt(6), nil))

	pos := &models.UserPosition{
		UserAddress: userAddr,
		Reserves: map[string]*models.UserReserve{
			wethAddr: {
				AssetAddress:                   wethAddr,
				ScaledATokenBalance:            colBalance,
				UsageAsCollateralEnabledOnUser: true,
				ScaledVariableDebt:             big.NewInt(0),
			},
			usdcAddr: {
				AssetAddress:                   usdcAddr,
				ScaledATokenBalance:            big.NewInt(0),
				UsageAsCollateralEnabledOnUser: false,
				ScaledVariableDebt:             debtBalance,
			},
		},
	}

	engine := NewStrategyEngine(st)
	recipe := engine.CalculateOptimalRecipe(userAddr, pos, hf)

	assert.NotNil(t, recipe, "Expected a valid recipe, got nil")
	if recipe != nil {
		assert.Equal(t, usdcAddr, recipe.DebtAsset, "Wrong debt asset selected")
		assert.Equal(t, wethAddr, recipe.CollateralAsset, "Wrong collateral asset selected")
		assert.True(t, recipe.ExpectedProfitUSD.Sign() > 0, "Expected positive profit")
		assert.Equal(t, uint16(10500), recipe.LiquidationBonusBps, "Should use standard bonus")
	}
}
