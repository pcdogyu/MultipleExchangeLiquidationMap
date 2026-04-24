package bookmap

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	liqmap "multipleexchangeliquidationmap"
)

type stubServices struct {
	events []liqmap.PriceWallEvent
}

func (s *stubServices) OrderBookView(exchange, mode string, limit int) (any, error) {
	if mode == "bad" {
		return nil, liqmap.BadRequestError{Message: "unknown mode"}
	}
	return map[string]any{"mode": mode, "limit": limit}, nil
}

func (s *stubServices) ListPriceWallEvents(page, limit, minutes int, side, mode string) (any, error) {
	rows := make([]map[string]any, 0, len(s.events))
	for _, event := range s.events {
		if side != "" && event.Side != side {
			continue
		}
		rows = append(rows, map[string]any{
			"side": event.Side,
			"mode": event.Mode,
		})
	}
	return map[string]any{"rows": rows}, nil
}

func (s *stubServices) RecordPriceWallEvent(req liqmap.PriceWallEvent) error {
	s.events = append(s.events, req)
	return nil
}

func newTestService() *service {
	return newService(&stubServices{})
}

func TestHandlePriceEventsPersistsAndListsRows(t *testing.T) {
	svc := newTestService()

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
	svc := newTestService()

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
