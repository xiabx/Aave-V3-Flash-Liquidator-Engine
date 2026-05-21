package math

import (
	"fmt"
	"math/big"
	"strings"

	"golang.org/x/crypto/sha3"
)

const (
	// DustThreshold 是过滤无效粉尘金额的最小阈值。
	DustThreshold int64 = 10
)

var (
	// RAY 表示 10^27，用于流动性和借贷指数的精度计算。
	RAY = new(big.Int).Exp(big.NewInt(10), big.NewInt(27), nil)

	// WAD 表示 10^18，用于健康因子计算的精度。
	WAD = new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil)

	// HFSafetyThreshold 是健康因子的安全阈值（WAD 精度下为 1.05）。
	HFSafetyThreshold = new(big.Int).Mul(big.NewInt(105), new(big.Int).Exp(big.NewInt(10), big.NewInt(16), nil))

	// HFLiquidationThreshold 是清算的严格阈值（WAD 精度下为 1.0）。
	HFLiquidationThreshold = WAD
)

// RayDiv 实现 WAD/RAY 除法，将实际余额转换为缩放余额。
func RayDiv(a, b *big.Int) *big.Int {
	if b.Cmp(big.NewInt(0)) == 0 {
		return big.NewInt(0)
	}
	halfB := new(big.Int).Div(b, big.NewInt(2))
	numerator := new(big.Int).Mul(a, RAY)
	numerator.Add(numerator, halfB)
	return new(big.Int).Div(numerator, b)
}

// RayMul 实现 WAD/RAY 乘法，将缩放余额转换为实际余额。
func RayMul(a, b *big.Int) *big.Int {
	halfRay := new(big.Int).Div(RAY, big.NewInt(2))
	res := new(big.Int).Mul(a, b)
	res.Add(res, halfRay)
	return res.Div(res, RAY)
}

// PadLeftZero 为 ABI 编码在十六进制字符串左侧填充零。
func PadLeftZero(hexStr string, length int) string {
	clean := strings.TrimPrefix(hexStr, "0x")
	if len(clean) >= length {
		return clean
	}
	return strings.Repeat("0", length-len(clean)) + clean
}

// ParseHexAddress 从 ABI 编码的主题中提取 20 字节地址。
func ParseHexAddress(hexStr string) string {
	hexStr = strings.TrimPrefix(hexStr, "0x")
	if len(hexStr) >= 40 {
		return "0x" + strings.ToLower(hexStr[len(hexStr)-40:])
	}
	return "0x" + strings.ToLower(hexStr)
}

// ParseHexBigInt 将十六进制字符串解析为 big.Int。
func ParseHexBigInt(hexStr string) *big.Int {
	hexStr = strings.TrimPrefix(hexStr, "0x")
	if hexStr == "" {
		return big.NewInt(0)
	}
	val, ok := new(big.Int).SetString(hexStr, 16)
	if !ok {
		return big.NewInt(0)
	}
	return val
}

// FormatUSD 将带有 8 位小数的 big.Int 转换为格式化的 USD 字符串。
func FormatUSD(val *big.Int) string {
	if val == nil {
		return "0.00"
	}
	f := new(big.Float).SetInt(val)
	base := new(big.Float).SetFloat64(1e8)
	f.Quo(f, base)
	return f.Text('f', 2)
}

// FormatHF 将 WAD 精度的 big.Int 格式化为带 4 位小数的人类可读字符串。
func FormatHF(hf *big.Int) string {
	if hf == nil {
		return "∞"
	}
	f := new(big.Float).SetInt(hf)
	wad := new(big.Float).SetFloat64(1e18)
	f.Quo(f, wad)
	return f.Text('f', 4)
}

// GetMethodID 计算给定签名的 4 字节函数选择器。
func GetMethodID(signature string) string {
	hash := sha3.NewLegacyKeccak256()
	hash.Write([]byte(signature))
	return fmt.Sprintf("0x%x", hash.Sum(nil)[:4])
}

// GetTopicHash 计算事件签名的 32 字节 Keccak-256 哈希值。
func GetTopicHash(signature string) string {
	hash := sha3.NewLegacyKeccak256()
	hash.Write([]byte(signature))
	return fmt.Sprintf("0x%x", hash.Sum(nil))
}
