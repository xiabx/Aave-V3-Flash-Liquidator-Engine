package conv

import (
	"math/big"
	"strings"
)

// HexToDecString 十六进制字符串转十进制字符串
func HexToDecString(hexStr string) string {
	hexStr = strings.TrimPrefix(hexStr, "0x")
	hexStr = strings.TrimPrefix(hexStr, "0X")

	if hexStr == "" {
		return ""
	}

	n := new(big.Int)
	_, ok := n.SetString(hexStr, 16)
	if !ok {
		return ""
	}

	return n.String()
}
