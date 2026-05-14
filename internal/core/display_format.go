package liqmap

import (
	"fmt"
	"math"
)

func formatYi1(v float64) string {
	return fmt.Sprintf("%.1f", v/1e8)
}

func formatYi2(v float64) string {
	return fmt.Sprintf("%.2f", v/1e8)
}

func formatPrice1(v float64) string {
	return fmt.Sprintf("%.1f", v)
}

func roundDisplayPrice1(v float64) float64 {
	if v <= 0 {
		return 0
	}
	return math.Round(v*10) / 10
}

func formatPointDistance(v float64) string {
	return fmt.Sprintf("%.1f", math.Abs(v))
}

func humanizeCompactUSD(v float64) string {
	abs := math.Abs(v)
	switch {
	case abs >= 1e8:
		return fmt.Sprintf("%.2f亿", v/1e8)
	case abs >= 1e4:
		return fmt.Sprintf("%.1f万", v/1e4)
	default:
		return fmt.Sprintf("%.0f", v)
	}
}
