package liqmap

import (
	"strconv"
	"strings"
)

func parseCSVFloats(raw string) []float64 {
	parts := strings.Split(raw, ",")
	out := make([]float64, 0, len(parts))
	for _, p := range parts {
		v, err := strconv.ParseFloat(strings.TrimSpace(p), 64)
		if err != nil || v <= 0 {
			continue
		}
		out = append(out, v)
	}
	return out
}

func parseCSVNonNegFloats(raw string) []float64 {
	parts := strings.Split(raw, ",")
	out := make([]float64, 0, len(parts))
	for _, p := range parts {
		v, err := strconv.ParseFloat(strings.TrimSpace(p), 64)
		if err != nil || v < 0 {
			continue
		}
		out = append(out, v)
	}
	return out
}
