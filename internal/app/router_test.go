package app_test

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"multipleexchangeliquidationmap/internal/app"
	liqmap "multipleexchangeliquidationmap/internal/core"
	dbplatform "multipleexchangeliquidationmap/internal/platform/db"

	_ "modernc.org/sqlite"
)

func newTestRouter(t *testing.T) *http.ServeMux {
	t.Helper()

	dbPath := filepath.Join(t.TempDir(), "router-test.db")
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
	return app.NewRouter(core, false)
}

func TestNewRouterRegistersMenuPages(t *testing.T) {
	mux := newTestRouter(t)
	paths := []string{
		"/",
		"/config",
		"/monitor?days=30",
		"/map",
		"/liquidations",
		"/bubbles",
		"/analysis",
		"/webdatasource",
		"/channel",
	}

	for _, path := range paths {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code == http.StatusNotFound {
			t.Fatalf("expected route %s to be mounted, got 404", path)
		}
	}
}

func TestNewRouterRegistersCompatibilityAPIs(t *testing.T) {
	mux := newTestRouter(t)
	cases := []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/api/dashboard"},
		{http.MethodGet, "/api/model-config"},
		{http.MethodGet, "/api/model-fit?hours=24&min_events=10"},
		{http.MethodGet, "/api/analysis"},
		{http.MethodGet, "/api/analysis-backtest-liquidation"},
		{http.MethodGet, "/api/liquidations?page=1&limit=10"},
		{http.MethodGet, "/api/klines?interval=1h&limit=10"},
		{http.MethodGet, "/api/orderbook"},
		{http.MethodGet, "/api/channel/history"},
		{http.MethodGet, "/api/webdatasource/status"},
		{http.MethodGet, "/api/version"},
	}

	for _, tc := range cases {
		req := httptest.NewRequest(tc.method, tc.path, nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code == http.StatusNotFound {
			t.Fatalf("expected route %s %s to be mounted, got 404", tc.method, tc.path)
		}
	}
}
