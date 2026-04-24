package channel

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	liqmap "multipleexchangeliquidationmap"
)

type stubServices struct {
	settings liqmap.ChannelSettings
}

func (s *stubServices) LoadSettings() liqmap.ChannelSettings {
	return s.settings
}

func (s *stubServices) SaveSettings(req liqmap.ChannelSettings) error {
	s.settings = req
	return nil
}

func (s *stubServices) TriggerChannelTestSend() (string, bool) {
	return "queued", true
}

func (s *stubServices) ListTelegramSendHistory(limit int) ([]liqmap.TelegramSendHistoryRow, error) {
	return nil, nil
}

func (s *stubServices) ListChannelTimeline(hours int) ([]liqmap.ChannelTimelineRow, error) {
	return nil, nil
}

func (s *stubServices) ListChannelPlannedPushes(hours int) []liqmap.ChannelPlannedPushRow {
	return nil
}

func newTestService() *service {
	return newService(&stubServices{})
}

func TestHandleSettingsPersistsAndReturnsSettings(t *testing.T) {
	svc := newTestService()

	postReq := httptest.NewRequest(http.MethodPost, "/api/settings", strings.NewReader(`{
		"telegram_bot_token":"bot-token",
		"telegram_channel":"channel-id",
		"notify_work_interval_min":30,
		"notify_off_interval_min":45,
		"work_time_expr":"09:00-18:00",
		"notify_enabled":true
	}`))
	postReq.Header.Set("Content-Type", "application/json")
	postRec := httptest.NewRecorder()
	svc.handleSettings(postRec, postReq)
	if postRec.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", postRec.Code)
	}

	getReq := httptest.NewRequest(http.MethodGet, "/api/settings", nil)
	getRec := httptest.NewRecorder()
	svc.handleSettings(getRec, getReq)
	if getRec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", getRec.Code)
	}

	body := getRec.Body.String()
	for _, want := range []string{`"telegram_bot_token":"bot-token"`, `"telegram_channel":"channel-id"`, `"notify_work_interval_min":30`, `"notify_off_interval_min":45`, `"notify_enabled":true`} {
		if !strings.Contains(body, want) {
			t.Fatalf("expected response to contain %s, got %s", want, body)
		}
	}
}

func TestHandleChannelTestRequiresConfiguredDestination(t *testing.T) {
	svc := newTestService()

	req := httptest.NewRequest(http.MethodPost, "/api/channel/test", nil)
	rec := httptest.NewRecorder()
	svc.handleChannelTest(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "telegram bot token or channel is empty") {
		t.Fatalf("unexpected body: %q", rec.Body.String())
	}
}
