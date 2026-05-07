package render

import (
	"bytes"
	"html/template"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	texttemplate "text/template"

	sharedtypes "multipleexchangeliquidationmap/internal/shared/types"
)

type navItem struct {
	Href  string
	Label string
}

var sharedNavItems = []navItem{
	{Href: "/", Label: "清算热区"},
	{Href: "/config", Label: "模型配置"},
	{Href: "/monitor", Label: "雷区监控"},
	{Href: "/map", Label: "盘口汇总"},
	{Href: "/liquidations", Label: "强平清算"},
	{Href: "/bubbles", Label: "气泡图"},
	{Href: "/market-info", Label: "市场信息"},
	{Href: "/webdatasource", Label: "页面数据源"},
	{Href: "/channel", Label: "消息通道"},
	{Href: "/analysis", Label: "日内分析"},
	{Href: "/analysis-backtest", Label: "单因子回测"},
	{Href: "/analysis-backtest-2fa", Label: "双因子回测"},
	{Href: "/analysis-backtest-liquidation", Label: "多多空空"},
}

var templateActivePath = map[string]string{
	"index":                         "/",
	"model_config_page":             "/config",
	"monitor":                       "/monitor",
	"map":                           "/map",
	"liquidations":                  "/liquidations",
	"bubbles":                       "/bubbles",
	"market_info":                   "/market-info",
	"webdatasource":                 "/webdatasource",
	"channel":                       "/channel",
	"analysis":                      "/analysis",
	"analysis_backtest":             "/analysis-backtest",
	"analysis_backtest_2fa":         "/analysis-backtest-2fa",
	"analysis_backtest_liquidation": "/analysis-backtest-liquidation",
}

var legacyGlobalFooterVersionLoaders = []*regexp.Regexp{
	regexp.MustCompile(`(?s)\(async\(\)=>\{try\{const r=await fetch\('/api/version'\);.*?document\.getElementById\('globalFooter'\).*?\}\)\(\);`),
	regexp.MustCompile(`(?s)\(async function\(\)\{\s*try\{\s*const r=await fetch\('/api/version'\);.*?document\.getElementById\('globalFooter'\).*?\}\)\(\);`),
	regexp.MustCompile(`(?m)^\s*async function loadFooter\(\)\{[^\n]*globalFooter[^\n]*\}\s*$\n?`),
}

const sharedTopNavStyle = `<style id="shared-top-nav-style">
#shared-top-nav.nav{height:58px;background:var(--nav, #101827);border-bottom:1px solid rgba(255,255,255,.08);display:flex;align-items:center;justify-content:space-between;padding:0 20px;position:sticky;top:0;z-index:200}
#shared-top-nav .nav-left,#shared-top-nav .nav-right{display:flex;align-items:center;gap:20px}
#shared-top-nav .brand{font-size:18px;font-weight:700;color:var(--navInk, #f6f7fb)}
#shared-top-nav .menu{overflow-x:auto;white-space:nowrap;max-width:min(74vw,1280px);scrollbar-width:none}
#shared-top-nav .menu::-webkit-scrollbar{display:none}
#shared-top-nav .menu a{color:#cdd5e4;text-decoration:none;font-size:15px;margin-right:16px}
#shared-top-nav .menu a.active{color:#fff;font-weight:700}
#shared-top-nav .nav-right a{color:#fff;text-decoration:none;font-size:14px;padding:8px 12px;border-radius:999px;border:1px solid rgba(255,255,255,.18)}
#shared-top-nav .theme-toggle{display:inline-flex;align-items:center;gap:6px;font-size:13px}
#shared-top-nav .theme-toggle button{height:30px;padding:0 10px;border-radius:999px;border:1px solid rgba(148,163,184,.45);background:transparent;color:var(--navInk, #f6f7fb);cursor:pointer}
#shared-top-nav .theme-toggle button.label{cursor:default;opacity:.92}
#shared-top-nav .theme-toggle button.active{background:rgba(255,255,255,.12);border-color:rgba(255,255,255,.18);color:#fff}
@media (max-width:980px){#shared-top-nav .menu{display:none}}
</style>`

const sharedGlobalFooterStyle = `<style id="shared-global-footer-style">
#shared-global-footer.footer{margin:18px auto 0 auto;max-width:1200px;padding:10px 12px;font-size:12px;color:var(--muted, #64748b);text-align:center}
</style>`

const sharedGlobalFooterHTML = `<div id="shared-global-footer" class="footer">Code by Yuhao@jiansutech.com - loading - loading - loading</div>`

const sharedGlobalFooterScript = `<script id="shared-global-footer-script">
(async function(){
  if(typeof document==='undefined') return;
  const footer=document.getElementById('shared-global-footer');
  if(!footer) return;
  try{
    const r=await fetch('/api/version');
    const v=await r.json();
    footer.textContent='Code by Yuhao@jiansutech.com - '+(v.commit_time||'-')+' - '+(v.commit_id||'-')+' - '+(v.branch||'-');
  }catch(_){
    footer.textContent='Code by Yuhao@jiansutech.com - - - -';
  }
})();
</script>`

func sharedGlobalFooter() string {
	return sharedGlobalFooterStyle + "\n" + sharedGlobalFooterHTML + "\n" + sharedGlobalFooterScript
}

func sharedTopNav(templateName string) string {
	activePath := templateActivePath[templateName]
	var menu strings.Builder
	for _, item := range sharedNavItems {
		active := ""
		if item.Href == activePath {
			active = ` class="active"`
		}
		menu.WriteString(`<a href="`)
		menu.WriteString(template.HTMLEscapeString(item.Href))
		menu.WriteString(`"`)
		menu.WriteString(active)
		menu.WriteString(`>`)
		menu.WriteString(template.HTMLEscapeString(item.Label))
		menu.WriteString(`</a>`)
	}
	return sharedTopNavStyle + `<div id="shared-top-nav" class="nav"><div class="nav-left"><div class="brand">ETH Liquidation Map</div><div class="menu">` +
		menu.String() +
		`</div></div><div class="nav-right"><div class="theme-toggle"><button class="label" type="button">主题</button><button id="themeDark" type="button" onclick="if(window.setTheme)window.setTheme('dark')">深色</button><button id="themeLight" type="button" onclick="if(window.setTheme)window.setTheme('light')">浅色</button></div><a href="/config" class="upgrade">升级</a></div></div>`
}

func withSharedTopNav(name, body string) string {
	if body == "" || !hasBody(body) {
		return body
	}
	if strings.Contains(body, `id="shared-top-nav"`) {
		return body
	}
	body = removeFirstElement(body, func(tag string) bool {
		return attrHasToken(tag, "class", "nav")
	})
	return insertAfterBodyStart(body, sharedTopNav(name))
}

func withSharedFooter(body string) string {
	if body == "" || !hasBody(body) {
		return body
	}
	if strings.Contains(body, `id="shared-global-footer-script"`) {
		return body
	}
	body = removeLegacyGlobalFooterVersionLoaders(body)
	body = removeFirstElement(body, func(tag string) bool {
		return strings.EqualFold(attrValue(tag, "id"), "globalFooter") || strings.EqualFold(attrValue(tag, "id"), "shared-global-footer")
	})
	return strings.Replace(body, "</body>", sharedGlobalFooter()+"</body>", 1)
}

func removeLegacyGlobalFooterVersionLoaders(body string) string {
	for _, re := range legacyGlobalFooterVersionLoaders {
		body = re.ReplaceAllString(body, "")
	}
	body = strings.ReplaceAll(body, "await loadFooter();", "")
	body = strings.ReplaceAll(body, "loadFooter();", "")
	return body
}

func hasBody(body string) bool {
	lower := strings.ToLower(body)
	return strings.Contains(lower, "<body") && strings.Contains(lower, "</body>")
}

func insertAfterBodyStart(body, fragment string) string {
	lower := strings.ToLower(body)
	idx := strings.Index(lower, "<body")
	if idx < 0 {
		return body
	}
	closeRel := strings.Index(body[idx:], ">")
	if closeRel < 0 {
		return body
	}
	insertAt := idx + closeRel + 1
	return body[:insertAt] + fragment + body[insertAt:]
}

func removeFirstElement(body string, match func(string) bool) string {
	lower := strings.ToLower(body)
	for searchFrom := 0; ; {
		idx := strings.Index(lower[searchFrom:], "<div")
		if idx < 0 {
			return body
		}
		start := searchFrom + idx
		tagEndRel := strings.Index(body[start:], ">")
		if tagEndRel < 0 {
			return body
		}
		tagEnd := start + tagEndRel + 1
		tag := body[start:tagEnd]
		if !match(tag) {
			searchFrom = tagEnd
			continue
		}
		end := findDivElementEnd(body, tagEnd)
		if end < 0 {
			return body
		}
		return body[:start] + body[end:]
	}
}

func findDivElementEnd(body string, afterStartTag int) int {
	lower := strings.ToLower(body)
	depth := 1
	pos := afterStartTag
	for depth > 0 {
		nextOpenRel := strings.Index(lower[pos:], "<div")
		nextCloseRel := strings.Index(lower[pos:], "</div>")
		if nextCloseRel < 0 {
			return -1
		}
		if nextOpenRel >= 0 && nextOpenRel < nextCloseRel {
			depth++
			openEndRel := strings.Index(body[pos+nextOpenRel:], ">")
			if openEndRel < 0 {
				return -1
			}
			pos += nextOpenRel + openEndRel + 1
			continue
		}
		depth--
		pos += nextCloseRel + len("</div>")
	}
	return pos
}

var attrPatternCache = map[string][2]*regexp.Regexp{}

func attrValue(tag, name string) string {
	pair, ok := attrPatternCache[name]
	if !ok {
		pair = [2]*regexp.Regexp{
			regexp.MustCompile(`(?i)\b` + regexp.QuoteMeta(name) + `\s*=\s*"([^"]*)"`),
			regexp.MustCompile(`(?i)\b` + regexp.QuoteMeta(name) + `\s*=\s*'([^']*)'`),
		}
		attrPatternCache[name] = pair
	}
	for _, re := range pair {
		m := re.FindStringSubmatch(tag)
		if len(m) >= 2 {
			return m[1]
		}
	}
	return ""
}

func attrHasToken(tag, name, token string) bool {
	for _, item := range strings.Fields(attrValue(tag, name)) {
		if strings.EqualFold(item, token) {
			return true
		}
	}
	return false
}

func HTMLPage(w http.ResponseWriter, name, body string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	body = withSharedTopNav(name, body)
	body = withSharedFooter(body)

	if tpl, parseErr := template.New(name).Parse(body); parseErr == nil {
		var buf bytes.Buffer
		if execErr := tpl.Execute(&buf, data); execErr == nil {
			_, _ = w.Write(buf.Bytes())
			return
		}
	}

	if data == nil {
		_, _ = io.WriteString(w, body)
		return
	}

	tpl, err := texttemplate.New(name).Parse(body)
	if err != nil {
		http.Error(w, "page render failed", http.StatusInternalServerError)
		return
	}
	var buf bytes.Buffer
	if err := tpl.Execute(&buf, data); err != nil {
		http.Error(w, "page render failed", http.StatusInternalServerError)
		return
	}
	_, _ = w.Write(buf.Bytes())
}

func PreferredFileOrFallback(w http.ResponseWriter, page sharedtypes.HTMLPage, data any) {
	for _, filename := range page.Preferred {
		if body, err := os.ReadFile(filename); err == nil {
			HTMLPage(w, page.TemplateName, string(body), data)
			return
		}
	}
	HTMLPage(w, page.TemplateName, page.FallbackHTML, data)
}
