package channel

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	liqmap "multipleexchangeliquidationmap"
	dbplatform "multipleexchangeliquidationmap/internal/platform/db"

	_ "modernc.org/sqlite"
)

func newTestService(t *testing.T) *service {
	t.Helper()

	dbPath := filepath.Join(t.TempDir(), "channel-service.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if err := dbplatform.Configure(db); err != nil {
		t.Fatalf("configure db: %v", err)
	}
	if err := dbplatform.Init(db); err != nil {
		t.Fatalf("init db: %v", err)
	}

	core := liqmap.NewApp(db, false)
	return newService(core)
}

func TestHandleSettingsPersistsAndReturnsSettings(t *testing.T) {
	svc := newTestService(t)

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
	svc := newTestService(t)

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
