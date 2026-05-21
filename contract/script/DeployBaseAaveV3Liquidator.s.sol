// SPDX-License-Identifier: MIT
pragma solidity ^0.8.20;

import {Script} from "forge-std/Script.sol";
import {console2} from "forge-std/console2.sol";
import {BaseAaveV3Liquidator} from "../src/BaseAaveV3Liquidator.sol";

contract DeployBaseAaveV3Liquidator is Script {
    function run() external {
        uint256 deployerPrivateKey = vm.envUint("BOT_PRIVATE_KEY");

        address addressProvider = vm.envOr(
            "AAVE_ADDRESS_PROVIDER",
            address(0xe20fCBdBfFC4Dd138cE8b2E6FBb6CB49777ad64D)
        );
        address uniswapV3Router = vm.envOr(
            "UNISWAP_V3_ROUTER",
            address(0x2626664c2603336E57B271c5C0b26F421741e481)
        );

        require(addressProvider != address(0), "Error: AAVE_ADDRESS_PROVIDER is zero");
        require(uniswapV3Router != address(0), "Error: UNISWAP_V3_ROUTER is zero");

        address deployerAddress = vm.addr(deployerPrivateKey);

        console2.log("=================================================");
        console2.log("Deploying BaseAaveV3Liquidator...");
        console2.log("Deployer Address: ", deployerAddress);
        console2.log("Deployer Gas Balance: ", deployerAddress.balance);
        console2.log("Target Aave AddressesProvider: ", addressProvider);
        console2.log("Target Uniswap V3 Router: ", uniswapV3Router);
        console2.log("=================================================");

        vm.startBroadcast(deployerPrivateKey);

        BaseAaveV3Liquidator liquidator = new BaseAaveV3Liquidator(
            addressProvider,
            uniswapV3Router
        );

        vm.stopBroadcast();

        console2.log("=================================================");
        console2.log("Deployment Complete!");
        console2.log("Contract deployed at address: ", address(liquidator));
        console2.log("=================================================");
    }
}