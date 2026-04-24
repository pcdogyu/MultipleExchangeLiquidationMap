package liquidations

import (
	"net/http"
	"strconv"
	"strings"

	liqmap "multipleexchangeliquidationmap/internal/core"
	"multipleexchangeliquidationmap/internal/platform/httpx"
	"multipleexchangeliquidationmap/internal/platform/pageview"
	"multipleexchangeliquidationmap/internal/shared/pages"
)

func (s *service) handlePage(w http.ResponseWriter, r *http.Request) {
	pageview.Serve(w, r, pages.Liquidations(), nil, pageview.Options{})
}

func (s *service) handleLiquidations(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpx.MethodNotAllowed(w)
		return
	}

	limit := 25
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			if n > 5000 {
				n = 5000
			}
			limit = n
		}
	}
	minQty := 0.0
	if raw := strings.TrimSpace(r.URL.Query().Get("min_qty")); raw != "" {
		if v, err := strconv.ParseFloat(raw, 64); err == nil && v >= 0 {
			minQty = v
		}
	}
	minValue := 0.0
	if raw := strings.TrimSpace(r.URL.Query().Get("min_value")); raw != "" {
		if v, err := strconv.ParseFloat(raw, 64); err == nil && v >= 0 {
			minValue = v
		}
	}
	filterField := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("filter_field")))
	if filterField == "" && minQty > 0 {
		filterField = "qty"
		minValue = minQty
	}
	switch filterField {
	case "", "qty", "amount", "notional", "notional_usd":
	default:
		filterField = ""
	}
	symbol := strings.TrimSpace(r.URL.Query().Get("symbol"))
	page := 1
	if raw := strings.TrimSpace(r.URL.Query().Get("page")); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			page = n
		}
	}
	startTS := int64(0)
	if raw := strings.TrimSpace(r.URL.Query().Get("start_ts")); raw != "" {
		if n, err := strconv.ParseInt(raw, 10, 64); err == nil && n > 0 {
			startTS = n
		}
	}
	endTS := int64(0)
	if raw := strings.TrimSpace(r.URL.Query().Get("end_ts")); raw != "" {
		if n, err := strconv.ParseInt(raw, 10, 64); err == nil && n > 0 {
			endTS = n
		}
	}

	offset := (page - 1) * limit
	rows := s.core.QueryLiquidations(liqmap.LiquidationListOptions{
		Limit:       limit,
		Offset:      offset,
		StartTS:     startTS,
		EndTS:       endTS,
		Symbol:      symbol,
		FilterField: filterField,
		MinValue:    minValue,
	})

	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"page":              page,
		"page_size":         limit,
		"rows":              rows,
		"symbol":            symbol,
		"filter_field":      filterField,
		"min_value":         minValue,
		"available_symbols": s.core.LiquidationSymbols(300),
		"ws_status":         s.core.LiquidationWSStatuses(),
	})
}
