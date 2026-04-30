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

func TestHTMLPageUsesSharedChrome(t *testing.T) {
	rr := httptest.NewRecorder()
	body := `<!doctype html><html><head><title>x</title></head><body><div class="nav"><div>old nav</div></div><main>content</main><div id="globalFooter" class="footer">old footer</div></body></html>`

	HTMLPage(rr, "analysis_backtest", body, nil)

	got := rr.Body.String()
	if strings.Contains(got, "old nav") || strings.Contains(got, "old footer") {
		t.Fatalf("expected old page chrome to be removed, got %q", got)
	}
	if strings.Count(got, `id="shared-top-nav"`) != 1 {
		t.Fatalf("expected one shared top nav, got %q", got)
	}
	if strings.Count(got, `id="shared-global-footer"`) != 1 {
		t.Fatalf("expected one shared footer, got %q", got)
	}
	if !strings.Contains(got, `<a href="/analysis-backtest" class="active">单因子回测</a>`) {
		t.Fatalf("expected active analysis backtest nav item, got %q", got)
	}
}
