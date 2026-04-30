package render

import (
	"net/http/httptest"
	"strings"
	"testing"

	sharedtypes "multipleexchangeliquidationmap/internal/shared/types"
)

func TestHTMLPageRendersTemplateData(t *testing.T) {
	rr := httptest.NewRecorder()
	HTMLPage(rr, "demo", "<h1>{{.Title}}</h1>", map[string]string{"Title": "Hello"})

	if got := rr.Body.String(); !strings.Contains(got, "<h1>Hello</h1>") {
		t.Fatalf("expected rendered template, got %q", got)
	}
}

func TestPreferredFileOrFallbackUsesFallbackWhenPreferredMissing(t *testing.T) {
	rr := httptest.NewRecorder()
	page := sharedtypes.HTMLPage{
		TemplateName: "demo",
		FallbackHTML: "<p>fallback</p>",
		Preferred:    []string{"definitely-missing-file.html"},
	}

	PreferredFileOrFallback(rr, page, nil)

	if got := rr.Body.String(); got != "<p>fallback</p>" {
		t.Fatalf("expected fallback html, got %q", got)
	}
}
