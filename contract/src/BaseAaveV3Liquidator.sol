// SPDX-License-Identifier: MIT
pragma solidity ^0.8.20;

import {IPoolAddressesProvider} from "@aave-v3-core/contracts/interfaces/IPoolAddressesProvider.sol";
import {FlashLoanSimpleReceiverBase} from "@aave-v3-core/contracts/flashloan/base/FlashLoanSimpleReceiverBase.sol";
import {Ownable} from "@openzeppelin/contracts/access/Ownable.sol";
import {IERC20} from "@openzeppelin/contracts/token/ERC20/IERC20.sol";
import {SafeERC20} from "@openzeppelin/contracts/token/ERC20/utils/SafeERC20.sol";

interface ISwapRouter {
    struct ExactInputSingleParams {
        address tokenIn;
        address tokenOut;
        uint24 fee;
        address recipient;
        uint256 amountIn;
        uint256 amountOutMinimum;
        uint160 sqrtPriceLimitX96;
    }
    function exactInputSingle(ExactInputSingleParams calldata params) external returns (uint256 amountOut);
}

contract BaseAaveV3Liquidator is FlashLoanSimpleReceiverBase, Ownable {
    using SafeERC20 for IERC20;

    error InvalidRouterAddress();
    error CallerNotPool();
    error InvalidInitiator();
    error LiquidationFailedNoCollateral();
    error UnprofitableArbitrage(uint256 expectedMinProfit, uint256 actualNetProfit);
    error NothingToRescue();

    event ArbitrageSuccess(
        address indexed targetUser,      // 被清算的用户地址
        address indexed collateralAsset, // 缴获的抵押物
        address indexed debtAsset,       // 偿还的债务资产
        uint256 debtCovered,             // 实际代还的债务数量
        uint256 collateralSeized,        // 实际收获的抵押物数量
        uint256 netProfit                // 最终净利润
    );

    ISwapRouter public immutable DEX_ROUTER;

    struct LiquidationParams {
        address collateralAsset;
        address debtAsset;
        address userAddress;
        uint256 debtToCover;
        uint256 minProfitAmount;
        uint24 poolFee;
    }

    constructor(address _addressProvider, address _swapRouter)
    FlashLoanSimpleReceiverBase(IPoolAddressesProvider(_addressProvider))
    Ownable(msg.sender)
    {
        if (_swapRouter == address(0)) revert InvalidRouterAddress();
        DEX_ROUTER = ISwapRouter(_swapRouter);
    }

    function executeArbitrage(LiquidationParams calldata params) external onlyOwner {
        bytes memory encodedParams = abi.encode(params);
        POOL.flashLoanSimple(
            address(this),
            params.debtAsset,
            params.debtToCover,
            encodedParams,
            0
        );
    }

    function executeOperation(
        address asset,
        uint256 amount,
        uint256 premium,
        address initiator,
        bytes calldata params
    ) external override returns (bool) {
        if (msg.sender != address(POOL)) revert CallerNotPool();
        if (initiator != address(this)) revert InvalidInitiator();

        LiquidationParams memory liqParams = abi.decode(params, (LiquidationParams));

        IERC20(asset).forceApprove(address(POOL), amount);
        POOL.liquidationCall(
            liqParams.collateralAsset,
            asset,
            liqParams.userAddress,
            amount,
            false
        );

        uint256 actualCollateral;
        {
            actualCollateral = IERC20(liqParams.collateralAsset).balanceOf(address(this));
            if (actualCollateral == 0) revert LiquidationFailedNoCollateral();

            IERC20(liqParams.collateralAsset).forceApprove(address(DEX_ROUTER), actualCollateral);

            ISwapRouter.ExactInputSingleParams memory swapParams = ISwapRouter.ExactInputSingleParams({
                tokenIn: liqParams.collateralAsset,
                tokenOut: liqParams.debtAsset,
                fee: liqParams.poolFee,
                recipient: address(this),
                amountIn: actualCollateral,
                amountOutMinimum: 0,
                sqrtPriceLimitX96: 0
            });

            DEX_ROUTER.exactInputSingle(swapParams);
            IERC20(liqParams.collateralAsset).forceApprove(address(DEX_ROUTER), 0);
        }

        uint256 amountToOwe = amount + premium;
        uint256 finalAssetBalance = IERC20(asset).balanceOf(address(this));

        if (finalAssetBalance < amountToOwe) {
            revert UnprofitableArbitrage(liqParams.minProfitAmount, 0);
        }

        uint256 netProfit = finalAssetBalance - amountToOwe;
        if (netProfit < liqParams.minProfitAmount) {
            revert UnprofitableArbitrage(liqParams.minProfitAmount, netProfit);
        }

        if (netProfit > 0) {
            IERC20(asset).safeTransfer(owner(), netProfit);
        }

        IERC20(asset).forceApprove(address(POOL), amountToOwe);

        emit ArbitrageSuccess(
            liqParams.userAddress,
            liqParams.collateralAsset,
            liqParams.debtAsset,
            liqParams.debtToCover,
            actualCollateral,
            netProfit
        );

        return true;
    }

    function rescueTokens(address token) external onlyOwner {
        uint256 balance = IERC20(token).balanceOf(address(this));
        if (balance == 0) revert NothingToRescue();
        IERC20(token).safeTransfer(owner(), balance);
    }
}