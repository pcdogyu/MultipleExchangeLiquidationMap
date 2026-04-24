package monitor

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHandlePageRedirectsToDefaultDays(t *testing.T) {
	svc := newService(nil)

	req := httptest.NewRequest(http.MethodGet, "/monitor", nil)
	rec := httptest.NewRecorder()
	svc.handlePage(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("expected 302, got %d", rec.Code)
	}
	if got := rec.Header().Get("Location"); got != "/monitor?days=30" {
		t.Fatalf("expected redirect to /monitor?days=30, got %q", got)
	}
}
