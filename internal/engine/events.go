package engine

import "aave_bot/pkg/math"

// ============================================================================
// 事件 Topic
// ============================================================================

var (
	TopicReserveDataUpdated = math.GetTopicHash("ReserveDataUpdated(address,uint256,uint256,uint256,uint256,uint256)")
	TopicSupply             = math.GetTopicHash("Supply(address,address,address,uint256,uint16)")
	TopicWithdraw           = math.GetTopicHash("Withdraw(address,address,address,uint256)")
	TopicBorrow             = math.GetTopicHash("Borrow(address,address,address,uint256,uint8,uint256,uint16)")
	TopicRepay              = math.GetTopicHash("Repay(address,address,address,uint256,bool)")
	TopicLiquidationCall    = math.GetTopicHash("LiquidationCall(address,address,address,uint256,uint256,address,bool)")
	TopicCollateralEnabled  = math.GetTopicHash("ReserveUsedAsCollateralEnabled(address,address)")
	TopicCollateralDisabled = math.GetTopicHash("ReserveUsedAsCollateralDisabled(address,address)")
	TopicUserEModeSet       = math.GetTopicHash("UserEModeSet(address,uint8)")
)
