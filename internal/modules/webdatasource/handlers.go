package webdatasource

import (
	"encoding/json"
	"net/http"
	"strings"

	"multipleexchangeliquidationmap/internal/platform/httpx"
	"multipleexchangeliquidationmap/internal/platform/pageview"
	"multipleexchangeliquidationmap/internal/shared/pages"
)

func (s *service) handlePage(w http.ResponseWriter, r *http.Request) {
	pageview.Serve(w, r, pages.WebDataSource(), nil, pageview.Options{})
}

func (s *service) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpx.MethodNotAllowed(w)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, s.deps.Core.WebDataSourceStatus())
}

func (s *service) handleInit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpx.MethodNotAllowed(w)
		return
	}
	started, err := s.deps.Core.TriggerWebDataSourceInit()
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"started":     started,
		"timeout_sec": s.deps.Core.WebDataSourceInitLoginTimeoutSec(),
	})
}

func (s *service) handleRun(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpx.MethodNotAllowed(w)
		return
	}
	var req struct {
		WindowDays int `json:"window_days"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)

	var days *int
	if req.WindowDays == 1 || req.WindowDays == 7 || req.WindowDays == 30 {
		days = &req.WindowDays
	}
	started, err := s.deps.Core.TriggerWebDataSourceRun(days)
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"started": started})
}

func (s *service) handleRuns(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpx.MethodNotAllowed(w)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"rows": s.deps.Core.ListRecentWebDataSourceRuns(20),
	})
}

func (s *service) handleSettings(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpx.MethodNotAllowed(w)
		return
	}
	var req struct {
		Enabled     *bool  `json:"enabled"`
		IntervalMin int    `json:"interval_min"`
		TimeoutSec  int    `json:"timeout_sec"`
		ChromePath  string `json:"chrome_path"`
		ProfileDir  string `json:"profile_dir"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	status := s.deps.Core.UpdateWebDataSourceSettings(
		req.Enabled,
		req.IntervalMin,
		req.TimeoutSec,
		strings.TrimSpace(req.ChromePath),
		strings.TrimSpace(req.ProfileDir),
	)
	httpx.WriteJSON(w, http.StatusOK, status)
}

func (s *service) handleMap(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpx.MethodNotAllowed(w)
		return
	}
	window := strings.TrimSpace(r.URL.Query().Get("window"))
	if window == "" {
		window = "30d"
	}
	httpx.WriteJSON(w, http.StatusOK, s.deps.Core.WebDataSourceMap(window))
}
