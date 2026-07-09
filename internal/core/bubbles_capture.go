package liqmap

func (a *App) captureBubblesScreenshotJPEG() ([]byte, error) {
	pageURL := capturePageURL("/bubbles")
	prepare := `(async()=>{
		if(typeof setTheme==='function') setTheme('dark');
		const iv=document.getElementById('iv');
		if(iv) iv.value='5m';
		const qty=document.getElementById('qtyFilter');
		if(qty) qty.value='5';
		if(typeof qtyFilter!=='undefined') qtyFilter=5;
		if(typeof filterApplied!=='undefined') filterApplied=true;
		const hist=document.getElementById('hist');
		if(hist) hist.checked=true;
		const tradeSig=document.getElementById('tradeSigToggle');
		if(tradeSig) tradeSig.checked=false;
		if(typeof setMoreBtnVisible==='function') setMoreBtnVisible();
		if(typeof syncFilterBtn==='function') syncFilterBtn();
		if(typeof load==='function') await load();
		const targetCount=288;
		if(Array.isArray(candles) && candles.length){
			viewCount=Math.min(targetCount, candles.length);
			viewStart=Math.max(0, candles.length-viewCount);
		}
		if(Array.isArray(tradeSignals)) tradeSignals=[];
		if(typeof updateMeta==='function') updateMeta();
		if(typeof draw==='function') draw();
		return true;
	})()`
	wait := `(function(){
		const wrap=document.querySelector('.chart-wrap');
		const meta=document.getElementById('meta');
		const cv=document.getElementById('cv');
		const iv=document.getElementById('iv');
		const qty=document.getElementById('qtyFilter');
		const tradeSig=document.getElementById('tradeSigToggle');
		if(!wrap || !meta || !cv || !iv || !qty || !tradeSig) return false;
		if(iv.value!=='5m') return false;
		if(Number(qty.value)!==5 || Number(qtyFilter)!==5) return false;
		if(tradeSig.checked) return false;
		const metaText=(meta.textContent||'').trim();
		if(!metaText || metaText.includes('加载') || metaText.includes('失败')) return false;
		if(!metaText.includes('已过滤小于 5 ETH')) return false;
		if(!Array.isArray(candles) || candles.length < 50) return false;
		return cv.width > 0 && cv.height > 0;
	})()`
	return captureElementJPEGWithScale(pageURL, ".panel", 1820, 1040, 1.6, prepare, wait)
}
