package config

import (
	"math/big"
	"os"
	"strconv"
	"strings"

	"github.com/ethereum/go-ethereum/common"
	"github.com/rs/zerolog/log"
)

// AppConfig 用于存储聚合后的全局配置
type AppConfig struct {
	ChainName            string
	ChainID              *big.Int
	BotPrivateKey        string
	LiquidatorContract   common.Address
	AaveV3Pool           string
	AaveV3Oracle         string
	RPCURLs              []string
	WSURL                string
	GraphEndpoint        string
	GraphAPIKey          string
	LogDir               string
	LogMaxSize           int
	LogMaxAge            int
	LogLevel             string
	WorkerCount          int
	UniswapV3Factory     common.Address
	Multicall3           common.Address
	StateSnapshotEnabled bool
	StateSnapshotDir     string
}

// LoadConfig 从环境变量中加载、解析并返回应用配置
func LoadConfig() AppConfig {
	chainID, ok := new(big.Int).SetString(requireEnv("CHAIN_ID"), 10)
	if !ok {
		log.Fatal().Msg("CHAIN_ID 格式无效")
	}

	rpcUrlsRaw := strings.Split(requireEnv("RPC_URLS"), ",")
	var rpcUrls []string
	for _, url := range rpcUrlsRaw {
		cleaned := strings.TrimSpace(url)
		if cleaned != "" {
			rpcUrls = append(rpcUrls, cleaned)
		}
	}
	if len(rpcUrls) == 0 {
		log.Fatal().Msg("RPC_URLS 为必填项，不可为空")
	}

	logMaxSize, _ := strconv.Atoi(getEnvOrDefault("LOG_MAX_SIZE", "100"))
	logMaxAge, _ := strconv.Atoi(getEnvOrDefault("LOG_MAX_AGE", "7"))
	logLevel := getEnvOrDefault("LOG_LEVEL", "info")
	workerCount, _ := strconv.Atoi(getEnvOrDefault("WORKER_COUNT", "8"))

	stateSnapshotEnabledStr := getEnvOrDefault("STATE_SNAPSHOT_ENABLED", "true")
	stateSnapshotEnabled := strings.ToLower(stateSnapshotEnabledStr) == "true"
	stateSnapshotDir := getEnvOrDefault("STATE_SNAPSHOT_DIR", "state_snapshots")

	factoryAddrRaw := requireEnv("UNISWAP_V3_FACTORY")
	if !common.IsHexAddress(factoryAddrRaw) {
		log.Fatal().Str("address", factoryAddrRaw).Msg("UNISWAP_V3_FACTORY 地址格式无效")
	}

	multicallAddrRaw := requireEnv("MULTICALL3")
	if !common.IsHexAddress(multicallAddrRaw) {
		log.Fatal().Str("address", multicallAddrRaw).Msg("MULTICALL3 地址格式无效")
	}

	return AppConfig{
		ChainName:            requireEnv("CHAIN_NAME"),
		ChainID:              chainID,
		BotPrivateKey:        requireEnv("BOT_PRIVATE_KEY"),
		LiquidatorContract:   common.HexToAddress(requireEnv("LIQUIDATOR_CONTRACT")),
		AaveV3Pool:           requireEnv("AAVE_V3_POOL"),
		AaveV3Oracle:         requireEnv("AAVE_V3_ORACLE"),
		RPCURLs:              rpcUrls,
		WSURL:                requireEnv("WS_URL"),
		GraphEndpoint:        requireEnv("GRAPH_ENDPOINT"),
		GraphAPIKey:          requireEnv("GRAPH_API_KEY"),
		LogDir:               getEnvOrDefault("LOG_DIR", "logs"),
		LogMaxSize:           logMaxSize,
		LogMaxAge:            logMaxAge,
		LogLevel:             logLevel,
		WorkerCount:          workerCount,
		UniswapV3Factory:     common.HexToAddress(factoryAddrRaw),
		Multicall3:           common.HexToAddress(multicallAddrRaw),
		StateSnapshotEnabled: stateSnapshotEnabled,
		StateSnapshotDir:     stateSnapshotDir,
	}
}

// requireEnv 强制获取必需的环境变量，缺失则直接 Panic
func requireEnv(key string) string {
	val := os.Getenv(key)
	if val == "" {
		log.Fatal().Str("env_var", key).Msg("严重配置错误: 缺少必需的环境变量")
	}
	return val
}

// getEnvOrDefault 获取环境变量，缺失则使用默认值
func getEnvOrDefault(key, defaultVal string) string {
	val := os.Getenv(key)
	if val == "" {
		return defaultVal
	}
	return val
}
