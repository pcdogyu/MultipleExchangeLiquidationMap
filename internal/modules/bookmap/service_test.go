package bookmap

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

	dbPath := filepath.Join(t.TempDir(), "bookmap-service.db")
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
	return newService(liqmap.NewBookmapModuleServices(core))
}

func TestHandlePriceEventsPersistsAndListsRows(t *testing.T) {
	svc := newTestService(t)

	postReq := httptest.NewRequest(http.MethodPost, "/api/price-events", strings.NewReader(`{
		"side":"bid",
		"price":3200.5,
		"peak":1500000,
		"duration_ms":1200,
		"mode":"weighted"
	}`))
	postReq.Header.Set("Content-Type", "application/json")
	postRec := httptest.NewRecorder()
	svc.handlePriceEvents(postRec, postReq)
	if postRec.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d, body=%s", postRec.Code, postRec.Body.String())
	}

	getReq := httptest.NewRequest(http.MethodGet, "/api/price-events?side=bid&page=1&limit=10&minutes=30", nil)
	getRec := httptest.NewRecorder()
	svc.handlePriceEvents(getRec, getReq)
	if getRec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d, body=%s", getRec.Code, getRec.Body.String())
	}
	if !strings.Contains(getRec.Body.String(), `"side":"bid"`) {
		t.Fatalf("expected bid row in response, got %s", getRec.Body.String())
	}
}

func TestHandleOrderBookRejectsUnknownMode(t *testing.T) {
	svc := newTestService(t)

	req := httptest.NewRequest(http.MethodGet, "/api/orderbook?mode=bad", nil)
	rec := httptest.NewRecorder()
	svc.handleOrderBook(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "unknown mode") {
		t.Fatalf("unexpected body: %q", rec.Body.String())
	}
}
