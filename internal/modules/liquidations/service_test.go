package liquidations

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	liqmap "multipleexchangeliquidationmap"
	"multipleexchangeliquidationmap/internal/appctx"
	dbplatform "multipleexchangeliquidationmap/internal/platform/db"

	_ "modernc.org/sqlite"
)

func newTestService(t *testing.T) (*service, *sql.DB) {
	t.Helper()

	dbPath := filepath.Join(t.TempDir(), "liquidations-service.db")
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
	}), db
}

func TestHandleLiquidationsRejectsWrongMethod(t *testing.T) {
	svc, _ := newTestService(t)

	req := httptest.NewRequest(http.MethodPost, "/api/liquidations", nil)
	rec := httptest.NewRecorder()
	svc.handleLiquidations(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", rec.Code)
	}
}

func TestHandleLiquidationsAppliesMinQtyFilter(t *testing.T) {
	svc, db := newTestService(t)

	rows := []struct {
		side string
		qty  float64
		ts   int64
	}{
		{side: "sell", qty: 0.5, ts: 1710000000000},
		{side: "buy", qty: 1.5, ts: 1710000001000},
	}
	for _, row := range rows {
		if _, err := db.Exec(`INSERT INTO liquidation_events(exchange, symbol, side, raw_side, qty, price, mark_price, notional_usd, event_ts, inserted_ts)
			VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			"binance", liqmap.DefaultSymbol, row.side, row.side, row.qty, 3000.0, 3000.0, row.qty*3000.0, row.ts, row.ts); err != nil {
			t.Fatalf("insert liquidation event: %v", err)
		}
	}

	req := httptest.NewRequest(http.MethodGet, "/api/liquidations?limit=10&min_qty=1", nil)
	rec := httptest.NewRecorder()
	svc.handleLiquidations(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var resp struct {
		Page     int               `json:"page"`
		PageSize int               `json:"page_size"`
		Rows     []liqmap.EventRow `json:"rows"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Page != 1 {
		t.Fatalf("expected page 1, got %d", resp.Page)
	}
	if resp.PageSize != 10 {
		t.Fatalf("expected page size 10, got %d", resp.PageSize)
	}
	if len(resp.Rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(resp.Rows))
	}
	if resp.Rows[0].Qty != 1.5 {
		t.Fatalf("expected filtered qty 1.5, got %v", resp.Rows[0].Qty)
	}
}
