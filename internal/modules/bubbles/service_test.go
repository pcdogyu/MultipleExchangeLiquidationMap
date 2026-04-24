package bubbles

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

	dbPath := filepath.Join(t.TempDir(), "bubbles-service.db")
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
	return newService(liqmap.NewBubblesModuleServices(core))
}

func TestHandleKlinesRejectsUnsupportedInterval(t *testing.T) {
	svc := newTestService(t)

	req := httptest.NewRequest(http.MethodGet, "/api/klines?interval=bad", nil)
	rec := httptest.NewRecorder()
	svc.handleKlines(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "unsupported interval") {
		t.Fatalf("unexpected body: %q", rec.Body.String())
	}
}

func TestHandleOKXLatestCloseRejectsWrongMethod(t *testing.T) {
	svc := newTestService(t)

	req := httptest.NewRequest(http.MethodPost, "/api/okx/latest-close", nil)
	rec := httptest.NewRecorder()
	svc.handleOKXLatestClose(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", rec.Code)
	}
}
