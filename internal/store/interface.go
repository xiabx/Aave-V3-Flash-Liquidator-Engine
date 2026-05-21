package store

import (
	"aave_bot/internal/models"
	"math/big"
	"sync"
)

type StateProvider interface {
	Mu() *sync.RWMutex

	GetCurrentBlock() uint64
	GetAssetPrice(asset string) *big.Int
	GetAssetConfig(asset string) *models.AssetConfig
	GetGlobalIndex(asset string) *models.GlobalIndex
	GetUserReserve(userAddr, asset string) *models.UserReserve
	GetEModeCategory(categoryId uint8) *models.EModeCategory
	GetSubscribers(asset string) []string

	UserPositions() map[string]*models.UserPosition
	Prices() map[string]*big.Int
	AssetConfigs() map[string]*models.AssetConfig
	GlobalIndices() map[string]*models.GlobalIndex
	RiskHeap() *models.RiskHeap
	HeapLookup() map[string]*models.UserRiskItem

	SetCurrentBlock(block uint64)
}

var _ StateProvider = (*StateStore)(nil)
