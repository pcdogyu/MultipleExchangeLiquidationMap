package home

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	liqmap "multipleexchangeliquidationmap"
	"multipleexchangeliquidationmap/internal/appctx"
	dbplatform "multipleexchangeliquidationmap/internal/platform/db"

	_ "modernc.org/sqlite"
)

func newTestService(t *testing.T) *service {
	t.Helper()

	dbPath := filepath.Join(t.TempDir(), "home-service.db")
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
	return newService(&appctx.Dependencies{
		Core:  core,
		Debug: false,
	})
}

func TestHandleWindowRejectsInvalidDays(t *testing.T) {
	svc := newTestService(t)

	req := httptest.NewRequest(http.MethodPost, "/api/window", strings.NewReader(`{"days":2}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	svc.handleWindow(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "invalid days") {
		t.Fatalf("unexpected body: %q", rec.Body.String())
	}
}

func TestHandleCoinGlassMapRequiresAPIKey(t *testing.T) {
	svc := newTestService(t)

	prev, had := os.LookupEnv("CG_API_KEY")
	_ = os.Unsetenv("CG_API_KEY")
	t.Cleanup(func() {
		if had {
			_ = os.Setenv("CG_API_KEY", prev)
		} else {
			_ = os.Unsetenv("CG_API_KEY")
		}
	})

	req := httptest.NewRequest(http.MethodGet, "/api/coinglass/map", nil)
	rec := httptest.NewRecorder()
	svc.handleCoinGlassMap(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "CG_API_KEY is not set") {
		t.Fatalf("unexpected body: %q", rec.Body.String())
	}
}
