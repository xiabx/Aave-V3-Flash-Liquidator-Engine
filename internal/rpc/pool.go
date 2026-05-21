package rpc

import (
	"aave_bot/pkg/abis"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
)

// FetchReservesList 调用 Pool 合约获取资产列表
func FetchReservesList(rpcClient *RPCClient, poolAddr string) ([]string, error) {
	parsedABI, err := abi.JSON(bytes.NewReader(abis.AaveV36Pool))
	if err != nil {
		return nil, fmt.Errorf("Pool ABI 解析失败: %w", err)
	}

	callDataBytes, err := parsedABI.Pack("getReservesList")
	if err != nil {
		return nil, fmt.Errorf("getReservesList 方法打包失败: %w", err)
	}
	dataPayload := "0x" + common.Bytes2Hex(callDataBytes)

	params := []interface{}{
		map[string]string{
			"to":   poolAddr,
			"data": dataPayload,
		},
		"latest",
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	rawResult, err := rpcClient.CallWithRetry(ctx, "eth_call", params)
	if err != nil {
		return nil, fmt.Errorf("fetchReservesList 永久失败: %w", err)
	}

	var rawHexResult string
	if err := json.Unmarshal(rawResult, &rawHexResult); err != nil {
		return nil, fmt.Errorf("fetchReservesList 结果反序列化失败: %w", err)
	}

	returnDataBytes, err := hexutil.Decode(rawHexResult)
	if err != nil {
		return nil, fmt.Errorf("十六进制结果解码失败: %w", err)
	}

	var reserves []common.Address
	err = parsedABI.UnpackIntoInterface(&reserves, "getReservesList", returnDataBytes)
	if err != nil {
		return nil, fmt.Errorf("储备资产数组解包失败: %w", err)
	}

	assetList := make([]string, len(reserves))
	for i, addr := range reserves {
		assetList[i] = strings.ToLower(addr.Hex())
	}

	return assetList, nil
}
