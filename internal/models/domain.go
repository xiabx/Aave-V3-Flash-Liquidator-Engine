package models

import "math/big"

// GlobalIndex 维护资产的全局流动性与借款指数
// 缓存 Aave Pool.getReserveData() 返回的实时指数，用于将 Scaled 余额转换为实际余额
type GlobalIndex struct {
	AssetAddress        string   // 资产合约地址 (小写)
	LiquidityIndex      *big.Int // 流动性指数 (RAY 精度 1e27)
	VariableBorrowIndex *big.Int // 可变借款指数 (RAY 精度 1e27)
	LastUpdateBlock     uint64   // 最后更新的区块高度
}

// UserPosition 维护单个用户的完整仓位信息
// 聚合用户在所有资产上的储备状态，支持 E-Mode 高效清算检测
type UserPosition struct {
	UserAddress     string                  // 用户地址 (小写)
	Reserves        map[string]*UserReserve // 键为资产地址 -> 储备状态映射
	EModeCategoryId uint8                   // 用户当前开启的 E-Mode 分类 ID (0 = 未开启)
	EModeFetched    bool                    // 标记是否已经向链上查过该用户的 E-Mode
}

// UserReserve 维护用户在单一资产上的底层状态
// 记录用户在每个资产上的 aToken 余额与可变债务
type UserReserve struct {
	AssetAddress                   string   // 资产合约地址
	ScaledATokenBalance            *big.Int // Scaled aToken 余额 (未累积利息)
	ScaledVariableDebt             *big.Int // Scaled 可变债务 (未累积利息)
	UsageAsCollateralEnabledOnUser bool     // 用户是否将该资产作为抵押品
}

// AssetConfig 维护资产的静态参数及相关代币合约地址
// 从 Pool.getReserveData() 解析获取，初始化后不再变更
type AssetConfig struct {
	Decimals               uint64 // 资产精度 (例如 USDC = 6, WETH = 18)
	LiquidationThreshold   uint16 // 清算阈值 (精度 10000, 例如 8500 = 85%)
	LiquidationBonus       uint16 // 清算奖励
	ReserveID              uint16 // 储备金 ID (用于 E-Mode CollateralBitmap 位运算)
	ATokenAddress          string // aToken 代币合约地址
	VTokenAddress          string // variableDebtToken (vToken) 代币合约地址
	LiquidationProtocolFee uint16
}

// EModeCategory 记录 E-Mode 分类数据
// 存储 E-Mode 分类的全量配置参数与抵押品位图，用于优化 HF 计算
type EModeCategory struct {
	Ltv                  uint16   // 最高借款额度 (Loan To Value, 精度 10000)
	LiquidationThreshold uint16   // 清算阈值 (精度 10000)
	LiquidationBonus     uint16   // 清算奖励 (ex 10500 = 5% bonus)
	PriceSource          string   // 该分类专属的预言机地址
	CollateralBitmap     *big.Int // 抵押品位图
}
