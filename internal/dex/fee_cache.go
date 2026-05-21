package dex

import (
	"aave_bot/pkg/abis"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"strings"

	"aave_bot/internal/rpc"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/rs/zerolog"
)

type FeeCache struct {
	bestFees map[string]*big.Int // key: "token0-token1" (小写且排序), value: Fee (500, 3000)
	logger   zerolog.Logger
}

func NewFeeCache(logger zerolog.Logger) *FeeCache {
	return &FeeCache{
		bestFees: make(map[string]*big.Int),
		logger:   logger,
	}
}

// BuildCache 构建全局流动性最优费率表
func (fc *FeeCache) BuildCache(ctx context.Context, rpcClient *rpc.RPCClient, assets []string, factoryAddr, multicallAddr common.Address) error {
	fc.logger.Info().Msg("开始动态构建 DEX 费用缓存...")

	factoryABI, _ := abi.JSON(bytes.NewReader(abis.FactoryABI))
	erc20ABI, _ := abi.JSON(bytes.NewReader(abis.Erc20ABI))
	mcABI, _ := abi.JSON(bytes.NewReader(abis.MulticallABI))

	standardFees := []int64{100, 500, 3000, 10000} // 0.01%, 0.05%, 0.3%, 1%

	var pairs [][2]string
	for i := 0; i < len(assets); i++ {
		for j := i + 1; j < len(assets); j++ {
			t0, t1 := assets[i], assets[j]
			if strings.Compare(t0, t1) > 0 {
				t0, t1 = t1, t0
			}
			pairs = append(pairs, [2]string{t0, t1})
		}
	}

	var poolCalls []rpc.Multicall3Call3
	type poolMeta struct {
		pairKey string
		fee     *big.Int
		token0  common.Address
	}
	var metaTracker []poolMeta

	for _, pair := range pairs {
		t0Addr := common.HexToAddress(pair[0])
		t1Addr := common.HexToAddress(pair[1])
		pairKey := fmt.Sprintf("%s-%s", pair[0], pair[1])

		for _, fee := range standardFees {
			calldata, _ := factoryABI.Pack("getPool", t0Addr, t1Addr, big.NewInt(fee))
			poolCalls = append(poolCalls, rpc.Multicall3Call3{
				Target:       factoryAddr,
				AllowFailure: true,
				CallData:     calldata,
			})
			metaTracker = append(metaTracker, poolMeta{
				pairKey: pairKey,
				fee:     big.NewInt(fee),
				token0:  t0Addr,
			})
		}
	}

	poolResults, err := executeMulticall(ctx, rpcClient, mcABI, multicallAddr, poolCalls)
	if err != nil {
		return fmt.Errorf("failed stage 1 getPool multicall: %w", err)
	}

	var balanceCalls []rpc.Multicall3Call3
	var validPoolMeta []poolMeta

	for i, res := range poolResults {
		if !res.Success || len(res.ReturnData) == 0 {
			continue
		}
		var poolAddr common.Address
		err := factoryABI.UnpackIntoInterface(&poolAddr, "getPool", res.ReturnData)
		if err != nil || poolAddr == common.HexToAddress("0x0") {
			continue
		}

		calldata, _ := erc20ABI.Pack("balanceOf", poolAddr)
		balanceCalls = append(balanceCalls, rpc.Multicall3Call3{
			Target:       metaTracker[i].token0,
			AllowFailure: true,
			CallData:     calldata,
		})
		validPoolMeta = append(validPoolMeta, metaTracker[i])
	}

	if len(balanceCalls) == 0 {
		fc.logger.Warn().Msg("未找到任何 Aave 资产对对应的有效 Uniswap V3 池子")
		return nil
	}

	balanceResults, err := executeMulticall(ctx, rpcClient, mcABI, multicallAddr, balanceCalls)
	if err != nil {
		return fmt.Errorf("failed stage 2 balanceOf multicall: %w", err)
	}

	maxBalanceTracker := make(map[string]*big.Int)

	for i, res := range balanceResults {
		if !res.Success || len(res.ReturnData) == 0 {
			continue
		}
		var balance *big.Int
		err := erc20ABI.UnpackIntoInterface(&balance, "balanceOf", res.ReturnData)
		if err != nil {
			continue
		}

		meta := validPoolMeta[i]
		currentMax, exists := maxBalanceTracker[meta.pairKey]

		if !exists || balance.Cmp(currentMax) > 0 {
			maxBalanceTracker[meta.pairKey] = balance
			fc.bestFees[meta.pairKey] = meta.fee
		}
	}

	fc.logger.Info().Int("cached_pairs", len(fc.bestFees)).Msg("已基于 TVL 成功构建静态最优费率缓存")
	return nil
}

// GetOptimalFee 根据借贷/抵押币种获取最优费率
func (fc *FeeCache) GetOptimalFee(assetA, assetB string) (*big.Int, error) {
	t0, t1 := strings.ToLower(assetA), strings.ToLower(assetB)
	if strings.Compare(t0, t1) > 0 {
		t0, t1 = t1, t0
	}
	key := fmt.Sprintf("%s-%s", t0, t1)

	if fee, exists := fc.bestFees[key]; exists {
		return fee, nil
	}
	return nil, fmt.Errorf("no valid pool found for pair %s", key)
}

// executeMulticall 辅助函数
func executeMulticall(ctx context.Context, rpcClient *rpc.RPCClient, mcABI abi.ABI, mcAddr common.Address, calls []rpc.Multicall3Call3) ([]rpc.Multicall3Result, error) {
	calldata, err := mcABI.Pack("aggregate3", calls)
	if err != nil {
		return nil, err
	}

	params := []interface{}{
		map[string]string{
			"to":   mcAddr.Hex(),
			"data": hexutil.Encode(calldata),
		},
		"latest",
	}

	rawResult, err := rpcClient.CallWithRetry(ctx, "eth_call", params)
	if err != nil {
		return nil, err
	}

	var resultHex string
	if err := json.Unmarshal(rawResult, &resultHex); err != nil {
		return nil, fmt.Errorf("failed to unmarshal multicall result: %w", err)
	}

	resultBytes, err := hexutil.Decode(resultHex)
	if err != nil {
		return nil, err
	}

	var results []rpc.Multicall3Result
	err = mcABI.UnpackIntoInterface(&results, "aggregate3", resultBytes)
	return results, err
}
