package liqmap

import (
	"strconv"
	"strings"
)

func parsePositiveIntSetting(raw string, fallback int) int {
	if n, err := strconv.Atoi(strings.TrimSpace(raw)); err == nil && n > 0 {
		return n
	}
	return fallback
}

func parseBoolSetting(raw string, fallback bool) bool {
	s := strings.ToLower(strings.TrimSpace(raw))
	if s == "" {
		return fallback
	}
	return s == "1" || s == "true" || s == "yes" || s == "on"
}
