package httpx

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWriteJSONSetsHeadersAndStatus(t *testing.T) {
	rr := httptest.NewRecorder()
	WriteJSON(rr, http.StatusCreated, map[string]string{"ok": "yes"})

	if rr.Code != http.StatusCreated {
		t.Fatalf("expected status %d, got %d", http.StatusCreated, rr.Code)
	}
	if got := rr.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("expected json content-type, got %q", got)
	}
	if !strings.Contains(rr.Body.String(), `"ok":"yes"`) {
		t.Fatalf("expected body to contain encoded json, got %q", rr.Body.String())
	}
}

func TestNoStoreSetsCacheHeaders(t *testing.T) {
	rr := httptest.NewRecorder()
	NoStore(rr)

	if got := rr.Header().Get("Cache-Control"); got != "no-store, max-age=0" {
		t.Fatalf("unexpected Cache-Control header: %q", got)
	}
	if got := rr.Header().Get("Pragma"); got != "no-cache" {
		t.Fatalf("unexpected Pragma header: %q", got)
	}
	if got := rr.Header().Get("Expires"); got != "0" {
		t.Fatalf("unexpected Expires header: %q", got)
	}
}
