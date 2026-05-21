// SPDX-License-Identifier: MIT
pragma solidity ^0.8.20;

import {Test} from "forge-std/Test.sol";
import {console2} from "forge-std/console2.sol";
import {IERC20} from "@openzeppelin/contracts/token/ERC20/IERC20.sol";
import {BaseAaveV3Liquidator} from "../src/BaseAaveV3Liquidator.sol";

interface IAaveV3Pool {
    function supply(address asset, uint256 amount, address onBehalfOf, uint16 referralCode) external;
    function borrow(address asset, uint256 amount, uint256 interestRateMode, uint16 referralCode, address onBehalfOf) external;
    function getUserAccountData(address user) external view returns (
        uint256 totalCollateralBase,
        uint256 totalDebtBase,
        uint256 availableBorrowsBase,
        uint256 currentLiquidationThreshold,
        uint256 ltv,
        uint256 healthFactor
    );
    function flashLoanSimple(
        address receiverAddress,
        address asset,
        uint256 amount,
        bytes calldata params,
        uint16 referralCode
    ) external;
}

contract BaseAaveV3LiquidatorTest is Test {
    address constant ADDRESS_PROVIDER = 0xe20fCBdBfFC4Dd138cE8b2E6FBb6CB49777ad64D;
    address constant UNISWAP_V3_ROUTER = 0x2626664c2603336E57B271c5C0b26F421741e481;
    address constant AAVE_POOL = 0xA238Dd80C259a72e81d7e4664a9801593F98d1c5;
    address constant AAVE_ORACLE = 0x2Cc0Fc26eD4563A5ce5e8bdcfe1A2878676Ae156;

    address constant WETH = 0x4200000000000000000000000000000000000006;
    address constant USDC = 0x833589fCD6eDb6E08f4c7C32D4f71b54bdA02913;

    BaseAaveV3Liquidator public liquidator;
    address public borrower = address(0x1111);
    address public botOwner = address(0x2222);

    function setUp() public {
        string memory baseRpcUrl = vm.envOr("RPC_URLS", string("https://mainnet.base.org"));
        vm.createSelectFork(baseRpcUrl);

        vm.label(ADDRESS_PROVIDER, "AaveProvider");
        vm.label(AAVE_POOL, "AavePool");
        vm.label(AAVE_ORACLE, "AaveOracle");
        vm.label(UNISWAP_V3_ROUTER, "UniV3Router");
        vm.label(WETH, "WETH");
        vm.label(USDC, "USDC");
        vm.label(borrower, "Borrower");
        vm.label(botOwner, "BotOwner");

        vm.prank(botOwner);
        liquidator = new BaseAaveV3Liquidator(ADDRESS_PROVIDER, UNISWAP_V3_ROUTER);
    }

    function test_LiquidationFlowSuccess() public {
        uint256 depositAmount = 10 ether;

        deal(WETH, borrower, depositAmount);

        vm.startPrank(borrower);
        IERC20(WETH).approve(AAVE_POOL, type(uint256).max);
        IAaveV3Pool(AAVE_POOL).supply(WETH, depositAmount, borrower, 0);

        uint256 borrowAmount = 15000 * 10 ** 6;
        IAaveV3Pool(AAVE_POOL).borrow(USDC, borrowAmount, 2, 0, borrower);
        vm.stopPrank();

        (,,,,, uint256 hfBefore) = IAaveV3Pool(AAVE_POOL).getUserAccountData(borrower);
        assertGt(hfBefore, 1e18, "Initial health factor must be safe");
        console2.log("Initial Borrower Health Factor:", hfBefore);

        bytes memory mockPriceData = abi.encode(1000 * 10 ** 8);
        vm.mockCall(
            AAVE_ORACLE,
            abi.encodeWithSignature("getAssetPrice(address)", WETH),
            mockPriceData
        );

        (,,,,, uint256 hfAfter) = IAaveV3Pool(AAVE_POOL).getUserAccountData(borrower);
        assertLt(hfAfter, 1e18, "Health factor must be underwater (< 1.0)");
        console2.log("Underwater Borrower Health Factor:", hfAfter);

        uint256 debtToCover = borrowAmount / 2;

        BaseAaveV3Liquidator.LiquidationParams memory params = BaseAaveV3Liquidator.LiquidationParams({
            collateralAsset: WETH,
            debtAsset: USDC,
            userAddress: borrower,
            debtToCover: debtToCover,
            minProfitAmount: 10 * 10 ** 6,
            poolFee: 500
        });

        vm.startPrank(botOwner);
        uint256 ownerBalanceBefore = IERC20(USDC).balanceOf(botOwner);

        vm.expectEmit(true, true, true, false);
        emit BaseAaveV3Liquidator.ArbitrageSuccess(borrower, WETH, USDC, debtToCover, 0, 0);
        liquidator.executeArbitrage(params);
        vm.stopPrank();

        uint256 ownerBalanceAfter = IERC20(USDC).balanceOf(botOwner);
        assertGt(ownerBalanceAfter, ownerBalanceBefore, "Should clear a net profit of USDC transferred to owner");
        console2.log("Net Profit Swept to Owner (USDC):", (ownerBalanceAfter - ownerBalanceBefore) / 1e6);
    }
}