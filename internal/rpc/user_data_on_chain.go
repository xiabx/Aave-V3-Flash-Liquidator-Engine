package rpc

import (
	"aave_bot/internal/models"
	"aave_bot/internal/store"
	"aave_bot/pkg/abis"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"reflect"
	"strings"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
)

var (
	poolABI      abi.ABI
	tokenABI     abi.ABI
	multicallABI abi.ABI
)

func init() {
	var err error
	poolABI, err = abi.JSON(bytes.NewReader(abis.AaveV36Pool))
	if err != nil {
		panic(fmt.Errorf("内嵌文件的 Aave Pool ABI 解析失败: %v", err))
	}

	tokenABI, err = abi.JSON(bytes.NewReader(abis.ATokenABI))
	if err != nil {
		panic(fmt.Errorf("aToken ABI 解析失败: %v", err))
	}

	multicallABI, err = abi.JSON(bytes.NewReader(abis.MulticallABI))
	if err != nil {
		panic(fmt.Errorf("Multicall ABI 解析失败: %v", err))
	}
}

func (r *RPCClient) FetchUserPositionBatchAtBlock(
	ctx context.Context,
	poolAddress string,
	userAddress string,
	blockNum uint64,
	stateStore *store.StateStore,
) (*models.UserAccountData, *models.UserPosition, error) {

	userCommon := common.HexToAddress(userAddress)
	poolCommon := common.HexToAddress(poolAddress)
	blockParam := "latest"
	if blockNum > 0 {
		blockParam = fmt.Sprintf("0x%x", blockNum)
	}

	stateStore.Mu().RLock()
	configs := stateStore.AssetConfigs()
	type assetMeta struct {
		AssetAddress  string
		ATokenAddress string
		VTokenAddress string
		ReserveIndex  uint16
	}
	assets := make([]assetMeta, 0, len(configs))
	for assetAddr, cfg := range configs {
		assets = append(assets, assetMeta{
			AssetAddress:  assetAddr,
			ATokenAddress: cfg.ATokenAddress,
			VTokenAddress: cfg.VTokenAddress,
			ReserveIndex:  cfg.ReserveID,
		})
	}
	stateStore.Mu().RUnlock()

	calls := make([]Multicall3Call3, 0, 3+len(assets)*2)

	calls = append(calls, Multicall3Call3{
		Target:       poolCommon,
		AllowFailure: false,
		CallData:     packABIData(poolABI, "getUserAccountData", userCommon),
	})
	calls = append(calls, Multicall3Call3{
		Target:       poolCommon,
		AllowFailure: false,
		CallData:     packABIData(poolABI, "getUserEMode", userCommon),
	})
	calls = append(calls, Multicall3Call3{
		Target:       poolCommon,
		AllowFailure: false,
		CallData:     packABIData(poolABI, "getUserConfiguration", userCommon),
	})

	for _, asset := range assets {
		calls = append(calls, Multicall3Call3{
			Target:       common.HexToAddress(asset.ATokenAddress),
			AllowFailure: false,
			CallData:     packABIData(tokenABI, "scaledBalanceOf", userCommon),
		})
		calls = append(calls, Multicall3Call3{
			Target:       common.HexToAddress(asset.VTokenAddress),
			AllowFailure: false,
			CallData:     packABIData(tokenABI, "scaledBalanceOf", userCommon),
		})
	}

	multicallData, err := multicallABI.Pack("aggregate3", calls)
	if err != nil {
		return nil, nil, fmt.Errorf("Multicall aggregate3 数据打包失败: %w", err)
	}

	params := []interface{}{
		map[string]string{
			"to":   r.multicallAddr.Hex(),
			"data": hexutil.Encode(multicallData),
		},
		blockParam,
	}

	rawResult, err := r.CallWithRetry(ctx, "eth_call", params)
	if err != nil {
		return nil, nil, fmt.Errorf("Multicall RPC 调用失败: %w", err)
	}
	var resultHex string
	if err := json.Unmarshal(rawResult, &resultHex); err != nil {
		return nil, nil, fmt.Errorf("Multicall 结果反序列化失败: %w", err)
	}

	resBytes, err := hexutil.Decode(resultHex)
	if err != nil {
		return nil, nil, fmt.Errorf("Multicall 十六进制结果解码失败: %w", err)
	}

	var returnDatas []Multicall3Result
	err = multicallABI.UnpackIntoInterface(&returnDatas, "aggregate3", resBytes)
	if err != nil {
		return nil, nil, fmt.Errorf("Multicall3 返回数据接口解包失败: %w", err)
	}

	if len(returnDatas) != len(calls) {
		return nil, nil, fmt.Errorf("Multicall3 返回数量不匹配: 期望 %d, 实际 %d", len(calls), len(returnDatas))
	}

	if !returnDatas[0].Success {
		return nil, nil, fmt.Errorf("getUserAccountData 链上调用失败")
	}
	acctRaw, err := unpackSafeBytes(&poolABI, "getUserAccountData", returnDatas[0].ReturnData)
	if err != nil {
		return nil, nil, fmt.Errorf("账户数据解包失败: %w", err)
	}
	acctData := &models.UserAccountData{
		TotalCollateralBase: acctRaw[0].(*big.Int),
		TotalDebtBase:       acctRaw[1].(*big.Int),
		HealthFactor:        acctRaw[5].(*big.Int),
	}

	if !returnDatas[1].Success {
		return nil, nil, fmt.Errorf("getUserEMode 链上调用失败")
	}
	emodeRaw, err := unpackSafeBytes(&poolABI, "getUserEMode", returnDatas[1].ReturnData)
	if err != nil {
		return nil, nil, fmt.Errorf("E-Mode 解包失败: %w", err)
	}
	eModeId := uint8(emodeRaw[0].(*big.Int).Uint64())

	if !returnDatas[2].Success {
		return nil, nil, fmt.Errorf("getUserConfiguration 链上调用失败")
	}

	configRaw, err := unpackSafeBytes(&poolABI, "getUserConfiguration", returnDatas[2].ReturnData)
	if err != nil {
		return nil, nil, fmt.Errorf("配置数据解包失败: %w", err)
	}

	val := reflect.ValueOf(configRaw[0])
	dataField := val.FieldByName("Data")
	if !dataField.IsValid() {
		return nil, nil, fmt.Errorf("意外 ABI 结构体: 缺少 Data 字段")
	}
	configData := dataField.Interface().(*big.Int)

	position := &models.UserPosition{
		UserAddress:     strings.ToLower(userAddress),
		Reserves:        make(map[string]*models.UserReserve),
		EModeCategoryId: eModeId,
		EModeFetched:    true,
	}

	idOffset := 3
	for i, asset := range assets {
		aTokenResult := returnDatas[idOffset+i*2]
		vTokenResult := returnDatas[idOffset+i*2+1]

		if !aTokenResult.Success || !vTokenResult.Success {
			return nil, nil, fmt.Errorf("资产 %s 的余额拉取失败", asset.AssetAddress)
		}

		aTokenUnpacked, err := unpackSafeBytes(&tokenABI, "scaledBalanceOf", aTokenResult.ReturnData)
		if err != nil {
			return nil, nil, fmt.Errorf("aToken %s 解包失败: %w", asset.AssetAddress, err)
		}

		vTokenUnpacked, err := unpackSafeBytes(&tokenABI, "scaledBalanceOf", vTokenResult.ReturnData)
		if err != nil {
			return nil, nil, fmt.Errorf("vToken %s 解包失败: %w", asset.AssetAddress, err)
		}

		bitIndex := uint(asset.ReserveIndex*2 + 1)
		isCollateral := new(big.Int).Rsh(configData, bitIndex).Bit(0) == 1

		position.Reserves[asset.AssetAddress] = &models.UserReserve{
			AssetAddress:                   asset.AssetAddress,
			ScaledATokenBalance:            aTokenUnpacked[0].(*big.Int),
			ScaledVariableDebt:             vTokenUnpacked[0].(*big.Int),
			UsageAsCollateralEnabledOnUser: isCollateral,
		}
	}

	return acctData, position, nil
}

func packABIData(a abi.ABI, method string, args ...interface{}) []byte {
	data, err := a.Pack(method, args...)
	if err != nil {
		panic(fmt.Errorf("方法 %s ABI 打包错误: %w", method, err))
	}
	return data
}

func unpackSafeBytes(a *abi.ABI, method string, data []byte) ([]interface{}, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("响应数据为空")
	}
	return a.Unpack(method, data)
}
