package bubbles

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	liqmap "multipleexchangeliquidationmap/internal/core"
	"multipleexchangeliquidationmap/internal/platform/httpx"
	"multipleexchangeliquidationmap/internal/platform/pageview"
	"multipleexchangeliquidationmap/internal/shared/pages"
)

type Services interface {
	FetchKlines(interval string, limit int, startTS, endTS int64) (map[string]any, error)
	LatestOKXClose() (map[string]any, error)
	TradeSignals(opts liqmap.TradeSignalOptions) []liqmap.TradeSignal
}

type service struct {
	core Services
}

func newService(core Services) *service {
	return &service{core: core}
}

func (s *service) handlePage(w http.ResponseWriter, r *http.Request) {
	pageview.Serve(w, r, pages.Bubbles(), nil, pageview.Options{})
}

func (s *service) handleKlines(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpx.MethodNotAllowed(w)
		return
	}

	limit := 300
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n >= 50 && n <= 1000 {
			limit = n
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

	resp, err := s.core.FetchKlines(r.URL.Query().Get("interval"), limit, startTS, endTS)
	if err != nil {
		var badRequest liqmap.BadRequestError
		if errors.As(err, &badRequest) {
			http.Error(w, badRequest.Error(), http.StatusBadRequest)
			return
		}
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, resp)
}

func (s *service) handleOKXLatestClose(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpx.MethodNotAllowed(w)
		return
	}

	resp, err := s.core.LatestOKXClose()
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, resp)
}

func (s *service) handleTradeSignals(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpx.MethodNotAllowed(w)
		return
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
	signals := s.core.TradeSignals(liqmap.TradeSignalOptions{
		StartTS:  startTS,
		EndTS:    endTS,
		Symbol:   r.URL.Query().Get("symbol"),
		Interval: r.URL.Query().Get("interval"),
	})
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"signals": signals,
	})
}
