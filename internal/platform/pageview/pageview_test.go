package pageview

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	sharedtypes "multipleexchangeliquidationmap/internal/shared/types"
)

func TestServeNotFoundOnExactPathMismatch(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/other", nil)
	rec := httptest.NewRecorder()

	Serve(rec, req, sharedtypes.HTMLPage{TemplateName: "x", FallbackHTML: "ok"}, nil, Options{
		ExactPath: "/",
	})

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}

func TestServeRedirectsWhenDefaultQueryMissing(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/monitor", nil)
	rec := httptest.NewRecorder()

	Serve(rec, req, sharedtypes.HTMLPage{TemplateName: "x", FallbackHTML: "ok"}, nil, Options{
		DefaultQuery: map[string]string{"days": "30"},
	})

	if rec.Code != http.StatusFound {
		t.Fatalf("expected redirect, got %d", rec.Code)
	}
	if got := rec.Header().Get("Location"); got != "/monitor?days=30" {
		t.Fatalf("unexpected redirect location %q", got)
	}
}

func TestServeAppliesNoStoreAndRendersFallback(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/config", nil)
	rec := httptest.NewRecorder()

	Serve(rec, req, sharedtypes.HTMLPage{
		TemplateName: "config",
		FallbackHTML: "<html>ok</html>",
	}, nil, Options{NoStore: true})

	if got := rec.Header().Get("Cache-Control"); !strings.Contains(got, "no-store") {
		t.Fatalf("expected no-store header, got %q", got)
	}
	if !strings.Contains(rec.Body.String(), "ok") {
		t.Fatalf("expected fallback html to render, got %q", rec.Body.String())
	}
}
