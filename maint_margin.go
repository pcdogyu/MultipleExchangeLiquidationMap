package liqmap

import (
	"fmt"
	"math"
	"strings"
)

func autoMaintMarginForLeverage(lev, mmDefault float64) float64 {
	if !(lev > 0) {
		return mmDefault
	}
	// Empirical tweak: raise MM for mid leverage so 20x short liq sits closer to
	// ~+4% band (e.g. 2271 when mark~2185).
	mm := mmDefault + 0.11/lev
	if mm < mmDefault {
		mm = mmDefault
	}
	if mm > 0.02 {
		mm = 0.02
	}
	return math.Round(mm*10000) / 10000
}

func autoMaintMarginCSV(levs []float64, mmDefault float64) string {
	parts := make([]string, 0, len(levs))
	for _, lev := range levs {
		parts = append(parts, fmt.Sprintf("%.4f", autoMaintMarginForLeverage(lev, mmDefault)))
	}
	return strings.Join(parts, ",")
}
