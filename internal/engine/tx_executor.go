package engine

import (
	"aave_bot/pkg/abis"
	"bytes"
	"context"
	"crypto/ecdsa"
	"fmt"
	"math/big"
	"strings"

	"aave_bot/internal/dex"
	"aave_bot/internal/rpc"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/rs/zerolog"
)

const (
	// gasLimitBuffer 添加到预估的 gas 限制中以提供安全余量。
	gasLimitBuffer = 20000
	// maxRPCRetries 最大 RPC 重试次数。
	maxRPCRetries = 3
	// minProfitAmount 清算所需的最小利润。
	minProfitAmount = 0
	// fallbackPoolFee 无法确定最优费率时使用的默认池费率。
	fallbackPoolFee = 3000
)

type TxExecutor struct {
	rpcClient  *rpc.RPCClient
	contract   common.Address
	privateKey *ecdsa.PrivateKey
	walletAddr common.Address
	chainID    *big.Int
	parsedABI  abi.ABI
	logger     zerolog.Logger
	feeCache   *dex.FeeCache
}

func NewTxExecutor(rpcClient *rpc.RPCClient, contractAddr common.Address, pk *ecdsa.PrivateKey, chainID *big.Int, logger zerolog.Logger, feeCache *dex.FeeCache) (*TxExecutor, error) {
	parsed, err := abi.JSON(bytes.NewReader(abis.LiquidatorABI))
	if err != nil {
		return nil, fmt.Errorf("failed to parse contract ABI: %w", err)
	}

	publicKey := pk.Public()
	publicKeyECDSA, ok := publicKey.(*ecdsa.PublicKey)
	if !ok {
		return nil, fmt.Errorf("error casting public key to ECDSA")
	}
	walletAddr := crypto.PubkeyToAddress(*publicKeyECDSA)

	return &TxExecutor{
		rpcClient:  rpcClient,
		contract:   contractAddr,
		privateKey: pk,
		walletAddr: walletAddr,
		chainID:    chainID,
		parsedABI:  parsed,
		logger:     logger,
		feeCache:   feeCache,
	}, nil
}

type ContractLiquidationParams struct {
	CollateralAsset common.Address
	DebtAsset       common.Address
	UserAddress     common.Address
	DebtToCover     *big.Int
	MinProfitAmount *big.Int
	PoolFee         *big.Int
}

func (ex *TxExecutor) SendLiquidation(ctx context.Context, recipe *LiquidationRecipe, calcLog zerolog.Logger) error {
	calcLog.Info().Str("user", recipe.UserAddress).Msg("开始执行清算流水线")

	colAddr := common.HexToAddress(recipe.CollateralAsset)
	debtAddr := common.HexToAddress(recipe.DebtAsset)
	userAddr := common.HexToAddress(recipe.UserAddress)

	// 从缓存中获取最优的 Uniswap V3 池费率。
	targetPoolFee, err := ex.feeCache.GetOptimalFee(recipe.CollateralAsset, recipe.DebtAsset)
	if err != nil {
		calcLog.Warn().Err(err).Msgf("未找到最优费率，回退至默认费率 %d", fallbackPoolFee)
		targetPoolFee = big.NewInt(fallbackPoolFee)
	} else {
		calcLog.Debug().Str("fee", targetPoolFee.String()).Msg("已从缓存加载最优费率")
	}

	paramsStruct := ContractLiquidationParams{
		CollateralAsset: colAddr,
		DebtAsset:       debtAddr,
		UserAddress:     userAddr,
		DebtToCover:     recipe.DebtToCover,
		MinProfitAmount: big.NewInt(minProfitAmount),
		PoolFee:         targetPoolFee,
	}

	calldata, err := ex.parsedABI.Pack("executeArbitrage", paramsStruct)
	if err != nil {
		return fmt.Errorf("failed to pack abi calldata: %w", err)
	}

	msg := ethereum.CallMsg{
		From:  ex.walletAddr,
		To:    &ex.contract,
		Value: big.NewInt(0),
		Data:  calldata,
	}

	var txHash string
	for attempt := 1; attempt <= maxRPCRetries; attempt++ {
		rpcUrl := ex.rpcClient.GetNextURL()
		rawEthClient, err := ethclient.Dial(rpcUrl)
		if err != nil {
			calcLog.Warn().Err(err).Str("url", rpcUrl).Msgf("RPC 节点拨号失败 (第 %d/%d 次尝试)", attempt, maxRPCRetries)
			continue
		}
		defer rawEthClient.Close()

		// 执行链上模拟以验证盈利能力并估算 Gas。
		gasLimit, err := rawEthClient.EstimateGas(ctx, msg)
		if err != nil {
			errStr := strings.ToLower(err.Error())
			if strings.Contains(errStr, "revert") || strings.Contains(errStr, "execution reverted") {
				return fmt.Errorf("链上模拟失败: %w", err)
			}
			calcLog.Warn().Err(err).Str("url", rpcUrl).Msg("Gas 估算 RPC 错误，尝试下一节点")
			continue
		}
		calcLog.Info().Uint64("gas_estimated", gasLimit).Msg("链上模拟通过")

		nonce, err := rawEthClient.PendingNonceAt(ctx, ex.walletAddr)
		if err != nil {
			calcLog.Warn().Err(err).Msg("获取 Nonce 失败")
			continue
		}

		gasTipCap, err := rawEthClient.SuggestGasTipCap(ctx)
		if err != nil {
			calcLog.Warn().Err(err).Msg("获取 GasTipCap 失败")
			continue
		}

		gasFeeCap, err := rawEthClient.SuggestGasPrice(ctx)
		if err != nil {
			calcLog.Warn().Err(err).Msg("获取 GasFeeCap 失败")
			continue
		}

		tx := types.NewTx(&types.DynamicFeeTx{
			ChainID:   ex.chainID,
			Nonce:     nonce,
			GasTipCap: gasTipCap,
			GasFeeCap: gasFeeCap,
			Gas:       gasLimit + gasLimitBuffer,
			To:        &ex.contract,
			Value:     big.NewInt(0),
			Data:      calldata,
		})

		signedTx, err := types.SignTx(tx, types.LatestSignerForChainID(ex.chainID), ex.privateKey)
		if err != nil {
			return fmt.Errorf("failed to sign transaction: %w", err)
		}

		err = rawEthClient.SendTransaction(ctx, signedTx)
		if err != nil {
			calcLog.Warn().Err(err).Msg("广播交易失败")
			continue
		}

		txHash = signedTx.Hash().Hex()
		break
	}

	if txHash == "" {
		return fmt.Errorf("exhausted all %d RPC retries", maxRPCRetries)
	}

	calcLog.Info().Str("tx_hash", txHash).Msg("清算交易已广播至内存池")
	return nil
}
