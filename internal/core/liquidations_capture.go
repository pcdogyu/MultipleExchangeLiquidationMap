package liqmap

import "fmt"

func (a *App) captureLiquidationsStructureScreenshotJPEG() ([]byte, error) {
	pageURL := fmt.Sprintf("http://127.0.0.1%s/liquidations", defaultServerAddr)
	prepare := `(async()=>{ if(typeof setTheme==='function') setTheme('light'); window.scrollTo(0,0); return true; })()`
	wait := `(function(){
		const grid=document.getElementById('periodGrid');
		if(!grid) return false;
		const cards=grid.querySelectorAll('.period-card');
		if(cards.length < 4) return false;
		const pending=[...cards].some(card=>{
			const text=(card.textContent||'').trim();
			const total=card.querySelector('.period-total');
			const hasBar=!!card.querySelector('.period-bar');
			return !text || !hasBar || !total || (total.textContent||'').trim()==='--';
		});
		return !pending;
	})()`
	return captureElementJPEGWithScale(pageURL, "#periodGrid", 1920, 520, 1.6, prepare, wait)
}
