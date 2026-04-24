package bubbles

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	liqmap "multipleexchangeliquidationmap/internal/core"
)

type stubServices struct{}

func (stubServices) FetchKlines(interval string, limit int, startTS, endTS int64) (map[string]any, error) {
	if interval == "bad" {
		return nil, liqmap.BadRequestError{Message: "unsupported interval"}
	}
	return map[string]any{"interval": interval}, nil
}

func (stubServices) LatestOKXClose() (map[string]any, error) {
	return map[string]any{"exchange": "okx"}, nil
}

func newTestService() *service {
	return newService(stubServices{})
}

func TestHandleKlinesRejectsUnsupportedInterval(t *testing.T) {
	svc := newTestService()

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
	svc := newTestService()

	req := httptest.NewRequest(http.MethodPost, "/api/okx/latest-close", nil)
	rec := httptest.NewRecorder()
	svc.handleOKXLatestClose(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", rec.Code)
	}
}
