package home

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
	pageview.Serve(w, r, pages.Home(), nil, pageview.Options{
		ExactPath: "/",
	})
}

func parseRequestedDays(r *http.Request) (int, bool, error) {
	raw := strings.TrimSpace(r.URL.Query().Get("days"))
	if raw == "" {
		return 0, false, nil
	}
	days, err := strconv.Atoi(raw)
	if err != nil {
		return 0, true, liqmap.BadRequestError{Message: "invalid days"}
	}
	switch days {
	case 0, 1, 7, 30:
		return days, true, nil
	default:
		return 0, true, liqmap.BadRequestError{Message: "invalid days"}
	}
}

func (s *service) handleWindow(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpx.MethodNotAllowed(w)
		return
	}
	var req struct {
		Days int `json:"days"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if err := s.deps.Core.SetWindowDays(req.Days); err != nil {
		var badRequest liqmap.BadRequestError
		if errors.As(err, &badRequest) {
			http.Error(w, badRequest.Error(), http.StatusBadRequest)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
}

func (s *service) handleDashboard(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpx.MethodNotAllowed(w)
		return
	}
	days := s.deps.Core.WindowDays()
	if requestedDays, hasRequestedDays, err := parseRequestedDays(r); err != nil {
		var badRequest liqmap.BadRequestError
		if errors.As(err, &badRequest) {
			http.Error(w, badRequest.Error(), http.StatusBadRequest)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	} else if hasRequestedDays {
		days = requestedDays
	}
	dash, err := s.deps.Core.Dashboard(days)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, dash)
}

func (s *service) handleModelLiquidationMap(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpx.MethodNotAllowed(w)
		return
	}
	windowDays := s.deps.Core.WindowDays()
	if requestedDays, hasRequestedDays, err := parseRequestedDays(r); err != nil {
		var badRequest liqmap.BadRequestError
		if errors.As(err, &badRequest) {
			http.Error(w, badRequest.Error(), http.StatusBadRequest)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	} else if hasRequestedDays {
		windowDays = requestedDays
	}
	lookbackMin := 0
	if raw := strings.TrimSpace(r.URL.Query().Get("lookback_min")); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n >= 60 && n <= 43200 {
			lookbackMin = n
		}
	}
	bucketMin := 0
	if raw := strings.TrimSpace(r.URL.Query().Get("bucket_min")); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n >= 1 && n <= 30 {
			bucketMin = n
		}
	}
	priceStep := 0.0
	if raw := strings.TrimSpace(r.URL.Query().Get("price_step")); raw != "" {
		if v, err := strconv.ParseFloat(raw, 64); err == nil && v >= 1 && v <= 50 {
			priceStep = v
		}
	}
	priceRange := 0.0
	if raw := strings.TrimSpace(r.URL.Query().Get("price_range")); raw != "" {
		if v, err := strconv.ParseFloat(raw, 64); err == nil && v >= 100 && v <= 1000 {
			priceRange = v
		}
	}
	resp, err := s.deps.Core.ModelLiquidationMap(windowDays, lookbackMin, bucketMin, priceStep, priceRange)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, resp)
}

func (s *service) handleCoinGlassMap(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpx.MethodNotAllowed(w)
		return
	}
	body, err := s.deps.Core.FetchCoinGlassMap(r.URL.Query().Get("symbol"), r.URL.Query().Get("window"))
	if err != nil {
		var badRequest liqmap.BadRequestError
		if errors.As(err, &badRequest) {
			http.Error(w, badRequest.Error(), http.StatusBadRequest)
			return
		}
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(body)
}
