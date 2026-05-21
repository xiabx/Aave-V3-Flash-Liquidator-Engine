package rpc

import (
	"aave_bot/pkg/abis"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math/big"

	"aave_bot/internal/models"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/rs/zerolog/log"
)

type Multicall3Call3 struct {
	Target       common.Address
	AllowFailure bool
	CallData     []byte
}

type Multicall3Result struct {
	Success    bool
	ReturnData []byte
}

// BatchFetchReserveData 打包 Multicall3 请求
func (r *RPCClient) BatchFetchReserveData(ctx context.Context, poolAddress string, assets []string, blockNumber uint64) (map[string]*models.GlobalIndex, map[string]*models.AssetConfig, error) {
	if len(assets) == 0 {
		return nil, nil, nil
	}

	parsedABI, err := abi.JSON(bytes.NewReader(abis.MulticallABI))
	if err != nil {
		return nil, nil, fmt.Errorf("Multicall ABI 解析失败: %w", err)
	}

	var calls []Multicall3Call3
	poolAddr := common.HexToAddress(poolAddress)

	for _, asset := range assets {
		assetAddr := common.HexToAddress(asset)
		callData, err := r.aaveV3ABI.Pack("getReserveData", assetAddr)
		if err != nil {
			return nil, nil, fmt.Errorf("资产 %s 的 getReserveData 方法打包失败: %w", asset, err)
		}

		calls = append(calls, Multicall3Call3{
			Target:       poolAddr,
			AllowFailure: true,
			CallData:     callData,
		})
	}

	payloadBytes, err := parsedABI.Pack("aggregate3", calls)
	if err != nil {
		return nil, nil, fmt.Errorf("aggregate3 方法打包失败: %w", err)
	}
	payloadHex := hexutil.Encode(payloadBytes)

	hexBlock := fmt.Sprintf("0x%x", blockNumber)
	params := []interface{}{
		map[string]string{
			"to":   r.multicallAddr.Hex(),
			"data": payloadHex,
		},
		hexBlock,
	}

	rawResult, err := r.CallWithRetry(ctx, "eth_call", params)
	if err != nil {
		return nil, nil, fmt.Errorf("Multicall RPC 调用失败: %w", err)
	}
	var resultHex string
	if err := json.Unmarshal(rawResult, &resultHex); err != nil {
		return nil, nil, fmt.Errorf("Multicall 结果反序列化失败: %w", err)
	}

	return r.decodeMulticallResultsSafe(resultHex, assets, blockNumber, parsedABI)
}

func (r *RPCClient) decodeMulticallResultsSafe(rawHex string, assets []string, blockNumber uint64, parsedABI abi.ABI) (map[string]*models.GlobalIndex, map[string]*models.AssetConfig, error) {
	returnDataBytes, err := hexutil.Decode(rawHex)
	if err != nil {
		return nil, nil, fmt.Errorf("十六进制返回数据解码失败: %w", err)
	}

	var results []Multicall3Result
	err = parsedABI.UnpackIntoInterface(&results, "aggregate3", returnDataBytes)
	if err != nil {
		return nil, nil, fmt.Errorf("返回数据解包失败: %w", err)
	}

	if len(results) != len(assets) {
		return nil, nil, fmt.Errorf("结果数量不匹配: 实际 %d, 期望 %d", len(results), len(assets))
	}

	indices := make(map[string]*models.GlobalIndex)
	configs := make(map[string]*models.AssetConfig)

	for i, res := range results {
		asset := assets[i]
		if !res.Success {
			log.Warn().Str("asset", asset).Msg("Aave getReserveData 对该资产调用失败（链上 revert）")
			continue
		}

		unpacked, err := r.aaveV3ABI.Unpack("getReserveData", res.ReturnData)
		if err != nil {
			log.Warn().Str("asset", asset).Err(err).Msg("getReserveData 返回数据解析失败")
			continue
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

		configBitmap := reserveData.Configuration.Data

		ltShifted := new(big.Int).Rsh(configBitmap, 16)
		lt := uint16(new(big.Int).And(ltShifted, big.NewInt(0xFFFF)).Uint64())

		bonusShifted := new(big.Int).Rsh(configBitmap, 32)
		liquidationBonus := uint16(new(big.Int).And(bonusShifted, big.NewInt(0xFFFF)).Uint64())

		decShifted := new(big.Int).Rsh(configBitmap, 48)
		decimals := new(big.Int).And(decShifted, big.NewInt(0xFF)).Uint64()

		feeShifted := new(big.Int).Rsh(configBitmap, 152)
		liquidationProtocolFee := uint16(new(big.Int).And(feeShifted, big.NewInt(0xFFFF)).Uint64())

		configs[asset] = &models.AssetConfig{
			Decimals:               decimals,
			LiquidationThreshold:   lt,
			LiquidationBonus:       liquidationBonus,
			LiquidationProtocolFee: liquidationProtocolFee,
			ReserveID:              reserveData.Id,
			ATokenAddress:          reserveData.ATokenAddress.Hex(),
			VTokenAddress:          reserveData.VariableDebtTokenAddress.Hex(),
		}

		indices[asset] = &models.GlobalIndex{
			AssetAddress:        asset,
			LiquidityIndex:      reserveData.LiquidityIndex,
			VariableBorrowIndex: reserveData.VariableBorrowIndex,
			LastUpdateBlock:     blockNumber,
		}
	}

	return indices, configs, nil
}

// BatchFetchEModeCategories 使用 Multicall3 批量获取 E-Mode 配置
func (r *RPCClient) BatchFetchEModeCategories(ctx context.Context, poolAddress string, categoryIds []uint8, blockNumber uint64) (map[uint8]*models.EModeCategory, error) {
	if len(categoryIds) == 0 {
		return nil, nil
	}

	multiABI, err := abi.JSON(bytes.NewReader(abis.MulticallABI))
	if err != nil {
		return nil, fmt.Errorf("Multicall ABI 解析失败: %w", err)
	}
	aaveABI, err := abi.JSON(bytes.NewReader(abis.AaveEModeABI))
	if err != nil {
		return nil, fmt.Errorf("Aave EMode ABI 解析失败: %w", err)
	}

	var calls []Multicall3Call3
	poolAddr := common.HexToAddress(poolAddress)

	var validIds []uint8
	for _, cid := range categoryIds {
		if cid == 0 {
			continue
		}
		validIds = append(validIds, cid)

		configData, _ := aaveABI.Pack("getEModeCategoryData", cid)
		calls = append(calls, Multicall3Call3{
			Target:       poolAddr,
			AllowFailure: true,
			CallData:     configData,
		})

		bitmapData, _ := aaveABI.Pack("getEModeCategoryCollateralBitmap", cid)
		calls = append(calls, Multicall3Call3{
			Target:       poolAddr,
			AllowFailure: true,
			CallData:     bitmapData,
		})
	}

	if len(validIds) == 0 {
		return make(map[uint8]*models.EModeCategory), nil
	}

	payloadBytes, err := multiABI.Pack("aggregate3", calls)
	if err != nil {
		return nil, fmt.Errorf("aggregate3 方法打包失败: %w", err)
	}

	hexBlock := fmt.Sprintf("0x%x", blockNumber)
	params := []interface{}{
		map[string]string{"to": r.multicallAddr.Hex(), "data": hexutil.Encode(payloadBytes)},
		hexBlock,
	}

	rawResult, err := r.CallWithRetry(ctx, "eth_call", params)
	if err != nil {
		return nil, fmt.Errorf("Multicall 调用失败: %w", err)
	}
	var resultHex string
	if err := json.Unmarshal(rawResult, &resultHex); err != nil {
		return nil, fmt.Errorf("Multicall 结果反序列化失败: %w", err)
	}

	returnDataBytes, err := hexutil.Decode(resultHex)
	if err != nil {
		return nil, fmt.Errorf("十六进制返回数据解码失败: %w", err)
	}

	var results []Multicall3Result
	if err = multiABI.UnpackIntoInterface(&results, "aggregate3", returnDataBytes); err != nil {
		return nil, fmt.Errorf("返回数据解包失败: %w", err)
	}

	categories := make(map[uint8]*models.EModeCategory)

	for i, cid := range validIds {
		configRes := results[i*2]
		bitmapRes := results[i*2+1]

		cat := &models.EModeCategory{
			CollateralBitmap: big.NewInt(0),
		}

		if !configRes.Success {
			revertReason := decodeRevertReason(configRes.ReturnData)
			log.Warn().Uint8("cid", cid).Str("reason", revertReason).Msg("getEModeCategoryData 链上调用 REVERTED")
		} else {
			unpackedConfig, err := r.aaveEModeABI.Unpack("getEModeCategoryData", configRes.ReturnData)
			if err != nil {
				log.Warn().Uint8("cid", cid).Err(err).Msg("getEModeCategoryData 返回数据解析失败")
			} else {
				emodeData := unpackedConfig[0].(struct {
					Ltv                  uint16         `json:"ltv"`
					LiquidationThreshold uint16         `json:"liquidationThreshold"`
					LiquidationBonus     uint16         `json:"liquidationBonus"`
					PriceSource          common.Address `json:"priceSource"`
					Label                string         `json:"label"`
				})
				cat.Ltv = emodeData.Ltv
				cat.LiquidationThreshold = emodeData.LiquidationThreshold
				cat.LiquidationBonus = emodeData.LiquidationBonus
				cat.PriceSource = emodeData.PriceSource.Hex()
			}
		}

		if !bitmapRes.Success {
			revertReason := decodeRevertReason(bitmapRes.ReturnData)
			log.Debug().Uint8("cid", cid).Str("reason", revertReason).Msg("getEModeCategoryCollateralBitmap 获取失败（该 Aave 版本可能缺少此函数）")
		} else {
			unpackedBitmap, err := r.aaveEModeABI.Unpack("getEModeCategoryCollateralBitmap", bitmapRes.ReturnData)
			if err != nil {
				log.Warn().Uint8("cid", cid).Err(err).Msg("getEModeCategoryCollateralBitmap 返回数据解析失败")
			} else {
				cat.CollateralBitmap = unpackedBitmap[0].(*big.Int)
			}
		}

		if configRes.Success {
			categories[cid] = cat
		}
	}

	return categories, nil
}

func decodeRevertReason(returnData []byte) string {
	if len(returnData) == 0 {
		return "空返回数据（函数选择器可能不存在）"
	}
	if len(returnData) >= 68 && bytes.Equal(returnData[0:4], common.FromHex("0x08c379a0")) {
		strLen := new(big.Int).SetBytes(returnData[36:68]).Uint64()
		if len(returnData) >= int(68+strLen) {
			return string(returnData[68 : 68+strLen])
		}
	}
	return "0x" + common.Bytes2Hex(returnData)
}
