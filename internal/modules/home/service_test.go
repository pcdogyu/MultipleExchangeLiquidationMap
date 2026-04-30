package home

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	liqmap "multipleexchangeliquidationmap/internal/core"
)

type stubServices struct {
	windowDays int
}

func (s *stubServices) WindowDays() int {
	return s.windowDays
}

func (s *stubServices) SetWindowDays(days int) error {
	switch days {
	case 0, 1, 7, 30:
		s.windowDays = days
		return nil
	default:
		return liqmap.BadRequestError{Message: "invalid days"}
	}
}

func (s *stubServices) Dashboard(days int) (liqmap.Dashboard, error) {
	return liqmap.Dashboard{}, nil
}

func (s *stubServices) ModelLiquidationMap(windowDays, lookbackMin, bucketMin int, priceStep, priceRange float64) (map[string]any, error) {
	return map[string]any{"window_days": windowDays}, nil
}

func (s *stubServices) FetchCoinGlassMap(symbol, window string) ([]byte, error) {
	return nil, liqmap.BadRequestError{Message: "CG_API_KEY is not set"}
}

func newTestService() *service {
	return newService(&stubServices{windowDays: 30})
}

func TestHandleWindowRejectsInvalidDays(t *testing.T) {
	svc := newTestService()

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
	svc := newTestService()

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
