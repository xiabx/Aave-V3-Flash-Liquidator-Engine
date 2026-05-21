package models

import (
	"math/big"
	"strconv"
	"strings"
)

// UserAccountData 映射链上 getUserAccountData 的返回值
// 用于预检阶段，验证本地计算结果的准确性
type UserAccountData struct {
	TotalCollateralBase *big.Int // 链上返回的总抵押品
	TotalDebtBase       *big.Int // 链上返回的总债务
	HealthFactor        *big.Int // 链上返回的健康因子
}

// RPCLog 映射以太坊日志结构
type RPCLog struct {
	Address          string   `json:"address"`          // 触发日志的合约地址
	Topics           []string `json:"topics"`           // Topic 数组 (索引 0 为事件签名)
	Data             string   `json:"data"`             // ABI 编码的非索引参数
	BlockNumber      string   `json:"blockNumber"`      // 区块号 (十六进制)
	TransactionHash  string   `json:"transactionHash"`  // 交易哈希
	TransactionIndex string   `json:"transactionIndex"` // 交易在区块中的索引
	LogIndex         string   `json:"logIndex"`         // 日志在交易中的索引
	Removed          bool     `json:"removed"`          // 是否因链重组被移除
}

type LogList []RPCLog

func (l LogList) Len() int { return len(l) }

func (l LogList) Swap(i, j int) { l[i], l[j] = l[j], l[i] }

func (l LogList) Less(i, j int) bool {
	blockI, _ := strconv.ParseUint(strings.TrimPrefix(l[i].BlockNumber, "0x"), 16, 64)
	blockJ, _ := strconv.ParseUint(strings.TrimPrefix(l[j].BlockNumber, "0x"), 16, 64)
	if blockI != blockJ {
		return blockI < blockJ
	}
	idxI, _ := strconv.ParseUint(strings.TrimPrefix(l[i].LogIndex, "0x"), 16, 64)
	idxJ, _ := strconv.ParseUint(strings.TrimPrefix(l[j].LogIndex, "0x"), 16, 64)
	return idxI < idxJ
}

// GraphUserResponse 映射 The Graph GraphQL 查询返回的用户数据结构
type GraphUserResponse struct {
	ID              string `json:"id"` // 用户地址
	EModeCategoryId *struct {
		ID string `json:"id"`
	} `json:"eModeCategoryId"`
	Reserves []struct {
		Reserve struct {
			UnderlyingAsset string `json:"underlyingAsset"` // 底层资产地址
		} `json:"reserve"`
		ScaledATokenBalance            string `json:"scaledATokenBalance"`            // Scaled aToken 余额 (十进制字符串)
		ScaledVariableDebt             string `json:"scaledVariableDebt"`             // Scaled 可变债务 (十进制字符串)
		UsageAsCollateralEnabledOnUser bool   `json:"usageAsCollateralEnabledOnUser"` // 是否作为抵押品
	} `json:"reserves"`
}

// 提取 ID 并转换为 uint8
func (g *GraphUserResponse) GetEModeID() uint8 {
	if g.EModeCategoryId == nil || g.EModeCategoryId.ID == "" {
		return 0
	}
	val, err := strconv.Atoi(g.EModeCategoryId.ID)
	if err != nil {
		return 0
	}
	return uint8(val)
}
