package liqmap

import "fmt"

func (a *App) captureBubblesScreenshotJPEG() ([]byte, error) {
	pageURL := fmt.Sprintf("http://127.0.0.1%s/bubbles", defaultServerAddr)
	prepare := `(async()=>{
		if(typeof setTheme==='function') setTheme('dark');
		const iv=document.getElementById('iv');
		if(iv) iv.value='5m';
		const hist=document.getElementById('hist');
		if(hist) hist.checked=true;
		if(typeof setMoreBtnVisible==='function') setMoreBtnVisible();
		if(typeof load==='function') await load();
		const targetCount=288;
		if(Array.isArray(candles) && candles.length){
			viewCount=Math.min(targetCount, candles.length);
			viewStart=Math.max(0, candles.length-viewCount);
		}
		if(typeof draw==='function') draw();
		return true;
	})()`
	wait := `(function(){
		const wrap=document.querySelector('.chart-wrap');
		const meta=document.getElementById('meta');
		const cv=document.getElementById('cv');
		const iv=document.getElementById('iv');
		if(!wrap || !meta || !cv || !iv) return false;
		if(iv.value!=='5m') return false;
		const metaText=(meta.textContent||'').trim();
		if(!metaText || metaText.includes('加载') || metaText.includes('失败')) return false;
		if(!Array.isArray(candles) || candles.length < 50) return false;
		return cv.width > 0 && cv.height > 0;
	})()`
	return captureElementJPEGWithScale(pageURL, ".panel", 1820, 1040, 1.6, prepare, wait)
}
