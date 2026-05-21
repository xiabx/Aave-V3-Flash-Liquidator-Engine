package math

import (
	"math/big"
	"testing"
)

func TestRayMul(t *testing.T) {
	testCases := []struct {
		name     string
		a, b     *big.Int
		expected *big.Int
	}{
		{
			name:     "zero value",
			a:        big.NewInt(0),
			b:        RAY,
			expected: big.NewInt(0),
		},
		{
			name:     "one value",
			a:        WAD,
			b:        RAY,
			expected: WAD,
		},
		{
			name:     "large values",
			a:        new(big.Int).Exp(big.NewInt(10), big.NewInt(20), nil), // 100 WAD
			b:        RAY,
			expected: new(big.Int).Exp(big.NewInt(10), big.NewInt(20), nil),
		},
		{
			name:     "fractional values",
			a:        new(big.Int).Div(WAD, big.NewInt(2)), // 0.5 WAD
			b:        RAY,
			expected: new(big.Int).Div(WAD, big.NewInt(2)),
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result := RayMul(tc.a, tc.b)
			if result.Cmp(tc.expected) != 0 {
				t.Errorf("expected %s, got %s", tc.expected.String(), result.String())
			}
		})
	}
}

func TestRayDiv(t *testing.T) {
	testCases := []struct {
		name     string
		a, b     *big.Int
		expected *big.Int
	}{
		{
			name:     "zero denominator",
			a:        WAD,
			b:        big.NewInt(0),
			expected: big.NewInt(0),
		},
		{
			name:     "one value",
			a:        WAD,
			b:        WAD,
			expected: RAY,
		},
		{
			name:     "large values",
			a:        new(big.Int).Exp(big.NewInt(10), big.NewInt(20), nil), // 100 WAD
			b:        WAD,
			expected: new(big.Int).Mul(big.NewInt(100), RAY),
		},
		{
			name:     "fractional values",
			a:        new(big.Int).Div(WAD, big.NewInt(2)), // 0.5 WAD
			b:        WAD,
			expected: new(big.Int).Div(RAY, big.NewInt(2)),
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result := RayDiv(tc.a, tc.b)
			if result.Cmp(tc.expected) != 0 {
				t.Errorf("expected %s, got %s", tc.expected.String(), result.String())
			}
		})
	}
}
