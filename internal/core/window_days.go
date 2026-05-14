package liqmap

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

func normalizeWindowDays(days int) (int, bool) {
	switch days {
	case windowIntraday, 1, 7, 30:
		return days, true
	default:
		return 0, false
	}
}

func requestedWindowDays(r *http.Request) (int, bool, error) {
	raw := strings.TrimSpace(r.URL.Query().Get("days"))
	if raw == "" {
		return 0, false, nil
	}
	days, err := strconv.Atoi(raw)
	if err != nil {
		return 0, true, fmt.Errorf("invalid days")
	}
	if normalized, ok := normalizeWindowDays(days); ok {
		return normalized, true, nil
	}
	return 0, true, fmt.Errorf("invalid days")
}
