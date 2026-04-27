package analysis

import (
	"net/http"
	"strconv"
	"strings"

	"multipleexchangeliquidationmap/internal/platform/httpx"
	"multipleexchangeliquidationmap/internal/platform/pageview"
	"multipleexchangeliquidationmap/internal/shared/pages"
)

func (s *service) handlePage(w http.ResponseWriter, r *http.Request) {
	pageview.Serve(w, r, pages.Analysis(), nil, pageview.Options{
		NoStore: true,
	})
}

func (s *service) handleBacktestPage(w http.ResponseWriter, r *http.Request) {
	pageview.Serve(w, r, pages.AnalysisBacktest(), nil, pageview.Options{
		NoStore: true,
	})
}

func (s *service) handleAnalysis(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpx.MethodNotAllowed(w)
		return
	}

	resp, err := s.core.AnalysisSnapshot()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, resp)
}

func (s *service) handleBacktest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpx.MethodNotAllowed(w)
		return
	}
	hours := 720
	if raw := strings.TrimSpace(r.URL.Query().Get("hours")); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 && n <= 24*90 {
			hours = n
		}
	}
	minConfidence := 90.0
	if raw := strings.TrimSpace(r.URL.Query().Get("min_confidence")); raw != "" {
		if n, err := strconv.ParseFloat(raw, 64); err == nil && n >= 0 && n <= 100 {
			minConfidence = n
		}
	}
	horizons := []int{5, 15, 30, 60}
	if raw := strings.TrimSpace(r.URL.Query().Get("horizons")); raw != "" {
		parsed := make([]int, 0, 4)
		for _, part := range strings.Split(raw, ",") {
			if n, err := strconv.Atoi(strings.TrimSpace(part)); err == nil && n > 0 {
				parsed = append(parsed, n)
			}
		}
		if len(parsed) > 0 {
			horizons = parsed
		}
	}
	resp, err := s.core.AnalysisBacktest(hours, minConfidence, horizons)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, resp)
}

func (s *service) handleBacktestHistory(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpx.MethodNotAllowed(w)
		return
	}
	limit := 50
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 && n <= 200 {
			limit = n
		}
	}
	page := 1
	if raw := strings.TrimSpace(r.URL.Query().Get("page")); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			page = n
		}
	}
	resp, err := s.core.AnalysisBacktestHistory(limit, page)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, resp)
}
