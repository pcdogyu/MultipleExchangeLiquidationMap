package channel

import (
	"encoding/json"
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
	pageview.Serve(w, r, pages.Channel(), s.deps.Core.LoadSettings(), pageview.Options{})
}

func (s *service) handleSettings(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		httpx.WriteJSON(w, http.StatusOK, s.deps.Core.LoadSettings())
	case http.MethodPost:
		var req liqmap.ChannelSettings
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if err := s.deps.Core.SaveSettings(req); err != nil {
			if strings.Contains(err.Error(), "work time range must") {
				http.Error(w, err.Error(), http.StatusBadRequest)
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

func (s *service) handleChannelTest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpx.MethodNotAllowed(w)
		return
	}
	settings := s.deps.Core.LoadSettings()
	if settings.TelegramBotToken == "" || settings.TelegramChannel == "" {
		http.Error(w, "telegram bot token or channel is empty", http.StatusBadRequest)
		return
	}
	msg, _ := s.deps.Core.TriggerChannelTestSend()
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusAccepted)
	_, _ = w.Write([]byte(msg))
}

func (s *service) handleChannelHistory(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpx.MethodNotAllowed(w)
		return
	}
	limit := 15
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 && n <= 200 {
			limit = n
		}
	}
	rows, err := s.deps.Core.ListTelegramSendHistory(limit)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, rows)
}

func (s *service) handleChannelTimeline(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpx.MethodNotAllowed(w)
		return
	}
	hours := 24
	limit := 24
	if raw := strings.TrimSpace(r.URL.Query().Get("hours")); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 && n <= 168 {
			hours = n
		}
	}
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 && n <= 200 {
			limit = n
		}
	}
	rows, err := s.deps.Core.ListChannelTimeline(hours)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if len(rows) > limit {
		rows = rows[:limit]
	}
	httpx.WriteJSON(w, http.StatusOK, rows)
}

func (s *service) handleChannelSchedule(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpx.MethodNotAllowed(w)
		return
	}
	hours := 24
	if raw := strings.TrimSpace(r.URL.Query().Get("hours")); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 && n <= 72 {
			hours = n
		}
	}
	httpx.WriteJSON(w, http.StatusOK, s.deps.Core.ListChannelPlannedPushes(hours))
}
