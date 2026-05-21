package main

import (
	"aave_bot/internal/config"
	"aave_bot/internal/dex"
	"aave_bot/internal/engine"
	"aave_bot/internal/oracle"
	"aave_bot/internal/rpc"
	"aave_bot/internal/store"
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/ethereum/go-ethereum/crypto"
	"github.com/joho/godotenv"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"gopkg.in/natefinch/lumberjack.v2"
)

func main() {
	envFile := flag.String("env", ".env_base", "Specify the environment file to load")
	flag.Parse()

	if err := godotenv.Load(*envFile); err != nil {
		fmt.Printf("环境文件 [%s] 加载失败: %v\n", *envFile, err)
		os.Exit(1)
	}

	cfg := config.LoadConfig()

	setupLogger(cfg)

	printStartupBanner(cfg, *envFile)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	rpcClient := rpc.NewRPCClient(cfg.RPCURLs, cfg.Multicall3)
	stateStore := store.NewStateStore()
	graphClient := rpc.NewGraphClient(cfg.GraphEndpoint, cfg.GraphAPIKey)

	log.Info().Msg("正在从 Aave V3 资金池获取动态资产列表...")
	targetAssets, err := rpc.FetchReservesList(rpcClient, cfg.AaveV3Pool)
	if err != nil {
		log.Fatal().Err(err).Msg("从链上获取储备资产列表失败")
	}
	log.Info().Int("count", len(targetAssets)).Msg("已成功从链上加载资产列表")

	feeCache := setupFeeCache(rpcClient, targetAssets, cfg)
	runBootstrapper(graphClient, rpcClient, stateStore, cfg.AaveV3Pool, targetAssets)

	txExecutor := setupTxExecutor(rpcClient, feeCache, cfg)
	riskEngine := engine.NewRiskEngine(stateStore, cfg.WorkerCount, rpcClient, cfg.AaveV3Pool, engine.NewStrategyEngine(stateStore), txExecutor)
	riskEngine.Start(ctx)

	oracleFeeder := oracle.NewOracleFeeder(rpcClient, stateStore, cfg.AaveV3Oracle, targetAssets, riskEngine)
	oracleFeeder.Start(ctx)

	if cfg.StateSnapshotEnabled {
		startStateSnapshotter(stateStore, cfg.StateSnapshotDir)
	}

	log.Info().Msg("所有引擎已就绪，开始清算监控")

	topics := []string{
		engine.TopicReserveDataUpdated, engine.TopicSupply, engine.TopicWithdraw,
		engine.TopicBorrow, engine.TopicRepay, engine.TopicLiquidationCall,
		engine.TopicCollateralEnabled, engine.TopicCollateralDisabled, engine.TopicUserEModeSet,
	}
	logEngine := engine.NewLogReplayEngine(cfg.WSURL, rpcClient, stateStore, cfg.AaveV3Pool, topics, riskEngine)
	go func() {
		if err := logEngine.Start(ctx); err != nil {
			log.Error().Err(err).Msg("日志引擎异常退出")
		}
	}()

	// 等待终止信号
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	<-sigChan

	log.Info().Msg("收到终止信号，开始优雅关闭...")
	cancel()

	time.Sleep(5 * time.Second)

	log.Info().Msg("正在执行最终状态快照...")
	finalSnapshotPath := fmt.Sprintf("%s/state_shutdown_%s.json", cfg.StateSnapshotDir, time.Now().Format("20060102_150405"))
	if err := stateStore.DumpToJSONFile(finalSnapshotPath); err != nil {
		log.Error().Err(err).Str("file", finalSnapshotPath).Msg("最终状态快照保存失败")
	} else {
		log.Info().Str("file", finalSnapshotPath).Msg("最终状态快照保存成功")
	}

	log.Info().Msg("关闭完成。")
}

// setupLogger 配置全局日志
func setupLogger(cfg config.AppConfig) {
	os.Setenv("TZ", "Asia/Shanghai")
	zerolog.TimeFieldFormat = zerolog.TimeFormatUnix

	if err := os.MkdirAll(cfg.LogDir, 0755); err != nil {
		panic(fmt.Sprintf("日志目录创建失败: %v", err))
	}

	logFilePath := fmt.Sprintf("%s/%s_%s.log", cfg.LogDir, strings.ToLower(cfg.ChainName), time.Now().Format("20060102_150405"))

	fileWriter := &lumberjack.Logger{
		Filename:  logFilePath,
		MaxSize:   cfg.LogMaxSize,
		MaxAge:    cfg.LogMaxAge,
		Compress:  false,
		LocalTime: true,
	}

	consoleWriter := zerolog.ConsoleWriter{Out: os.Stderr, TimeFormat: time.RFC3339}
	multiWriter := zerolog.MultiLevelWriter(consoleWriter, fileWriter)
	log.Logger = zerolog.New(multiWriter).With().Timestamp().Logger()

	level, err := zerolog.ParseLevel(strings.ToLower(cfg.LogLevel))
	if err != nil {
		level = zerolog.InfoLevel
	}
	zerolog.SetGlobalLevel(level)
}

// printStartupBanner 打印启动信息
func printStartupBanner(cfg config.AppConfig, envFile string) {
	fmt.Println(strings.Repeat("=", 60))
	fmt.Printf("Aave V3 清算机器人启动中\n")
	fmt.Printf("环境配置文件     : %s\n", envFile)
	fmt.Printf("目标链           : %s (Chain ID: %s)\n", cfg.ChainName, cfg.ChainID.String())
	fmt.Printf("RPC 节点数量     : %d\n", len(cfg.RPCURLs))
	fmt.Printf("日志级别         : %s\n", zerolog.GlobalLevel().String())
	fmt.Println(strings.Repeat("=", 60))

	log.Info().
		Str("chain", cfg.ChainName).
		Str("env_file", envFile).
		Msg("系统启动中")
}

// setupFeeCache 初始化并构建 DEX 费率缓存。
func setupFeeCache(rpcClient *rpc.RPCClient, assets []string, cfg config.AppConfig) *dex.FeeCache {
	feeCache := dex.NewFeeCache(log.With().Str("module", "FeeCache").Logger())
	cacheCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	err := feeCache.BuildCache(cacheCtx, rpcClient, assets, cfg.UniswapV3Factory, cfg.Multicall3)
	if err != nil {
		log.Fatal().Err(err).Msg("静态最优费率缓存初始化失败")
	}
	return feeCache
}

// runBootstrapper 从 The Graph 加载历史快照数据。
func runBootstrapper(graphClient *rpc.GraphClient, rpcClient *rpc.RPCClient, store *store.StateStore, poolAddr string, assets []string) {
	bootstrapper := rpc.NewBootstrapper(graphClient, rpcClient, store, poolAddr)
	bootCtx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	if err := bootstrapper.Run(bootCtx, assets); err != nil {
		log.Fatal().Err(err).Msg("启动引导过程发生致命错误")
	}
}

// setupTxExecutor 初始化交易执行器。
func setupTxExecutor(rpcClient *rpc.RPCClient, feeCache *dex.FeeCache, cfg config.AppConfig) *engine.TxExecutor {
	privKeyHex := strings.TrimPrefix(cfg.BotPrivateKey, "0x")
	privateKey, err := crypto.HexToECDSA(privKeyHex)
	if err != nil {
		log.Fatal().Err(err).Msg("严重配置错误: 私钥格式无效")
	}

	executorLogger := log.With().Str("module", "TxExecutor").Logger()
	txExecutor, err := engine.NewTxExecutor(rpcClient, cfg.LiquidatorContract, privateKey, cfg.ChainID, executorLogger, feeCache)
	if err != nil {
		log.Fatal().Err(err).Msg("TxExecutor 初始化失败")
	}
	return txExecutor
}

// startStateSnapshotter 启动一个 goroutine 定期将内存状态转储到文件。
func startStateSnapshotter(store *store.StateStore, dumpDir string) {
	go func() {
		if err := os.MkdirAll(dumpDir, 0755); err != nil {
			log.Error().Err(err).Msg("state_snapshots 目录创建失败")
			return
		}

		ticker := time.NewTicker(10 * time.Minute)
		defer ticker.Stop()

		for range ticker.C {
			fileName := fmt.Sprintf("%s/state_%s.json", dumpDir, time.Now().Format("20060102_150405"))
			log.Info().Str("file", fileName).Msg("开始定时 StateStore 转储")

			if err := store.DumpToJSONFile(fileName); err != nil {
				log.Error().Err(err).Msg("StateStore 转储失败")
			} else {
				log.Info().Str("file", fileName).Msg("StateStore 转储完成")
			}
		}
	}()
}
