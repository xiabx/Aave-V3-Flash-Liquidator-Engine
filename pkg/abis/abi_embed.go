package abis

import _ "embed"

//go:embed aave_v3.6.json
var AaveV36Pool []byte

//go:embed aToken.json
var ATokenABI []byte

//go:embed vToken.json
var VTokenABI []byte

//go:embed multicall3.json
var MulticallABI []byte

//go:embed factory.json
var FactoryABI []byte

//go:embed erc20.json
var Erc20ABI []byte

//go:embed oracle.json
var OracleABI []byte

//go:embed liquidator.json
var LiquidatorABI []byte

//go:embed aave_emode.json
var AaveEModeABI []byte
