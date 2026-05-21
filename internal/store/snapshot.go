package store

import (
	"aave_bot/internal/models"
	"encoding/json"
	"math/big"
	"os"
)

// DumpToJSONFile 将 StateStore 的全量状态导出为格式化的 JSON 文件
func (s *StateStore) DumpToJSONFile(filepath string) error {
	s.mu.RLock()

	snapshot := struct {
		AnchorBlock      uint64                          `json:"anchorBlock"`
		CurrentBlock     uint64                          `json:"currentBlock"`
		GlobalIndices    map[string]*models.GlobalIndex  `json:"globalIndices"`
		UserPositions    map[string]*models.UserPosition `json:"userPositions"`
		Prices           map[string]*big.Int             `json:"prices"`
		AssetConfigs     map[string]*models.AssetConfig  `json:"assetConfigs"`
		AssetSubscribers map[string][]string             `json:"assetSubscribers"` // 转换为字符串切片提升可读性
		RiskHeap         models.RiskHeap                 `json:"riskHeap"`
		HeapLookup       map[string]*models.UserRiskItem `json:"heapLookup"`
		EModeCategories  map[uint8]*models.EModeCategory `json:"eModeCategories"`
	}{
		AnchorBlock:      s.anchorBlock,
		CurrentBlock:     s.currentBlock,
		GlobalIndices:    s.globalIndices,
		UserPositions:    s.userPositions,
		Prices:           s.prices,
		AssetConfigs:     s.assetConfigs,
		RiskHeap:         s.riskHeap,
		HeapLookup:       s.heapLookup,
		EModeCategories:  s.eModeCategories,
		AssetSubscribers: make(map[string][]string),
	}

	for asset, usersMap := range s.assetSubscribers {
		userList := make([]string, 0, len(usersMap))
		for userAddr := range usersMap {
			userList = append(userList, userAddr)
		}
		snapshot.AssetSubscribers[asset] = userList
	}

	jsonData, err := json.MarshalIndent(snapshot, "", "  ")

	s.mu.RUnlock()

	if err != nil {
		return err
	}

	return os.WriteFile(filepath, jsonData, 0644)
}
