package liqmap

import (
	"math"
	"strconv"
	"strings"
)

func isFinite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }

func nullableFloat(v float64) any {
	if !(v > 0) {
		return nil
	}
	return v
}

func clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func parseFloat(raw string) float64 {
	v, _ := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	return v
}
