package config

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	liqmap "multipleexchangeliquidationmap"
	"multipleexchangeliquidationmap/internal/appctx"
	"multipleexchangeliquidationmap/internal/platform/httpx"
	"multipleexchangeliquidationmap/internal/platform/pageview"
	"multipleexchangeliquidationmap/internal/shared/pages"
)

type service struct {
	deps *appctx.Dependencies
}

func newService(deps *appctx.Dependencies) *service {
	return &service{deps: deps}
}

func (s *service) handlePage(w http.ResponseWriter, r *http.Request) {
	data := &liqmap.ModelConfigPageData{
		ModelConfig:      s.deps.Core.LoadModelConfig(),
		PageTitle:        "模型配置",
		ActiveMenu:       "config",
		ShowAnalysisInfo: false,
	}
	pageview.Serve(w, r, pages.Config(), data, pageview.Options{
		NoStore: true,
	})
}

func (s *service) handleModelConfig(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		httpx.NoStore(w)
		httpx.WriteJSON(w, http.StatusOK, s.deps.Core.LoadModelConfig())
	case http.MethodPost:
		var req liqmap.ModelConfig
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if err := s.deps.Core.SaveModelConfig(req); err != nil {
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

func (s *service) handleModelFit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpx.MethodNotAllowed(w)
		return
	}

	hours := 24
	if raw := strings.TrimSpace(r.URL.Query().Get("hours")); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n >= 1 && n <= 168 {
			hours = n
		}
	}
	minEvents := 25
	if raw := strings.TrimSpace(r.URL.Query().Get("min_events")); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n >= 5 && n <= 200 {
			minEvents = n
		}
	}
	exchange := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("exchange")))
	mode := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("mode")))

	resp, err := s.deps.Core.RunModelFit(hours, minEvents, exchange, mode)
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
