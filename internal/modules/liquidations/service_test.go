package liquidations

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	liqmap "multipleexchangeliquidationmap"
)

type stubServices struct {
	rows []liqmap.EventRow
}

func (s stubServices) ListLiquidations(limit, offset int, startTS, endTS int64) []liqmap.EventRow {
	if offset >= len(s.rows) {
		return nil
	}
	end := offset + limit
	if end > len(s.rows) {
		end = len(s.rows)
	}
	out := make([]liqmap.EventRow, 0, end-offset)
	for _, row := range s.rows[offset:end] {
		if startTS > 0 && row.EventTS < startTS {
			continue
		}
		if endTS > 0 && row.EventTS > endTS {
			continue
		}
		out = append(out, row)
	}
	return out
}

func newTestService(rows []liqmap.EventRow) *service {
	return newService(stubServices{rows: rows})
}

func TestHandleLiquidationsRejectsWrongMethod(t *testing.T) {
	svc := newTestService(nil)

	req := httptest.NewRequest(http.MethodPost, "/api/liquidations", nil)
	rec := httptest.NewRecorder()
	svc.handleLiquidations(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", rec.Code)
	}
}

func TestHandleLiquidationsAppliesMinQtyFilter(t *testing.T) {
	svc := newTestService([]liqmap.EventRow{
		{Side: "sell", Qty: 0.5, EventTS: 1710000000000},
		{Side: "buy", Qty: 1.5, EventTS: 1710000001000},
	})

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
