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
	body := `<!doctype html><html><head><title>x</title></head><body><div class="nav"><div>old nav</div></div><main>content</main><div id="pageStatus" class="footer page-status">page loading</div><script>(async()=>{try{const r=await fetch('/api/version');const v=await r.json();const el=document.getElementById('globalFooter');if(el)el.textContent='Code by old - '+v.branch;}catch(_){const el=document.getElementById('globalFooter');if(el)el.textContent='old';}})();</script><div id="globalFooter" class="footer">old footer</div></body></html>`

	HTMLPage(rr, "analysis_backtest", body, nil)

	got := rr.Body.String()
	if strings.Contains(got, "old nav") || strings.Contains(got, "old footer") {
		t.Fatalf("expected old page chrome to be removed, got %q", got)
	}
	if !strings.Contains(got, `id="pageStatus"`) || !strings.Contains(got, "page loading") {
		t.Fatalf("expected page status footer to stay separate, got %q", got)
	}
	if strings.Count(got, `id="shared-top-nav"`) != 1 {
		t.Fatalf("expected one shared top nav, got %q", got)
	}
	if strings.Count(got, `id="shared-global-footer"`) != 1 {
		t.Fatalf("expected one shared footer, got %q", got)
	}
	if strings.Count(got, `/api/version`) != 1 {
		t.Fatalf("expected only the shared footer version loader, got %q", got)
	}
	if !strings.Contains(got, `<a href="/analysis-backtest" class="active">单因子回测</a>`) {
		t.Fatalf("expected active analysis backtest nav item, got %q", got)
	}
	if strings.Contains(got, `href="/config" class="upgrade"`) {
		t.Fatalf("expected upgrade control to avoid config navigation, got %q", got)
	}
	if !strings.Contains(got, `onclick="return sharedDoUpgrade(event)"`) || !strings.Contains(got, `/api/upgrade/pull`) {
		t.Fatalf("expected shared upgrade control to trigger upgrade API, got %q", got)
	}
	if strings.Count(got, `id="sharedUpgradeModal"`) != 1 {
		t.Fatalf("expected one shared upgrade modal, got %q", got)
	}
	if !strings.Contains(got, `onclick="return sharedDoOpenLogs(event)"`) || !strings.Contains(got, `/api/logs`) {
		t.Fatalf("expected shared log control to load runtime logs, got %q", got)
	}
	if strings.Count(got, `id="sharedLogModal"`) != 1 {
		t.Fatalf("expected one shared log modal, got %q", got)
	}
}

func TestHTMLPageRemovesLegacyLoadFooterWithoutBreakingPageLoad(t *testing.T) {
	rr := httptest.NewRecorder()
	body := `<!doctype html><html><head><title>x</title></head><body><main>content</main><script>
async function loadFooter(){try{const r=await fetch('/api/version');const v=await r.json();const el=document.getElementById('globalFooter');if(el)el.textContent='Code by old - '+v.branch;}catch(_){const el=document.getElementById('globalFooter');if(el)el.textContent='old';}}
async function loadHistory(){window.loadedHistory=true;}
window.addEventListener('load',async()=>{await loadFooter();await loadHistory();});
</script><div id="globalFooter" class="footer">old footer</div></body></html>`

	HTMLPage(rr, "channel", body, nil)

	got := rr.Body.String()
	if strings.Contains(got, "loadFooter") || strings.Contains(got, "old footer") {
		t.Fatalf("expected legacy footer loader to be removed, got %q", got)
	}
	if !strings.Contains(got, "await loadHistory();") {
		t.Fatalf("expected page load work to remain, got %q", got)
	}
	if strings.Count(got, `/api/version`) != 1 {
		t.Fatalf("expected only one shared version loader, got %q", got)
	}
}

func TestHTMLPageInsertsSharedChromeWithoutOldChrome(t *testing.T) {
	rr := httptest.NewRecorder()
	body := `<!doctype html><html><head><title>x</title></head><body><main>content</main><div id="pageStatus" class="page-status">ready</div></body></html>`

	HTMLPage(rr, "monitor", body, nil)

	got := rr.Body.String()
	if strings.Count(got, `id="shared-top-nav"`) != 1 {
		t.Fatalf("expected one shared top nav, got %q", got)
	}
	if strings.Count(got, `id="shared-global-footer"`) != 1 {
		t.Fatalf("expected one shared footer, got %q", got)
	}
	if !strings.Contains(got, `<a href="/monitor" class="active">雷区监控</a>`) {
		t.Fatalf("expected active monitor nav item, got %q", got)
	}
	if !strings.Contains(got, `id="pageStatus"`) {
		t.Fatalf("expected page status to be preserved, got %q", got)
	}
}

func TestSharedTopNavHidesHomeAndConfigMenuItems(t *testing.T) {
	rr := httptest.NewRecorder()
	body := `<!doctype html><html><head><title>x</title></head><body><main>content</main></body></html>`

	HTMLPage(rr, "webdatasource", body, nil)

	got := rr.Body.String()
	if strings.Contains(got, `<a href="/">清算热区</a>`) {
		t.Fatalf("expected heat map nav item to be hidden, got %q", got)
	}
	if strings.Contains(got, `<a href="/config">模型配置</a>`) {
		t.Fatalf("expected model config nav item to be hidden, got %q", got)
	}
	if !strings.Contains(got, `<a href="/webdatasource" class="active">页面数据源</a>`) {
		t.Fatalf("expected webdatasource nav item to remain active, got %q", got)
	}
}

func TestSharedNavActivePathForEveryKnownTemplate(t *testing.T) {
	for templateName, activePath := range templateActivePath {
		rr := httptest.NewRecorder()
		body := `<!doctype html><html><head><title>x</title></head><body><main>content</main></body></html>`

		HTMLPage(rr, templateName, body, nil)

		got := rr.Body.String()
		want := `<a href="` + activePath + `" class="active">`
		if !strings.Contains(got, want) {
			t.Fatalf("template %s expected active path %s, got %q", templateName, activePath, got)
		}
	}
}
