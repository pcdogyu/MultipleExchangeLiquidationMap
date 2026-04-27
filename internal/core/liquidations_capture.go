package liqmap

import "fmt"

func (a *App) captureLiquidationsStructureScreenshotJPEG() ([]byte, error) {
	pageURL := fmt.Sprintf("http://127.0.0.1%s/liquidations", defaultServerAddr)
	prepare := `(async()=>{ if(typeof setTheme==='function') setTheme('light'); window.scrollTo(0,0); return true; })()`
	wait := `(function(){
		const wrap=document.getElementById('liquidationStructureCapture');
		const grid=document.getElementById('periodGrid');
		const pattern=document.getElementById('patternBox');
		if(!wrap || !grid || !pattern) return false;
		const cards=grid.querySelectorAll('.period-card');
		const title=(pattern.textContent||'').trim();
		if(cards.length < 4) return false;
		if(!title || title.includes('加载中') || title.includes('暂无数据')) return false;
		return true;
	})()`
	return captureElementJPEGWithScale(pageURL, "#liquidationStructureCapture", 1680, 980, 1.6, prepare, wait)
}
