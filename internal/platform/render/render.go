package render

import (
	"bytes"
	"html/template"
	"io"
	"net/http"
	"os"
	"strings"
	texttemplate "text/template"

	sharedtypes "multipleexchangeliquidationmap/internal/shared/types"
)

const sharedTopNavSnippet = `<style id="shared-top-nav-style">
#shared-top-nav.nav{height:58px;background:var(--nav, #101827);border-bottom:1px solid rgba(255,255,255,.08);display:flex;align-items:center;justify-content:space-between;padding:0 20px;position:sticky;top:0;z-index:200}
#shared-top-nav .nav-left,#shared-top-nav .nav-right{display:flex;align-items:center;gap:20px}
#shared-top-nav .brand{font-size:18px;font-weight:700;color:var(--navInk, #f6f7fb)}
#shared-top-nav .menu a{color:#cdd5e4;text-decoration:none;font-size:15px;margin-right:16px}
#shared-top-nav .menu a.active{color:#fff;font-weight:700}
#shared-top-nav .nav-right a{color:#fff;text-decoration:none;font-size:14px;padding:8px 12px;border-radius:999px;border:1px solid rgba(255,255,255,.18)}
#shared-top-nav .theme-toggle{display:inline-flex;align-items:center;gap:6px;font-size:13px}
#shared-top-nav .theme-toggle button{height:30px;padding:0 10px;border-radius:999px;border:1px solid rgba(148,163,184,.45);background:transparent;color:var(--navInk, #f6f7fb);cursor:pointer}
#shared-top-nav .theme-toggle button.label{cursor:default;opacity:.92}
#shared-top-nav .theme-toggle button.active{background:rgba(255,255,255,.12);border-color:rgba(255,255,255,.18);color:#fff}
@media (max-width:980px){#shared-top-nav .menu{display:none}}
</style>
<script id="shared-top-nav-script">
(function(){
  if(typeof document==='undefined') return;
  const existing=document.querySelector('.nav');
  if(!existing) return;
  const path=(window.location&&window.location.pathname)||'/';
  const items=[
    ['/', '清算热区'],
    ['/config', '模型配置'],
    ['/monitor', '雷区监控'],
    ['/map', '盘口汇总'],
    ['/liquidations', '强平清算'],
    ['/bubbles', '气泡图'],
    ['/webdatasource', '页面数据源'],
    ['/channel', '消息通道'],
    ['/analysis', '日内分析'],
    ['/analysis-backtest', '单因子回测'],
    ['/analysis-backtest-2fa', '双因子回测'],
    ['/analysis-backtest-liquidation', '多多空空']
  ];
  const menuHTML=items.map(function(item){
    const href=item[0];
    const text=item[1];
    const active=path===href?' class="active"':'';
    return '<a href="'+href+'"'+active+'>'+text+'</a>';
  }).join('');
  existing.id='shared-top-nav';
  existing.innerHTML=
    '<div class="nav-left">'+
      '<div class="brand">ETH Liquidation Map</div>'+
      '<div class="menu">'+menuHTML+'</div>'+
    '</div>'+
    '<div class="nav-right">'+
      '<div class="theme-toggle">'+
        '<button class="label" type="button">主题</button>'+
        '<button id="themeDark" type="button">深色</button>'+
        '<button id="themeLight" type="button">浅色</button>'+
      '</div>'+
      '<a href="/config">升级</a>'+
    '</div>';
  const darkBtn=document.getElementById('themeDark');
  const lightBtn=document.getElementById('themeLight');
  if(darkBtn) darkBtn.onclick=function(){ if(typeof window.setTheme==='function') window.setTheme('dark'); };
  if(lightBtn) lightBtn.onclick=function(){ if(typeof window.setTheme==='function') window.setTheme('light'); };
})();
</script>`

const sharedFooterSnippet = `<style id="shared-global-footer-style">
#shared-global-footer.footer{margin:18px auto 0 auto;max-width:1200px;padding:10px 12px;font-size:12px;color:var(--muted, #64748b);text-align:center}
</style>
<script id="shared-global-footer-script">
(async function(){
  if(typeof document==='undefined') return;
  let footer=document.querySelector('#globalFooter');
  if(!footer) footer=document.querySelector('.footer');
  if(!footer){
    footer=document.createElement('div');
    footer.className='footer';
    document.body.appendChild(footer);
  }
  footer.id='shared-global-footer';
  try{
    const r=await fetch('/api/version');
    const v=await r.json();
    footer.textContent='Code by Yuhao@jiansutech.com - '+(v.commit_time||'-')+' - '+(v.commit_id||'-')+' - '+(v.branch||'-');
  }catch(_){
    footer.textContent='Code by Yuhao@jiansutech.com - - - -';
  }
})();
</script>`

func withSharedTopNav(body string) string {
	if body == "" || !strings.Contains(body, "</body>") {
		return body
	}
	if strings.Contains(body, `id="shared-top-nav-script"`) {
		return body
	}
	return strings.Replace(body, "</body>", sharedTopNavSnippet+"</body>", 1)
}

func withSharedFooter(body string) string {
	if body == "" || !strings.Contains(body, "</body>") {
		return body
	}
	if strings.Contains(body, `id="shared-global-footer-script"`) {
		return body
	}
	return strings.Replace(body, "</body>", sharedFooterSnippet+"</body>", 1)
}

func HTMLPage(w http.ResponseWriter, name, body string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	body = withSharedTopNav(body)
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
