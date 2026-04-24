package bookmap

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	liqmap "multipleexchangeliquidationmap"
	"multipleexchangeliquidationmap/internal/platform/httpx"
	"multipleexchangeliquidationmap/internal/platform/pageview"
	"multipleexchangeliquidationmap/internal/shared/pages"
)

func (s *service) handlePage(w http.ResponseWriter, r *http.Request) {
	pageview.Serve(w, r, pages.Bookmap(), nil, pageview.Options{})
}

func (s *service) handleOrderBook(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpx.MethodNotAllowed(w)
		return
	}

	limit := 20
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 && n <= 200 {
			limit = n
		}
	}
	resp, err := s.deps.Core.OrderBookView(r.URL.Query().Get("exchange"), r.URL.Query().Get("mode"), limit)
	if err != nil {
		var badRequest liqmap.BadRequestError
		if errors.As(err, &badRequest) {
			http.Error(w, badRequest.Error(), http.StatusBadRequest)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, resp)
}

func (s *service) handlePriceEvents(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		q := r.URL.Query()
		side := strings.ToLower(strings.TrimSpace(q.Get("side")))
		mode := strings.ToLower(strings.TrimSpace(q.Get("mode")))
		hasPaging := strings.TrimSpace(q.Get("page")) != "" || strings.TrimSpace(q.Get("limit")) != "" || side != "" || mode != "" || strings.TrimSpace(q.Get("minutes")) != ""
		page := 1
		limit := 25
		minutes := 30
		if hasPaging {
			if raw := strings.TrimSpace(q.Get("page")); raw != "" {
				if n, err := strconv.Atoi(raw); err == nil && n >= 1 && n <= 100000 {
					page = n
				}
			}
			if raw := strings.TrimSpace(q.Get("limit")); raw != "" {
				if n, err := strconv.Atoi(raw); err == nil && n >= 1 && n <= 200 {
					limit = n
				}
			}
			if raw := strings.TrimSpace(q.Get("minutes")); raw != "" {
				if n, err := strconv.Atoi(raw); err == nil && n >= 1 && n <= 24*60 {
					minutes = n
				}
			}
		}
		resp, err := s.deps.Core.ListPriceWallEvents(page, limit, minutes, side, mode)
		if err != nil {
			var badRequest liqmap.BadRequestError
			if errors.As(err, &badRequest) {
				http.Error(w, badRequest.Error(), http.StatusBadRequest)
				return
			}
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, resp)
	case http.MethodPost:
		var req liqmap.PriceWallEvent
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if err := s.deps.Core.RecordPriceWallEvent(req); err != nil {
			var badRequest liqmap.BadRequestError
			if errors.As(err, &badRequest) {
				http.Error(w, badRequest.Error(), http.StatusBadRequest)
				return
			}
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		httpx.MethodNotAllowed(w)
	}
}
