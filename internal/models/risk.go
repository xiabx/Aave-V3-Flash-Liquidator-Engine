package models

import "math/big"

// UserRiskItem 存储在堆中的用户风险快照
// 作为 RiskHeap 的元素，维护用户的 Health Factor 与堆索引
type UserRiskItem struct {
	UserAddress  string   // 用户地址
	HealthFactor *big.Int // 当前 Health Factor (WAD 精度)
	Index        int      // container/heap 内部维护的索引
	NextCheckAt  int64    // 下一次允许检查的时间戳 (秒)
}

// RiskHeap 实现 heap.Interface，按 HF 从小到大排序 (最小堆)
type RiskHeap []*UserRiskItem

func (h RiskHeap) Len() int { return len(h) }

func (h RiskHeap) Less(i, j int) bool {
	// HF 越小，优先级越高，越应该排在堆顶
	return h[i].HealthFactor.Cmp(h[j].HealthFactor) < 0
}

func (h RiskHeap) Swap(i, j int) {
	h[i], h[j] = h[j], h[i]
	h[i].Index = i
	h[j].Index = j
}

func (h *RiskHeap) Push(x interface{}) {
	n := len(*h)
	item := x.(*UserRiskItem)
	item.Index = n
	*h = append(*h, item)
}

func (h *RiskHeap) Pop() interface{} {
	old := *h
	n := len(old)
	item := old[n-1]
	old[n-1] = nil  // 避免内存泄漏
	item.Index = -1 // 标记为已移除
	*h = old[0 : n-1]
	return item
}

// RiskSnapshot 记录用户在特定高度下的完整风险画像
// 作为 HF 计算结果返回给上层引擎，包含原始抵押品价值与加权后的总抵押品
type RiskSnapshot struct {
	HealthFactor        *big.Int // 健康因子 (WAD 精度, 1.0 = 1e18)
	RawCollateralBase   *big.Int // 原始抵押品美元价值 (8 位精度)
	TotalCollateralBase *big.Int // 加权后的总抵押品美元价值 (考虑 LT 阈值, 8 位精度)
	TotalDebtBase       *big.Int // 总债务美元价值 (8 位精度)
	CalculatedAtBlock   uint64   // 计算时的区块高度 (用于一致性校验)
}
