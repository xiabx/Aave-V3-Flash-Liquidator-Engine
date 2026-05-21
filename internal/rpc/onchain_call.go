package rpc

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"strings"

	"aave_bot/internal/models"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
)

// FetchIndicesAtBlock 获取指定资产在特定高度的指数
func (r *RPCClient) FetchIndicesAtBlock(ctx context.Context, poolAddress, assetAddress string, blockNumber uint64) (*models.GlobalIndex, error) {
	dataPayload, err := r.aaveV3ABI.Pack("getReserveData", common.HexToAddress(assetAddress))
	if err != nil {
		return nil, fmt.Errorf("getReserveData 方法打包失败: %w", err)
	}

	hexBlock := fmt.Sprintf("0x%x", blockNumber)
	params := []interface{}{
		map[string]string{
			"to":   poolAddress,
			"data": hexutil.Encode(dataPayload),
		},
		hexBlock,
	}

	rawResult, err := r.CallWithRetry(ctx, "eth_call", params)
	if err != nil {
		return nil, err
	}
	var resHex string
	if err := json.Unmarshal(rawResult, &resHex); err != nil {
		return nil, fmt.Errorf("十六进制字符串结果反序列化失败: %w", err)
	}

	returnData, err := hexutil.Decode(resHex)
	if err != nil {
		return nil, fmt.Errorf("返回数据解码失败: %w", err)
	}

	unpacked, err := r.aaveV3ABI.Unpack("getReserveData", returnData)
	if err != nil {
		return nil, fmt.Errorf("getReserveData 返回数据解包失败: %w", err)
	}

	reserveData := unpacked[0].(struct {
		Configuration struct {
			Data *big.Int `json:"data"`
		} `json:"configuration"`
		LiquidityIndex              *big.Int       `json:"liquidityIndex"`
		CurrentLiquidityRate        *big.Int       `json:"currentLiquidityRate"`
		VariableBorrowIndex         *big.Int       `json:"variableBorrowIndex"`
		CurrentVariableBorrowRate   *big.Int       `json:"currentVariableBorrowRate"`
		CurrentStableBorrowRate     *big.Int       `json:"currentStableBorrowRate"`
		LastUpdateTimestamp         *big.Int       `json:"lastUpdateTimestamp"`
		Id                          uint16         `json:"id"`
		ATokenAddress               common.Address `json:"aTokenAddress"`
		StableDebtTokenAddress      common.Address `json:"stableDebtTokenAddress"`
		VariableDebtTokenAddress    common.Address `json:"variableDebtTokenAddress"`
		InterestRateStrategyAddress common.Address `json:"interestRateStrategyAddress"`
		AccruedToTreasury           *big.Int       `json:"accruedToTreasury"`
		Unbacked                    *big.Int       `json:"unbacked"`
		IsolationModeTotalDebt      *big.Int       `json:"isolationModeTotalDebt"`
	})

	return &models.GlobalIndex{
		AssetAddress:        assetAddress,
		LiquidityIndex:      reserveData.LiquidityIndex,
		VariableBorrowIndex: reserveData.VariableBorrowIndex,
		LastUpdateBlock:     blockNumber,
	}, nil
}

// FetchEModeCategory 获取指定 E-Mode 分类的配置信息
func (r *RPCClient) FetchEModeCategory(ctx context.Context, poolAddress string, categoryId uint8) (*models.EModeCategory, error) {
	configPayload, err := r.aaveEModeABI.Pack("getEModeCategoryData", categoryId)
	if err != nil {
		return nil, fmt.Errorf("getEModeCategoryData 方法打包失败: %w", err)
	}

	paramsConfig := []interface{}{
		map[string]string{"to": poolAddress, "data": hexutil.Encode(configPayload)}, "latest",
	}

	rawConfig, err := r.CallWithRetry(ctx, "eth_call", paramsConfig)
	if err != nil {
		return nil, fmt.Errorf("E-Mode 配置拉取失败: %w", err)
	}
	var configResHex string
	if err := json.Unmarshal(rawConfig, &configResHex); err != nil {
		return nil, fmt.Errorf("E-Mode 配置结果反序列化失败: %w", err)
	}

	configData, err := hexutil.Decode(configResHex)
	if err != nil {
		return nil, fmt.Errorf("E-Mode 配置数据解码失败: %w", err)
	}

	unpackedConfig, err := r.aaveEModeABI.Unpack("getEModeCategoryData", configData)
	if err != nil {
		return nil, fmt.Errorf("E-Mode 配置解包失败: %w", err)
	}

	emodeData := unpackedConfig[0].(struct {
		Ltv                  uint16         `json:"ltv"`
		LiquidationThreshold uint16         `json:"liquidationThreshold"`
		LiquidationBonus     uint16         `json:"liquidationBonus"`
		PriceSource          common.Address `json:"priceSource"`
		Label                string         `json:"label"`
	})

	bitmapPayload, err := r.aaveEModeABI.Pack("getEModeCategoryCollateralBitmap", categoryId)
	if err != nil {
		return nil, fmt.Errorf("getEModeCategoryCollateralBitmap 方法打包失败: %w", err)
	}

	paramsBitmap := []interface{}{
		map[string]string{"to": poolAddress, "data": hexutil.Encode(bitmapPayload)}, "latest",
	}

	rawBitmap, err := r.CallWithRetry(ctx, "eth_call", paramsBitmap)
	if err != nil {
		return nil, fmt.Errorf("E-Mode 位图拉取失败: %w", err)
	}
	var bitmapResHex string
	if err := json.Unmarshal(rawBitmap, &bitmapResHex); err != nil {
		return nil, fmt.Errorf("E-Mode 位图结果反序列化失败: %w", err)
	}

	bitmapData, err := hexutil.Decode(bitmapResHex)
	if err != nil {
		return nil, fmt.Errorf("E-Mode 位图数据解码失败: %w", err)
	}

	unpackedBitmap, err := r.aaveEModeABI.Unpack("getEModeCategoryCollateralBitmap", bitmapData)
	if err != nil {
		return nil, fmt.Errorf("E-Mode 位图解包失败: %w", err)
	}
	collateralBitmap := unpackedBitmap[0].(*big.Int)

	return &models.EModeCategory{
		Ltv:                  emodeData.Ltv,
		LiquidationThreshold: emodeData.LiquidationThreshold,
		LiquidationBonus:     emodeData.LiquidationBonus,
		PriceSource:          emodeData.PriceSource.Hex(),
		CollateralBitmap:     collateralBitmap,
	}, nil
}

// FetchUserEMode 获取用户当前开启的 E-Mode 分类 ID
func (r *RPCClient) FetchUserEMode(ctx context.Context, poolAddress, userAddress string) (uint8, error) {
	dataPayload, err := r.aaveV3ABI.Pack("getUserEMode", common.HexToAddress(userAddress))
	if err != nil {
		return 0, fmt.Errorf("getUserEMode 方法打包失败: %w", err)
	}

	params := []interface{}{
		map[string]string{"to": poolAddress, "data": hexutil.Encode(dataPayload)}, "latest",
	}

	rawResult, err := r.CallWithRetry(ctx, "eth_call", params)
	if err != nil {
		return 0, err
	}
	var resHex string
	if err := json.Unmarshal(rawResult, &resHex); err != nil {
		return 0, fmt.Errorf("用户 E-Mode 结果反序列化失败: %w", err)
	}

	hexData := strings.TrimPrefix(resHex, "0x")
	if hexData == "" {
		return 0, nil
	}
	val, _ := new(big.Int).SetString(hexData, 16)
	return uint8(val.Uint64()), nil
}
