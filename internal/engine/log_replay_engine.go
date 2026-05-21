package engine

import (
	"aave_bot/pkg/conv"
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/websocket"
	"github.com/rs/zerolog/log"

	"aave_bot/internal/models"
	"aave_bot/internal/rpc"
	"aave_bot/internal/store"
	"aave_bot/pkg/math"
)

// LogReplayEngine 通过 WebSocket 监听 Aave V3 Pool 合约事件，并更新内存状态。
type LogReplayEngine struct {
	wsURL                 string
	rpcClient             *rpc.RPCClient
	store                 *store.StateStore
	poolAddress           string
	topics                []string
	logBuffer             chan models.RPCLog
	lastProcessedBlock    uint64
	lastProcessedLogIndex uint64
	riskEngine            *RiskEngine
	currentTxHash         string
	dirtyUsers            map[string]struct{}
}

// NewLogReplayEngine 创建一个新的 LogReplayEngine 实例。
func NewLogReplayEngine(wsURL string, rpcClient *rpc.RPCClient, store *store.StateStore, poolAddress string, topics []string, riskEngine *RiskEngine) *LogReplayEngine {
	return &LogReplayEngine{
		wsURL:       wsURL,
		rpcClient:   rpcClient,
		store:       store,
		poolAddress: strings.ToLower(poolAddress),
		topics:      topics,
		logBuffer:   make(chan models.RPCLog, 20000),
		riskEngine:  riskEngine,
		dirtyUsers:  make(map[string]struct{}),
	}
}

// fetchLatestBlock 从链上获取最新区块号。
func (e *LogReplayEngine) fetchLatestBlock(ctx context.Context) (uint64, error) {
	rawResult, err := e.rpcClient.CallWithRetry(ctx, "eth_blockNumber", []interface{}{})
	if err != nil {
		return 0, err
	}
	var resultHex string
	if err := json.Unmarshal(rawResult, &resultHex); err != nil {
		return 0, fmt.Errorf("failed to unmarshal block number result: %w", err)
	}

	blockNum, err := strconv.ParseUint(strings.TrimPrefix(resultHex, "0x"), 16, 64)
	if err != nil {
		return 0, fmt.Errorf("failed to parse block number: %w", err)
	}
	return blockNum, nil
}

// markUserDirty 将用户标记为需要重新计算健康因子。
func (e *LogReplayEngine) markUserDirty(userAddr string) {
	e.dirtyUsers[userAddr] = struct{}{}
}

// flushDirtyUsers 将所有脏用户发送到 RiskEngine 的任务队列。
func (e *LogReplayEngine) flushDirtyUsers() {
	for userAddr := range e.dirtyUsers {
		e.riskEngine.Enqueue(userAddr)
	}
	e.dirtyUsers = make(map[string]struct{})
}

// fetchHistoricLogs 分块获取历史日志以避免 RPC 限制。
func (e *LogReplayEngine) fetchHistoricLogs(ctx context.Context, fromBlock, toBlock uint64) ([]models.RPCLog, error) {
	var allLogs []models.RPCLog
	const chunkSize uint64 = 10

	for currentFrom := fromBlock; currentFrom <= toBlock; currentFrom += chunkSize {
		currentTo := currentFrom + chunkSize - 1
		if currentTo > toBlock {
			currentTo = toBlock
		}

		hexFrom := fmt.Sprintf("0x%x", currentFrom)
		hexTo := fmt.Sprintf("0x%x", currentTo)

		params := []interface{}{
			map[string]interface{}{
				"fromBlock": hexFrom,
				"toBlock":   hexTo,
				"address":   e.poolAddress,
				"topics":    []interface{}{e.topics},
			},
		}

		rawResult, err := e.rpcClient.CallWithRetry(ctx, "eth_getLogs", params)
		if err != nil {
			return nil, fmt.Errorf("chunk [%d, %d] request failed: %w", currentFrom, currentTo, err)
		}

		var logs []models.RPCLog
		if err := json.Unmarshal(rawResult, &logs); err != nil {
			return nil, fmt.Errorf("chunk [%d, %d] decode failed: %w", currentFrom, currentTo, err)
		}

		allLogs = append(allLogs, logs...)
		time.Sleep(50 * time.Millisecond) // 限流以避免 RPC 节点问题。
	}

	return allLogs, nil
}

// processLog 根据事件签名将日志路由到相应的处理器。
func (e *LogReplayEngine) processLog(logData models.RPCLog) {
	if logData.Removed {
		log.Warn().Str("block", logData.BlockNumber).Msg("检测到链重组，该日志已移除")
		return
	}
	if len(logData.Topics) == 0 {
		return
	}

	eventSignature := logData.Topics[0]
	switch eventSignature {
	case TopicReserveDataUpdated:
		e.handleReserveDataUpdated(logData)
	case TopicSupply:
		e.handleSupply(logData)
	case TopicWithdraw:
		e.handleWithdraw(logData)
	case TopicBorrow:
		e.handleBorrow(logData)
	case TopicRepay:
		e.handleRepay(logData)
	case TopicLiquidationCall:
		e.handleLiquidationCall(logData)
	case TopicCollateralEnabled:
		e.handleCollateralEnabled(logData)
	case TopicCollateralDisabled:
		e.handleCollateralDisabled(logData)
	case TopicUserEModeSet:
		e.handleUserEModeSet(logData)
	default:
		log.Warn().Str("eventSignature", eventSignature).Msg("未知事件签名")
	}
}

// handleReserveDataUpdated 处理 ReserveDataUpdated 事件。
func (e *LogReplayEngine) handleReserveDataUpdated(logData models.RPCLog) {
	if len(logData.Topics) < 2 {
		return
	}

	assetAddress := math.ParseHexAddress(logData.Topics[1])
	data := strings.TrimPrefix(logData.Data, "0x")
	if len(data) < 320 {
		return
	}

	liquidityIndex := math.ParseHexBigInt(data[192:256])
	variableBorrowIndex := math.ParseHexBigInt(data[256:320])
	blockNum, _ := strconv.ParseUint(strings.TrimPrefix(logData.BlockNumber, "0x"), 16, 64)

	newIndex := &models.GlobalIndex{
		AssetAddress:        assetAddress,
		LiquidityIndex:      liquidityIndex,
		VariableBorrowIndex: variableBorrowIndex,
		LastUpdateBlock:     blockNum,
	}
	e.store.SetGlobalIndex(assetAddress, newIndex)

	log.Info().
		Str("event", "ReserveDataUpdated").
		Str("BlockNumber", conv.HexToDecString(logData.BlockNumber)).
		Str("asset", assetAddress).
		Msg("GlobalIndex 已更新")
	e.riskEngine.EnqueueByAsset(assetAddress)
}

// handleCollateralEnabled 处理 ReserveUsedAsCollateralEnabled 事件。
func (e *LogReplayEngine) handleCollateralEnabled(logData models.RPCLog) {
	if len(logData.Topics) < 3 {
		return
	}
	assetAddress := math.ParseHexAddress(logData.Topics[1])
	userAddress := math.ParseHexAddress(logData.Topics[2])

	reserve := e.store.GetUserReserve(userAddress, assetAddress)
	e.store.Mu().Lock()
	reserve.UsageAsCollateralEnabledOnUser = true
	e.store.Mu().Unlock()

	log.Info().Str("event", "CollateralEnabled").Str("user", userAddress).Str("asset", assetAddress).Msg("已启用为抵押物")
	e.markUserDirty(userAddress)
}

// handleCollateralDisabled 处理 ReserveUsedAsCollateralDisabled 事件。
func (e *LogReplayEngine) handleCollateralDisabled(logData models.RPCLog) {
	if len(logData.Topics) < 3 {
		return
	}
	assetAddress := math.ParseHexAddress(logData.Topics[1])
	userAddress := math.ParseHexAddress(logData.Topics[2])

	reserve := e.store.GetUserReserve(userAddress, assetAddress)
	e.store.Mu().Lock()
	reserve.UsageAsCollateralEnabledOnUser = false
	e.store.Mu().Unlock()

	log.Info().Str("event", "CollateralDisabled").Str("user", userAddress).Str("asset", assetAddress).Msg("已取消抵押物状态")
	e.markUserDirty(userAddress)
}

func (e *LogReplayEngine) handleSupply(logData models.RPCLog) {
	if len(logData.Topics) < 3 {
		return
	}

	assetAddress := math.ParseHexAddress(logData.Topics[1])
	onBehalfOf := math.ParseHexAddress(logData.Topics[2])
	data := strings.TrimPrefix(logData.Data, "0x")
	if len(data) < 128 {
		return
	}

	amount := math.ParseHexBigInt(data[64:128])
	globalIdx := e.store.GetGlobalIndex(assetAddress)
	if globalIdx == nil || globalIdx.LiquidityIndex.Cmp(big.NewInt(0)) == 0 {
		log.Warn().Str("asset", assetAddress).Msg("Supply 事件未命中该资产的 Index 缓存")
		return
	}

	scaledAmount := math.RayDiv(amount, globalIdx.LiquidityIndex)

	reserve := e.store.GetUserReserve(onBehalfOf, assetAddress)
	e.store.Mu().Lock()
	reserve.ScaledATokenBalance.Add(reserve.ScaledATokenBalance, scaledAmount)
	e.store.Mu().Unlock()

	log.Info().
		Str("event", "Supply").
		Str("user", onBehalfOf).
		Str("asset", assetAddress).
		Str("amount", amount.String()).
		Msg("状态已更新")

	e.markUserDirty(onBehalfOf)
}

func (e *LogReplayEngine) handleWithdraw(logData models.RPCLog) {
	if len(logData.Topics) < 4 {
		return
	}

	assetAddress := math.ParseHexAddress(logData.Topics[1])
	userAddress := math.ParseHexAddress(logData.Topics[2])
	data := strings.TrimPrefix(logData.Data, "0x")
	if len(data) < 64 {
		return
	}

	amount := math.ParseHexBigInt(data[0:64])
	globalIdx := e.store.GetGlobalIndex(assetAddress)
	if globalIdx == nil || globalIdx.LiquidityIndex.Cmp(big.NewInt(0)) == 0 {
		return
	}

	scaledAmount := math.RayDiv(amount, globalIdx.LiquidityIndex)

	reserve := e.store.GetUserReserve(userAddress, assetAddress)

	e.store.Mu().Lock()
	reserve.ScaledATokenBalance.Sub(reserve.ScaledATokenBalance, scaledAmount)

	if reserve.ScaledATokenBalance.Sign() < 0 || new(big.Int).Abs(reserve.ScaledATokenBalance).Cmp(big.NewInt(math.DustThreshold)) <= 0 {
		reserve.ScaledATokenBalance.SetInt64(0)
	}
	e.store.Mu().Unlock()

	log.Info().
		Str("event", "Withdraw").
		Str("user", userAddress).
		Str("asset", assetAddress).
		Str("amount", amount.String()).
		Msg("状态已更新")
	e.markUserDirty(userAddress)
}

func (e *LogReplayEngine) handleBorrow(logData models.RPCLog) {
	if len(logData.Topics) < 3 {
		return
	}

	assetAddress := math.ParseHexAddress(logData.Topics[1])
	onBehalfOf := math.ParseHexAddress(logData.Topics[2])
	data := strings.TrimPrefix(logData.Data, "0x")
	if len(data) < 192 {
		return
	}

	amount := math.ParseHexBigInt(data[64:128])
	interestRateMode := math.ParseHexBigInt(data[128:192]).Int64()
	if interestRateMode != 2 { // 仅跟踪可变利率借贷
		return
	}

	globalIdx := e.store.GetGlobalIndex(assetAddress)
	if globalIdx == nil || globalIdx.VariableBorrowIndex.Cmp(big.NewInt(0)) == 0 {
		return
	}

	scaledAmount := math.RayDiv(amount, globalIdx.VariableBorrowIndex)

	reserve := e.store.GetUserReserve(onBehalfOf, assetAddress)

	e.store.Mu().Lock()
	reserve.ScaledVariableDebt.Add(reserve.ScaledVariableDebt, scaledAmount)
	e.store.Mu().Unlock()

	log.Info().
		Str("event", "Borrow").
		Str("user", onBehalfOf).
		Str("asset", assetAddress).
		Str("amount", amount.String()).
		Msg("状态已更新")
	e.markUserDirty(onBehalfOf)
}

func (e *LogReplayEngine) handleRepay(logData models.RPCLog) {
	if len(logData.Topics) < 3 {
		return
	}

	assetAddress := math.ParseHexAddress(logData.Topics[1])
	userAddress := math.ParseHexAddress(logData.Topics[2])
	data := strings.TrimPrefix(logData.Data, "0x")
	if len(data) < 64 {
		return
	}

	amount := math.ParseHexBigInt(data[0:64])
	globalIdx := e.store.GetGlobalIndex(assetAddress)
	if globalIdx == nil || globalIdx.VariableBorrowIndex.Cmp(big.NewInt(0)) == 0 {
		return
	}

	scaledAmount := math.RayDiv(amount, globalIdx.VariableBorrowIndex)

	reserve := e.store.GetUserReserve(userAddress, assetAddress)

	e.store.Mu().Lock()
	reserve.ScaledVariableDebt.Sub(reserve.ScaledVariableDebt, scaledAmount)

	if reserve.ScaledVariableDebt.Sign() < 0 || new(big.Int).Abs(reserve.ScaledVariableDebt).Cmp(big.NewInt(math.DustThreshold)) <= 0 {
		reserve.ScaledVariableDebt.SetInt64(0)
	}
	e.store.Mu().Unlock()

	log.Info().
		Str("event", "Repay").
		Str("user", userAddress).
		Str("asset", assetAddress).
		Str("amount", amount.String()).
		Msg("状态已更新")

	e.markUserDirty(userAddress)
}

func (e *LogReplayEngine) handleUserEModeSet(logData models.RPCLog) {
	if len(logData.Topics) < 2 {
		return
	}
	userAddress := math.ParseHexAddress(logData.Topics[1])
	data := strings.TrimPrefix(logData.Data, "0x")

	categoryId := uint8(math.ParseHexBigInt(data).Uint64())

	e.store.SetUserEMode(userAddress, categoryId)

	log.Info().Str("event", "EModeSet").Str("user", userAddress).Uint8("eMode", categoryId).Msg("用户 E-Mode 已更新")
	e.markUserDirty(userAddress)
}

func (e *LogReplayEngine) handleLiquidationCall(logData models.RPCLog) {
	if len(logData.Topics) < 4 {
		return
	}

	collateralAsset := math.ParseHexAddress(logData.Topics[1])
	debtAsset := math.ParseHexAddress(logData.Topics[2])
	userAddress := math.ParseHexAddress(logData.Topics[3])
	data := strings.TrimPrefix(logData.Data, "0x")
	if len(data) < 128 {
		return
	}

	debtToCover := math.ParseHexBigInt(data[0:64])
	liquidatedCollateralAmount := math.ParseHexBigInt(data[64:128])

	debtIdx := e.store.GetGlobalIndex(debtAsset)
	collateralIdx := e.store.GetGlobalIndex(collateralAsset)
	if debtIdx == nil || collateralIdx == nil {
		return
	}

	scaledDebtToCover := math.RayDiv(debtToCover, debtIdx.VariableBorrowIndex)
	scaledCollateralLiquidated := math.RayDiv(liquidatedCollateralAmount, collateralIdx.LiquidityIndex)

	debtReserve := e.store.GetUserReserve(userAddress, debtAsset)
	collateralReserve := e.store.GetUserReserve(userAddress, collateralAsset)

	e.store.Mu().Lock()

	debtReserve.ScaledVariableDebt.Sub(debtReserve.ScaledVariableDebt, scaledDebtToCover)
	if debtReserve.ScaledVariableDebt.Sign() < 0 || new(big.Int).Abs(debtReserve.ScaledVariableDebt).Cmp(big.NewInt(math.DustThreshold)) <= 0 {
		debtReserve.ScaledVariableDebt.SetInt64(0)
	}

	collateralReserve.ScaledATokenBalance.Sub(collateralReserve.ScaledATokenBalance, scaledCollateralLiquidated)
	if collateralReserve.ScaledATokenBalance.Sign() < 0 || new(big.Int).Abs(collateralReserve.ScaledATokenBalance).Cmp(big.NewInt(math.DustThreshold)) <= 0 {
		collateralReserve.ScaledATokenBalance.SetInt64(0)
	}
	e.store.Mu().Unlock()

	log.Info().
		Str("event", "LiquidationCall").
		Str("user", userAddress).
		Str("collateral", collateralAsset).
		Str("debt", debtAsset).
		Msg("LiquidationCall 已处理")

	e.markUserDirty(userAddress)
}

func (e *LogReplayEngine) isDuplicate(logBlock, logIndex uint64) bool {
	if logBlock < e.lastProcessedBlock {
		return true
	}
	if logBlock == e.lastProcessedBlock && logIndex <= e.lastProcessedLogIndex {
		return true
	}
	return false
}

func (e *LogReplayEngine) Start(ctx context.Context) error {
	dialer := websocket.DefaultDialer
	conn, _, err := dialer.DialContext(ctx, e.wsURL, nil)
	if err != nil {
		return fmt.Errorf("ws dial failed: %w", err)
	}
	defer conn.Close()

	subPayload := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "eth_subscribe",
		"params": []interface{}{
			"logs",
			map[string]interface{}{
				"address": e.poolAddress,
				"topics":  []interface{}{e.topics},
			},
		},
	}
	if err := conn.WriteJSON(subPayload); err != nil {
		return fmt.Errorf("failed to send subscribe payload: %w", err)
	}

	go func() {
		for {
			var msg struct {
				Params struct {
					Result models.RPCLog `json:"result"`
				} `json:"params"`
			}
			err := conn.ReadJSON(&msg)
			if err != nil {
				log.Error().Err(err).Msg("WS 引擎读取错误或已断开连接")
				return
			}
			if msg.Params.Result.BlockNumber != "" {
				e.logBuffer <- msg.Params.Result
			}
		}
	}()

	log.Info().Msg("WS 订阅已激活，缓冲区填充中...")
	time.Sleep(1 * time.Second)

	bWs, err := e.fetchLatestBlock(ctx)
	if err != nil {
		return fmt.Errorf("failed to fetch latest block for sync: %w", err)
	}
	log.Info().Uint64("bWs", bWs).Msg("已捕获重叠边界区块号")

	hBlock := e.store.AnchorBlock()

	log.Info().Uint64("from", hBlock+1).Uint64("to", bWs).Msg("拉取历史重叠区间日志")
	historicLogs, err := e.fetchHistoricLogs(ctx, hBlock+1, bWs)
	if err != nil {
		return fmt.Errorf("failed to fetch historic logs: %w", err)
	}

	sort.Sort(models.LogList(historicLogs))

	for _, logData := range historicLogs {
		if logData.TransactionHash != e.currentTxHash {
			e.flushDirtyUsers()
			e.currentTxHash = logData.TransactionHash
		}
		blockNum, _ := strconv.ParseUint(strings.TrimPrefix(logData.BlockNumber, "0x"), 16, 64)
		logIdx, _ := strconv.ParseUint(strings.TrimPrefix(logData.LogIndex, "0x"), 16, 64)

		e.processLog(logData)

		e.lastProcessedBlock = blockNum
		e.lastProcessedLogIndex = logIdx
	}
	e.flushDirtyUsers()

	if e.lastProcessedBlock < bWs {
		e.lastProcessedBlock = bWs
		e.lastProcessedLogIndex = 0
	}

	log.Info().Uint64("block", e.lastProcessedBlock).Uint64("index", e.lastProcessedLogIndex).Msg("历史日志回放完成")
	log.Info().Msg("切换至实时去重流模式")

	for {
		select {
		case <-ctx.Done():
			return nil
		case logData := <-e.logBuffer:
			blockNum, _ := strconv.ParseUint(strings.TrimPrefix(logData.BlockNumber, "0x"), 16, 64)

			e.store.SetCurrentBlock(blockNum)

			if logData.TransactionHash != e.currentTxHash {
				e.flushDirtyUsers()
				e.currentTxHash = logData.TransactionHash
			}

			logIdx, _ := strconv.ParseUint(strings.TrimPrefix(logData.LogIndex, "0x"), 16, 64)

			if e.isDuplicate(blockNum, logIdx) {
				log.Debug().Uint64("block", blockNum).Uint64("index", logIdx).Msg("丢弃重复日志")
			} else {
				e.processLog(logData)
				e.lastProcessedBlock = blockNum
				e.lastProcessedLogIndex = logIdx
			}

			if len(e.logBuffer) == 0 {
				e.flushDirtyUsers()
			}
		}
	}
}
