package store

import (
	"aave_bot/internal/models"
	"math/big"
	"sync"
)

// Mu 返回读写锁引用
func (s *StateStore) Mu() *sync.RWMutex {
	return &s.mu
}

// GlobalIndices 返回全局指数映射的直接引用
func (s *StateStore) GlobalIndices() map[string]*models.GlobalIndex {
	return s.globalIndices
}

// UserPositions 返回用户仓位映射的直接引用
func (s *StateStore) UserPositions() map[string]*models.UserPosition {
	return s.userPositions
}

// Prices 返回价格映射的直接引用
func (s *StateStore) Prices() map[string]*big.Int {
	return s.prices
}

// AssetConfigs 返回资产配置映射的直接引用
func (s *StateStore) AssetConfigs() map[string]*models.AssetConfig {
	return s.assetConfigs
}

// AssetSubscribers 返回反向索引映射的直接引用
func (s *StateStore) AssetSubscribers() map[string]map[string]struct{} {
	return s.assetSubscribers
}

// RiskHeap 返回风险堆的指针
func (s *StateStore) RiskHeap() *models.RiskHeap {
	return &s.riskHeap
}

// HeapLookup 返回堆查找表
func (s *StateStore) HeapLookup() map[string]*models.UserRiskItem {
	return s.heapLookup
}

// EModeCategories 返回 E-Mode 分类配置映射
func (s *StateStore) EModeCategories() map[uint8]*models.EModeCategory {
	return s.eModeCategories
}

// AnchorBlock 返回 The Graph 锚定的基准区块高度
func (s *StateStore) AnchorBlock() uint64 {
	return s.anchorBlock
}
