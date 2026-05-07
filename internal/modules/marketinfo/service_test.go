package marketinfo

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	liqmap "multipleexchangeliquidationmap/internal/core"
)

type stubMarketInfoServices struct {
	symbol string
	period string
	limit  int
}

func (s *stubMarketInfoServices) BinanceMarketInfo(symbol, period string, limit int) (liqmap.MarketInfoResponse, error) {
	s.symbol = symbol
	s.period = period
	s.limit = limit
	return liqmap.MarketInfoResponse{Exchange: "binance", Symbol: "ETHUSDT", Period: period, Limit: limit}, nil
}

func TestHandleMarketInfoUsesDefaults(t *testing.T) {
	stub := &stubMarketInfoServices{}
	svc := newService(stub)

	req := httptest.NewRequest(http.MethodGet, "/api/market-info", nil)
	rec := httptest.NewRecorder()
	svc.handleMarketInfo(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%q", rec.Code, rec.Body.String())
	}
	if stub.period != "5m" || stub.limit != 288 {
		t.Fatalf("unexpected defaults period=%q limit=%d", stub.period, stub.limit)
	}
	var resp liqmap.MarketInfoResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Period != "5m" || resp.Limit != 288 {
		t.Fatalf("unexpected response period=%q limit=%d", resp.Period, resp.Limit)
	}
}

func TestHandleMarketInfoRejectsInvalidPeriod(t *testing.T) {
	svc := newService(&stubMarketInfoServices{})

	req := httptest.NewRequest(http.MethodGet, "/api/market-info?period=3m", nil)
	rec := httptest.NewRecorder()
	svc.handleMarketInfo(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "invalid period") {
		t.Fatalf("unexpected body: %q", rec.Body.String())
	}
}
