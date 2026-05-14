package liquidations

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	liqmap "multipleexchangeliquidationmap/internal/core"
)

type stubServices struct {
	rows []liqmap.EventRow
}

func (s stubServices) QueryLiquidations(opts liqmap.LiquidationListOptions) []liqmap.EventRow {
	limit := opts.Limit
	if limit <= 0 {
		limit = len(s.rows)
	}
	offset := opts.Offset
	if offset < 0 {
		offset = 0
	}
	if offset >= len(s.rows) {
		return nil
	}
	end := offset + limit
	if end > len(s.rows) {
		end = len(s.rows)
	}
	out := make([]liqmap.EventRow, 0, end-offset)
	for _, row := range s.rows[offset:end] {
		if opts.StartTS > 0 && row.EventTS < opts.StartTS {
			continue
		}
		if opts.EndTS > 0 && row.EventTS > opts.EndTS {
			continue
		}
		switch opts.FilterField {
		case "qty":
			if row.Qty < opts.MinValue {
				continue
			}
		case "notional", "notional_usd", "amount":
			if row.NotionalUSD < opts.MinValue {
				continue
			}
		}
		out = append(out, row)
	}
	return out
}

func (s stubServices) LiquidationPeriodSummary(opts liqmap.LiquidationListOptions) liqmap.LiquidationPeriodSummary {
	return liqmap.LiquidationPeriodSummary{}
}

func (s stubServices) LiquidationSymbols(limit int) []string {
	return []string{"ETHUSDT"}
}

func (s stubServices) LiquidationWSStatuses() []liqmap.LiquidationWSStatus {
	return nil
}

func (s stubServices) RetryLiquidationExchange(exchange string) error {
	return nil
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
