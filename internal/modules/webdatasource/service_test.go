package webdatasource

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

	dbPath := filepath.Join(t.TempDir(), "webdatasource-service.db")
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

func TestHandleStatusRejectsWrongMethod(t *testing.T) {
	svc := newTestService(t)

	req := httptest.NewRequest(http.MethodPost, "/api/webdatasource/status", nil)
	rec := httptest.NewRecorder()
	svc.handleStatus(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", rec.Code)
	}
}

func TestHandleSettingsRejectsBadJSON(t *testing.T) {
	svc := newTestService(t)

	req := httptest.NewRequest(http.MethodPost, "/api/webdatasource/settings", strings.NewReader("{"))
	rec := httptest.NewRecorder()
	svc.handleSettings(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}
