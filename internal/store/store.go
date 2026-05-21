package store

import (
	"math/big"
	"strings"
	"sync"

	"aave_bot/internal/models"
)

// StateStore 为机器人状态提供线程安全的内存缓存。
type StateStore struct {
	mu               sync.RWMutex
	globalIndices    map[string]*models.GlobalIndex
	userPositions    map[string]*models.UserPosition
	anchorBlock      uint64
	prices           map[string]*big.Int
	assetConfigs     map[string]*models.AssetConfig
	assetSubscribers map[string]map[string]struct{}
	riskHeap         models.RiskHeap
	heapLookup       map[string]*models.UserRiskItem
	eModeCategories  map[uint8]*models.EModeCategory
	currentBlock     uint64
}

func (s *StateStore) GetAssetPrice(asset string) *big.Int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.prices[strings.ToLower(asset)]
}

func (s *StateStore) GetAssetConfig(asset string) *models.AssetConfig {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.assetConfigs[strings.ToLower(asset)]
}

func (s *StateStore) GetEModeCategory(categoryId uint8) *models.EModeCategory {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.eModeCategories[categoryId]
}

func NewStateStore() *StateStore {
	return &StateStore{
		globalIndices:    make(map[string]*models.GlobalIndex),
		userPositions:    make(map[string]*models.UserPosition),
		prices:           make(map[string]*big.Int),
		assetConfigs:     make(map[string]*models.AssetConfig),
		assetSubscribers: make(map[string]map[string]struct{}),
		riskHeap:         make(models.RiskHeap, 0),
		heapLookup:       make(map[string]*models.UserRiskItem),
		eModeCategories:  make(map[uint8]*models.EModeCategory),
	}
}

func (s *StateStore) SetCurrentBlock(block uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if block > s.currentBlock {
		s.currentBlock = block
	}
}

func (s *StateStore) GetCurrentBlock() uint64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.currentBlock
}

func (s *StateStore) SetAnchorBlock(block uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.anchorBlock = block
	if s.currentBlock == 0 {
		s.currentBlock = block
	}
}

func (s *StateStore) SetGlobalIndex(asset string, index *models.GlobalIndex) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.globalIndices[strings.ToLower(asset)] = index
}

func (s *StateStore) UpsertUserReserve(userAddr string, reserve *models.UserReserve) {
	s.mu.Lock()
	defer s.mu.Unlock()

	uAddr := strings.ToLower(userAddr)
	asset := strings.ToLower(reserve.AssetAddress)

	if _, exists := s.userPositions[uAddr]; !exists {
		s.userPositions[uAddr] = &models.UserPosition{
			UserAddress: uAddr,
			Reserves:    make(map[string]*models.UserReserve),
		}
	}
	s.userPositions[uAddr].Reserves[asset] = reserve

	if s.assetSubscribers[asset] == nil {
		s.assetSubscribers[asset] = make(map[string]struct{})
	}
	s.assetSubscribers[asset][uAddr] = struct{}{}
}

func (s *StateStore) SetUserEMode(userAddr string, categoryId uint8) {
	s.mu.Lock()
	defer s.mu.Unlock()

	uAddr := strings.ToLower(userAddr)
	if _, exists := s.userPositions[uAddr]; !exists {
		s.userPositions[uAddr] = &models.UserPosition{
			UserAddress: uAddr,
			Reserves:    make(map[string]*models.UserReserve),
		}
	}
	s.userPositions[uAddr].EModeCategoryId = categoryId
	s.userPositions[uAddr].EModeFetched = true
}

func (s *StateStore) GetGlobalIndex(asset string) *models.GlobalIndex {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.globalIndices[strings.ToLower(asset)]
}

func (s *StateStore) GetUserReserve(userAddr, asset string) *models.UserReserve {
	s.mu.Lock()
	defer s.mu.Unlock()

	uAddr := strings.ToLower(userAddr)
	assetAddr := strings.ToLower(asset)

	if _, exists := s.userPositions[uAddr]; !exists {
		s.userPositions[uAddr] = &models.UserPosition{
			UserAddress: uAddr,
			Reserves:    make(map[string]*models.UserReserve),
		}
	}

	if _, exists := s.userPositions[uAddr].Reserves[assetAddr]; !exists {
		s.userPositions[uAddr].Reserves[assetAddr] = &models.UserReserve{
			AssetAddress:                   assetAddr,
			ScaledATokenBalance:            big.NewInt(0),
			ScaledVariableDebt:             big.NewInt(0),
			UsageAsCollateralEnabledOnUser: false,
		}

		if s.assetSubscribers[assetAddr] == nil {
			s.assetSubscribers[assetAddr] = make(map[string]struct{})
		}
		s.assetSubscribers[assetAddr][uAddr] = struct{}{}
	}

	return s.userPositions[uAddr].Reserves[assetAddr]
}

func (s *StateStore) GetSubscribers(asset string) []string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	subs := s.assetSubscribers[strings.ToLower(asset)]
	users := make([]string, 0, len(subs))
	for u := range subs {
		users = append(users, u)
	}
	return users
}

func (s *StateStore) SetAssetPrice(asset string, price *big.Int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.prices[strings.ToLower(asset)] = price
}

func (s *StateStore) SetAssetConfig(asset string, config *models.AssetConfig) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.assetConfigs[strings.ToLower(asset)] = config
}

func (s *StateStore) SetEModeCategory(categoryId uint8, category *models.EModeCategory) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.eModeCategories[categoryId] = category
}
