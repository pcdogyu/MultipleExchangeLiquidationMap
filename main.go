package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

const (
	defaultDBPath     = "liqmap.db"
	defaultSymbol     = "ETHUSDT"
	defaultServerAddr  = ":8888"
	defaultWindowDays = 1
)

var bandSizes = []int{10, 20, 30, 40, 50, 60, 80, 100, 125, 150, 175, 200, 250, 300, 350, 400}

type MarketState struct {
	Exchange    string   `json:"exchange"`
	Symbol      string   `json:"symbol"`
	MarkPrice   float64  `json:"mark_price"`
	OIQty       *float64 `json:"oi_qty,omitempty"`
	OIValueUSD  *float64 `json:"oi_value_usd,omitempty"`
	FundingRate *float64 `json:"funding_rate,omitempty"`
	UpdatedTS   int64    `json:"updated_ts"`
}

type BandRow struct {
	Band            int     `json:"band"`
	UpPrice         float64 `json:"up_price"`
	UpNotionalUSD   float64 `json:"up_notional_usd"`
	DownPrice       float64 `json:"down_price"`
	DownNotionalUSD float64 `json:"down_notional_usd"`
}

type Dashboard struct {
	Symbol        string        `json:"symbol"`
	WindowDays    int           `json:"window_days"`
	GeneratedAt   int64         `json:"generated_at"`
	States        []MarketState `json:"states"`
	CurrentPrice  float64       `json:"current_price"`
	Bands         []BandRow     `json:"bands"`
	LongestShort  []any         `json:"longest_short"`
	LongestLong   []any         `json:"longest_long"`
	Events        []EventRow    `json:"events"`
}

type EventRow struct {
	Exchange    string  `json:"exchange"`
	Side        string  `json:"side"`
	Price       float64 `json:"price"`
	Qty         float64 `json:"qty"`
	NotionalUSD float64 `json:"notional_usd"`
	EventTS     int64   `json:"event_ts"`
}

type App struct {
	db         *sql.DB
	mu         sync.RWMutex
	windowDays int
	debug      bool
}

type ChannelSettings struct {
	TelegramBotToken string `json:"telegram_bot_token"`
	TelegramChannel  string `json:"telegram_channel"`
	NotifyIntervalMin int   `json:"notify_interval_min"`
}

func getenv(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

func initDB(db *sql.DB) error {
	stmts := []string{
		`PRAGMA journal_mode=WAL;`,
		`CREATE TABLE IF NOT EXISTS market_state (
			exchange TEXT NOT NULL,
			symbol TEXT NOT NULL,
			mark_price REAL,
			oi_qty REAL,
			oi_value_usd REAL,
			funding_rate REAL,
			updated_ts INTEGER NOT NULL,
			PRIMARY KEY(exchange, symbol)
		);`,
		`CREATE TABLE IF NOT EXISTS liquidation_events (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			exchange TEXT NOT NULL,
			symbol TEXT NOT NULL,
			side TEXT NOT NULL,
			raw_side TEXT,
			qty REAL NOT NULL,
			price REAL NOT NULL,
			mark_price REAL NOT NULL,
			notional_usd REAL NOT NULL,
			event_ts INTEGER NOT NULL,
			inserted_ts INTEGER NOT NULL
		);`,
		`CREATE TABLE IF NOT EXISTS band_reports (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			report_ts INTEGER NOT NULL,
			symbol TEXT NOT NULL,
			current_price REAL NOT NULL,
			band INTEGER NOT NULL,
			up_price REAL NOT NULL,
			up_notional_usd REAL NOT NULL,
			down_price REAL NOT NULL,
			down_notional_usd REAL NOT NULL
		);`,
		`CREATE TABLE IF NOT EXISTS longest_bar_reports (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			report_ts INTEGER NOT NULL,
			symbol TEXT NOT NULL,
			side TEXT NOT NULL,
			bucket_size REAL NOT NULL,
			bucket_price REAL NOT NULL,
			bucket_notional_usd REAL NOT NULL
		);`,
		`CREATE TABLE IF NOT EXISTS app_settings (
			key TEXT PRIMARY KEY,
			value TEXT NOT NULL
		);`,
	}
	for _, stmt := range stmts {
		if _, err := db.Exec(stmt); err != nil {
			return err
		}
	}
	return nil
}

func (a *App) window() int {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.windowDays
}

func (a *App) setWindow(days int) {
	a.mu.Lock()
	a.windowDays = days
	a.mu.Unlock()
}

func main() {
	debug := getenv("DEBUG", "") == "1" || strings.EqualFold(getenv("DEBUG", ""), "true")
	dbPath := getenv("DB_PATH", defaultDBPath)
	if debug {
		log.Printf("debug enabled: db_path=%s addr=%s symbol=%s", dbPath, defaultServerAddr, defaultSymbol)
	}

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()
	if err := initDB(db); err != nil {
		log.Fatal(err)
	}

	app := &App{db: db, windowDays: defaultWindowDays, debug: debug}
	mux := http.NewServeMux()
	mux.HandleFunc("/", app.handleIndex)
	mux.HandleFunc("/map", app.handleMap)
	mux.HandleFunc("/channel", app.handleChannel)
	mux.HandleFunc("/api/dashboard", app.handleDashboard)
	mux.HandleFunc("/api/window", app.handleWindow)
	mux.HandleFunc("/api/settings", app.handleSettings)
	mux.HandleFunc("/api/channel/test", app.handleChannelTest)
	mux.HandleFunc("/api/upgrade/pull", app.handleUpgradePull)

	log.Printf("dashboard listening on http://127.0.0.1%s", defaultServerAddr)
	if err := http.ListenAndServe(defaultServerAddr, mux); err != nil {
		log.Fatal(err)
	}
}

func (a *App) handleIndex(w http.ResponseWriter, r *http.Request) {
	if a.debug {
		log.Printf("%s %s", r.Method, r.URL.Path)
	}
	tpl := template.Must(template.New("index").Parse(indexHTML))
	_ = tpl.Execute(w, nil)
}

func (a *App) handleMap(w http.ResponseWriter, r *http.Request) {
	if a.debug {
		log.Printf("%s %s", r.Method, r.URL.Path)
	}
	tpl := template.Must(template.New("map").Parse(mapHTML))
	_ = tpl.Execute(w, nil)
}

func (a *App) handleChannel(w http.ResponseWriter, r *http.Request) {
	if a.debug {
		log.Printf("%s %s", r.Method, r.URL.Path)
	}
	tpl := template.Must(template.New("channel").Parse(channelHTML))
	_ = tpl.Execute(w, a.loadSettings())
}

func (a *App) handleWindow(w http.ResponseWriter, r *http.Request) {
	if a.debug {
		log.Printf("%s %s", r.Method, r.URL.Path)
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Days int `json:"days"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	switch req.Days {
	case 1, 7, 30:
		a.setWindow(req.Days)
	default:
		http.Error(w, "invalid days", http.StatusBadRequest)
	}
}

func (a *App) handleDashboard(w http.ResponseWriter, r *http.Request) {
	if a.debug {
		log.Printf("%s %s", r.Method, r.URL.Path)
	}
	days := a.window()
	dash, err := a.buildDashboard(days)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	_ = json.NewEncoder(w).Encode(dash)
}

func (a *App) getSetting(key string) string {
	row := a.db.QueryRow(`SELECT value FROM app_settings WHERE key=?`, key)
	var value string
	if err := row.Scan(&value); err != nil {
		return ""
	}
	return value
}

func (a *App) setSetting(key, value string) error {
	_, err := a.db.Exec(`INSERT INTO app_settings(key, value) VALUES(?, ?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, value)
	return err
}

func (a *App) loadSettings() ChannelSettings {
	interval := 15
	if raw := strings.TrimSpace(a.getSetting("notify_interval_min")); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			interval = n
		}
	}
	return ChannelSettings{
		TelegramBotToken: a.getSetting("telegram_bot_token"),
		TelegramChannel:  a.getSetting("telegram_channel"),
		NotifyIntervalMin: interval,
	}
}

func (a *App) handleSettings(w http.ResponseWriter, r *http.Request) {
	if a.debug {
		log.Printf("%s %s", r.Method, r.URL.Path)
	}
	switch r.Method {
	case http.MethodGet:
		_ = json.NewEncoder(w).Encode(a.loadSettings())
	case http.MethodPost:
		var req ChannelSettings
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if err := a.setSetting("telegram_bot_token", strings.TrimSpace(req.TelegramBotToken)); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if err := a.setSetting("telegram_channel", strings.TrimSpace(req.TelegramChannel)); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		interval := req.NotifyIntervalMin
		if interval <= 0 {
			interval = 15
		}
		if err := a.setSetting("notify_interval_min", strconv.Itoa(interval)); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (a *App) handleChannelTest(w http.ResponseWriter, r *http.Request) {
	if a.debug {
		log.Printf("%s %s", r.Method, r.URL.Path)
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := a.sendTelegramTestMessage(); err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *App) handleUpgradePull(w http.ResponseWriter, r *http.Request) {
	if a.debug {
		log.Printf("%s %s", r.Method, r.URL.Path)
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	cmd := exec.Command("git", "pull", "--ff-only")
	cmd.Dir = getenv("APP_ROOT", ".")
	out, err := cmd.CombinedOutput()
	resp := map[string]any{
		"output": string(out),
	}
	if err != nil {
		resp["error"] = err.Error()
		w.WriteHeader(http.StatusBadGateway)
	}
	_ = json.NewEncoder(w).Encode(resp)
}

func (a *App) sendTelegramTestMessage() error {
	token := strings.TrimSpace(a.getSetting("telegram_bot_token"))
	channel := strings.TrimSpace(a.getSetting("telegram_channel"))
	if token == "" || channel == "" {
		return fmt.Errorf("telegram bot token or channel is empty")
	}

	url := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", token)
	payload := map[string]string{
		"chat_id": channel,
		"text":    "ETH Liquidation Map test message",
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(string(body)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		data, _ := io.ReadAll(resp.Body)
		if len(data) == 0 {
			return fmt.Errorf("telegram api returned %s", resp.Status)
		}
		return fmt.Errorf("telegram api returned %s: %s", resp.Status, strings.TrimSpace(string(data)))
	}
	return nil
}

func maskSensitive(s string) string {
	s = strings.TrimSpace(s)
	if len(s) <= 8 {
		return s
	}
	return s[:4] + strings.Repeat("*", len(s)-8) + s[len(s)-4:]
}

func (a *App) buildDashboard(days int) (Dashboard, error) {
	states, err := a.loadMarketStates(defaultSymbol)
	if err != nil {
		return Dashboard{}, err
	}
	currentPrice := weightedPrice(states)
	bands, short, long, err := a.loadHeatSnapshot(defaultSymbol, days)
	if err != nil {
		return Dashboard{}, err
	}
	return Dashboard{
		Symbol:       defaultSymbol,
		WindowDays:   days,
		GeneratedAt:  time.Now().UnixMilli(),
		States:       states,
		CurrentPrice: currentPrice,
		Bands:        bands,
		LongestShort: short,
		LongestLong:  long,
		Events:       a.loadRecentEvents(defaultSymbol, 86400),
	}, nil
}

func (a *App) loadMarketStates(symbol string) ([]MarketState, error) {
	rows, err := a.db.Query(`SELECT exchange, symbol, mark_price, oi_qty, oi_value_usd, funding_rate, updated_ts
		FROM market_state WHERE symbol=? ORDER BY exchange`, symbol)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []MarketState{}
	for rows.Next() {
		var s MarketState
		var oiQty, oiValue, funding sql.NullFloat64
		if err := rows.Scan(&s.Exchange, &s.Symbol, &s.MarkPrice, &oiQty, &oiValue, &funding, &s.UpdatedTS); err != nil {
			return nil, err
		}
		if oiQty.Valid { s.OIQty = &oiQty.Float64 }
		if oiValue.Valid { s.OIValueUSD = &oiValue.Float64 }
		if funding.Valid { s.FundingRate = &funding.Float64 }
		out = append(out, s)
	}
	return out, nil
}

func weightedPrice(states []MarketState) float64 {
	var sumPrice, sumWeight float64
	for _, s := range states {
		if s.MarkPrice <= 0 {
			continue
		}
		w := 1.0
		if s.OIValueUSD != nil && *s.OIValueUSD > 0 {
			w = *s.OIValueUSD
		} else if s.OIQty != nil && *s.OIQty > 0 {
			w = *s.OIQty * s.MarkPrice
		}
		sumPrice += s.MarkPrice * w
		sumWeight += w
	}
	if sumWeight == 0 {
		return 0
	}
	return sumPrice / sumWeight
}

func (a *App) loadHeatSnapshot(symbol string, days int) ([]BandRow, []any, []any, error) {
	cutoff := time.Now().Add(-time.Duration(days) * 24 * time.Hour).UnixMilli()
	rows, err := a.db.Query(`SELECT band, AVG(up_price), AVG(up_notional_usd), AVG(down_price), AVG(down_notional_usd)
		FROM band_reports WHERE symbol=? AND report_ts>=? GROUP BY band ORDER BY band`, symbol, cutoff)
	if err != nil {
		return nil, nil, nil, err
	}
	defer rows.Close()
	out := []BandRow{}
	for rows.Next() {
		var r BandRow
		if err := rows.Scan(&r.Band, &r.UpPrice, &r.UpNotionalUSD, &r.DownPrice, &r.DownNotionalUSD); err != nil {
			return nil, nil, nil, err
		}
		out = append(out, r)
	}
	short := a.loadLongestBar(symbol, days, "short")
	long := a.loadLongestBar(symbol, days, "long")
	return out, short, long, nil
}

func (a *App) loadLongestBar(symbol string, days int, side string) []any {
	cutoff := time.Now().Add(-time.Duration(days) * 24 * time.Hour).UnixMilli()
	row := a.db.QueryRow(`SELECT bucket_price, bucket_notional_usd FROM longest_bar_reports
		WHERE symbol=? AND side=? AND report_ts>=? ORDER BY bucket_notional_usd DESC LIMIT 1`, symbol, side, cutoff)
	var price, notional sql.NullFloat64
	if err := row.Scan(&price, &notional); err != nil {
		return []any{"-", 0}
	}
	p := "-"
	if price.Valid {
		p = fmt.Sprintf("%.1f", price.Float64)
	}
	n := 0.0
	if notional.Valid {
		n = notional.Float64
	}
	return []any{p, n}
}

func (a *App) loadRecentEvents(symbol string, seconds int) []EventRow {
	cutoff := time.Now().Add(-time.Duration(seconds) * time.Second).UnixMilli()
	rows, err := a.db.Query(`SELECT exchange, side, price, qty, notional_usd, event_ts FROM liquidation_events
		WHERE symbol=? AND event_ts>=? ORDER BY event_ts DESC LIMIT 30`, symbol, cutoff)
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := []EventRow{}
	for rows.Next() {
		var e EventRow
		if err := rows.Scan(&e.Exchange, &e.Side, &e.Price, &e.Qty, &e.NotionalUSD, &e.EventTS); err != nil {
			return nil
		}
		out = append(out, e)
	}
	return out
}

const indexHTML = `<!doctype html>
<html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>ETH Liquidation Map</title>
<style>
body{margin:0;background:#f5f7fb;color:#1f2937;font-family:Inter,system-ui,Segoe UI,Arial,sans-serif}
.nav{height:56px;background:#fff;border-bottom:1px solid #d9e0ea;display:flex;align-items:center;justify-content:space-between;padding:0 20px;position:sticky;top:0;z-index:10}
.nav-left,.nav-right{display:flex;align-items:center;gap:20px}.brand{font-size:18px;font-weight:700;color:#111827}
.menu a{color:#4b5563;text-decoration:none;font-size:14px;margin-right:18px}.menu a.active{color:#111827;font-weight:700}
.upgrade{color:#111827;font-weight:700;text-decoration:none}.wrap{max-width:1200px;margin:0 auto;padding:22px}
.top{display:flex;justify-content:space-between;align-items:center;gap:12px;flex-wrap:wrap}
.panel{border:1px solid #dce3ec;background:#fff;margin:14px 0;padding:16px;border-radius:10px;box-shadow:0 1px 2px rgba(15,23,42,.04)}
.btns button{margin-right:8px;background:#fff;color:#111827;border:1px solid #cbd5e1;padding:8px 14px;border-radius:8px;cursor:pointer}
.btns button.active{background:#22c55e;color:#fff;border-color:#22c55e} table{width:100%;border-collapse:collapse}
th,td{border-bottom:1px solid #e5e7eb;padding:8px 10px;text-align:right}th:first-child,td:first-child{text-align:left}
.grid{display:grid;grid-template-columns:1fr;gap:14px}.hint{color:#6b7280;font-size:12px}.mono{font-family:ui-monospace,SFMono-Regular,Menlo,Consolas,monospace}
</style></head><body>
<div class="nav"><div class="nav-left"><div class="brand">ETH Liquidation Map</div><div class="menu"><a href="/" class="active">&#28165;&#31639;&#28909;&#21306;</a><a href="/map">&#28165;&#31639;&#22320;&#22270;</a><a href="/channel">&#28040;&#24687;&#36890;&#36947;</a></div></div><div class="nav-right"><a href="#" class="upgrade" onclick="return doUpgrade(event)">&#21319;&#32423;</a></div></div>
<div class="wrap"><div class="panel top"><div><h2 style="margin:0 0 6px 0;color:#111827">ETH &#28165;&#31639;&#28909;&#21306;</h2><div class="hint">&#25353; <span class="mono">1</span> / <span class="mono">7</span> / <span class="mono">3</span> &#20999;&#25442; 1&#22825; / 7&#22825; / 30&#22825;</div></div><div class="btns"><button data-days="1">1&#22825;</button><button data-days="7">7&#22825;</button><button data-days="30">30&#22825;</button></div></div>
<div class="panel"><div id="status">loading...</div></div>
<div class="grid"><div class="panel"><h3>&#24066;&#22330;&#29366;&#24577;</h3><div id="market"></div></div><div class="panel"><h3>&#28165;&#31639;&#28909;&#21306;&#36895;&#25253;</h3><div id="bands"></div></div></div></div>
<script>
let currentDays=1;
async function setWindow(days){currentDays=days;await fetch('/api/window',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({days})});renderActive();load();}
function renderActive(){document.querySelectorAll('button[data-days]').forEach(b=>b.classList.toggle('active',Number(b.dataset.days)===currentDays));}
function fmtPrice(n){return Number(n).toLocaleString('zh-CN',{maximumFractionDigits:0})}
function fmtAmount(n){n=Number(n);if(!isFinite(n))return '-';const a=Math.abs(n);if(a>=1e8)return (n/1e8).toFixed(2)+'\u4ebf';if(a>=1e6)return (n/1e6).toFixed(2)+'\u767e\u4e07';return (n/1e4).toFixed(2)+'\u4e07';}
function fmt4(n){return Number(n).toFixed(8)}
function renderTable(rows,headers){if(!rows||!rows.length)return '<div class="hint">\u6682\u65e0\u6570\u636e</div>';let html='<table><thead><tr>'+headers.map(h=>'<th>'+h+'</th>').join('')+'</tr></thead><tbody>';for(const r of rows) html+='<tr>'+r.map(c=>'<td>'+c+'</td>').join('')+'</tr>';return html+'</tbody></table>';}
function renderHeatReport(d){
  const bands = d.bands || [];
  if(!bands.length) return '<div class="hint">\u6682\u65e0\u6570\u636e</div>';
  let html = '<table><thead>' +
    '<tr><th rowspan="2">\u70b9\u6570\u9608\u503c</th><th colspan="2">\u4e0a\u65b9\u7a7a\u5355</th><th colspan="2">\u4e0b\u65b9\u591a\u5355</th></tr>' +
    '<tr><th>\u6e05\u7b97\u4ef7\u683c</th><th>\u6e05\u7b97\u89c4\u6a21(\u4ebf)</th><th>\u6e05\u7b97\u4ef7\u683c</th><th>\u6e05\u7b97\u89c4\u6a21(\u4ebf)</th></tr>' +
    '</thead><tbody>';
  const toYi = n => (Number(n||0)/1e8).toFixed(1);
  for(const b of bands){
    html += '<tr>' +
      '<td>'+b.band+'\u70b9\u5185</td>' +
      '<td>'+fmtPrice(b.up_price)+'</td>' +
      '<td>'+toYi(b.up_notional_usd)+'</td>' +
      '<td>'+fmtPrice(b.down_price)+'</td>' +
      '<td>'+toYi(b.down_notional_usd)+'</td>' +
      '</tr>';
  }
  const ls = d.longest_short || [];
  const ll = d.longest_long || [];
  const sp = (ls.length>=1 && ls[0] !== '-') ? fmtPrice(ls[0]) : '-';
  const sn = (ls.length>=2) ? toYi(ls[1]) : '-';
  const lp = (ll.length>=1 && ll[0] !== '-') ? fmtPrice(ll[0]) : '-';
  const ln = (ll.length>=2) ? toYi(ll[1]) : '-';
  html += '<tr><td>\u6700\u957f\u67f1</td><td>'+sp+'</td><td>'+sn+'</td><td>'+lp+'</td><td>'+ln+'</td></tr>';
  html += '</tbody></table>';
  return html;
}
async function load(){
  const r=await fetch('/api/dashboard');
  const d=await r.json();
  currentDays=d.window_days||currentDays;
  renderActive();
  document.getElementById('status').textContent='\u5f53\u524d\u4ef7: '+fmtPrice(d.current_price)+' | \u5468\u671f: '+d.window_days+'\u5929 | \u66f4\u65b0\u65f6\u95f4: '+new Date(d.generated_at).toLocaleString();
  document.getElementById('market').innerHTML=renderTable((d.states||[]).map(s=>[s.exchange,fmtPrice(s.mark_price),s.oi_qty?fmtAmount(s.oi_qty*s.mark_price):'-',s.oi_value_usd?fmtAmount(s.oi_value_usd):'-',s.funding_rate==null?'-':fmt4(s.funding_rate)]),['\u4ea4\u6613\u6240','\u6807\u8bb0\u4ef7','OI\u6570\u91cf','OI\u4ef7\u503cUSD','Funding']);
  document.getElementById('bands').innerHTML=renderHeatReport(d);
}
async function doUpgrade(event){if(event)event.preventDefault();const answer=prompt('\u786e\u8ba4\u6267\u884c git pull \u5417\uff1f\u8f93\u5165 yes \u7ee7\u7eed\uff1a');if(answer!=='yes')return false;const r=await fetch('/api/upgrade/pull',{method:'POST'});const d=await r.json().catch(()=>({output:'',error:'response parse failed'}));alert((d.error?('\u62c9\u53d6\u5931\u8d25: '+d.error+'\n'):'\u62c9\u53d6\u5b8c\u6210\n')+(d.output||''));return false;}
document.querySelectorAll('button[data-days]').forEach(b=>b.onclick=()=>setWindow(Number(b.dataset.days)));document.addEventListener('keydown',e=>{if(e.key==='1')setWindow(1);if(e.key==='7')setWindow(7);if(e.key==='3')setWindow(30);});setInterval(load,5000);load();
</script></body></html>`

const mapHTML = `<!doctype html>
<html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>&#28165;&#31639;&#22320;&#22270;</title>
<style>
body{margin:0;background:#f5f7fb;color:#1f2937;font-family:Inter,system-ui,Segoe UI,Arial,sans-serif}.nav{height:56px;background:#fff;border-bottom:1px solid #d9e0ea;display:flex;align-items:center;justify-content:space-between;padding:0 20px;position:sticky;top:0;z-index:10}.nav-left,.nav-right{display:flex;align-items:center;gap:20px}.brand{font-size:18px;font-weight:700;color:#111827}.menu a{color:#4b5563;text-decoration:none;font-size:14px;margin-right:18px}.menu a.active{color:#111827;font-weight:700}.upgrade{color:#111827;font-weight:700;text-decoration:none}.wrap{max-width:1400px;margin:0 auto;padding:22px}.panel{border:1px solid #dce3ec;background:#fff;margin:14px 0;padding:16px;border-radius:10px;box-shadow:0 1px 2px rgba(15,23,42,.04)}.row{display:flex;gap:10px;align-items:center;flex-wrap:wrap;justify-content:space-between}.btns button{background:#fff;color:#111827;border:1px solid #cbd5e1;padding:8px 14px;border-radius:8px;cursor:pointer}.btns button.active{background:#22c55e;color:#fff;border-color:#22c55e}.small{font-size:12px;color:#6b7280}canvas{width:100%;height:760px;display:block;border:1px solid #e5e7eb;border-radius:10px;background:#fff}.legend{display:flex;gap:14px;align-items:center;flex-wrap:wrap;font-size:12px;color:#6b7280;margin-top:10px}.swatch{display:inline-block;width:10px;height:10px;border-radius:2px;margin-right:6px}
</style></head><body>
<div class="nav"><div class="nav-left"><div class="brand">ETH Liquidation Map</div><div class="menu"><a href="/">&#28165;&#31639;&#28909;&#21306;</a><a href="/map" class="active">&#28165;&#31639;&#22320;&#22270;</a><a href="/channel">&#28040;&#24687;&#36890;&#36947;</a></div></div><div class="nav-right"><a href="#" class="upgrade" onclick="return doUpgrade(event)">&#21319;&#32423;</a></div></div>
<div class="wrap"><div class="panel"><div class="row"><div><h2 style="margin:0;color:#111827">&#28165;&#31639;&#22320;&#22270;</h2><div class="small">汇总本地市场状态、热区、清算事件与最长柱数据，按价格轴绘制。</div></div><div class="btns"><button data-days="1">1天</button><button data-days="7">7天</button><button data-days="30">30天</button></div></div><div class="legend"><span><i class="swatch" style="background:#22c55e"></i>买单</span><span><i class="swatch" style="background:#ef4444"></i>卖单</span><span><i class="swatch" style="background:#f59e0b"></i>清算事件</span><span><i class="swatch" style="background:#38bdf8"></i>最长柱</span></div></div><div class="panel"><canvas id="chart" width="1600" height="760"></canvas></div></div>
<script>
let currentDays=30,dashboard=null;function fmtPrice(n){return Number(n).toLocaleString('zh-CN',{maximumFractionDigits:0})}function fmtAmount(n){n=Number(n);if(!isFinite(n))return '-';const a=Math.abs(n);if(a>=1e8)return (n/1e8).toFixed(2)+'\u4ebf';if(a>=1e6)return (n/1e6).toFixed(2)+'\u767e\u4e07';return (n/1e4).toFixed(2)+'\u4e07';}function renderActive(){document.querySelectorAll('button[data-days]').forEach(b=>b.classList.toggle('active',Number(b.dataset.days)===currentDays));}async function setWindow(days){currentDays=days;await fetch('/api/window',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({days})});renderActive();await load();}
function draw(){const c=document.getElementById('chart'),x=c.getContext('2d'),W=c.width,H=c.height;x.clearRect(0,0,W,H);x.fillStyle='#fff';x.fillRect(0,0,W,H);const bands=(dashboard&&dashboard.bands)||[],states=(dashboard&&dashboard.states)||[],ls=(dashboard&&dashboard.longest_short)||[],ll=(dashboard&&dashboard.longest_long)||[],events=(dashboard&&dashboard.events)||[],price=dashboard?dashboard.current_price:0,b=new Map(),snap=p=>Math.round(p/5)*5,add=(p,v,k)=>{if(!(p>0)||!(v>0))return;const key=snap(p),o=b.get(key)||{price:key,buy:0,sell:0,event:0,longest:0};o[k]+=v;b.set(key,o);};bands.forEach(r=>{add(r.up_price,r.up_notional_usd,'buy');add(r.down_price,r.down_notional_usd,'sell');});events.forEach(e=>add(e.price,e.notional_usd,'event'));if(ls.length>=2&&ls[0]!=='-')add(Number(ls[0]),Number(ls[1]||0),'longest');if(ll.length>=2&&ll[0]!=='-')add(Number(ll[0]),Number(ll[1]||0),'longest');states.forEach(s=>{if(s.mark_price>0&&s.oi_value_usd)add(s.mark_price,s.oi_value_usd,'longest');});const p=Array.from(b.values()).sort((a,z)=>a.price-z.price),min=p.length?Math.min(...p.map(v=>v.price)):0,max=p.length?Math.max(...p.map(v=>v.price)):1,mv=Math.max(1,...p.flatMap(v=>[v.buy||0,v.sell||0,v.event||0,v.longest||0]))*1.15,padL=90,padR=30,padT=50,padB=80,pw=W-padL-padR,ph=H-padT-padB,by=padT+ph;x.strokeStyle='#e5e7eb';x.lineWidth=1;x.font='14px sans-serif';x.fillStyle='#111827';for(let i=0;i<=6;i++){const y=padT+ph*(i/6);x.beginPath();x.moveTo(padL,y);x.lineTo(W-padR,y);x.stroke();x.fillText(fmtAmount(mv*(1-i/6)),18,y+4);}const sx=v=>padL+(max===min?pw/2:((v-min)/(max-min))*pw);p.forEach(v=>{const px=sx(v.price),w=8,bh=(v.buy/mv)*ph,sh=(v.sell/mv)*ph,eh=(v.event/mv)*ph,lh=(v.longest/mv)*ph;if(v.buy>0){x.fillStyle='#22c55e';x.fillRect(px-w-5,by-bh,w,bh);}if(v.sell>0){x.fillStyle='#ef4444';x.fillRect(px+1,by-sh,w,sh);}if(v.event>0){x.fillStyle='#f59e0b';x.fillRect(px-3,by-eh,6,eh);}if(v.longest>0){x.fillStyle='#38bdf8';x.fillRect(px-1,by-lh,4,lh);}});const cp=sx(price);x.strokeStyle='#111827';x.setLineDash([8,6]);x.beginPath();x.moveTo(cp,padT);x.lineTo(cp,by);x.stroke();x.setLineDash([]);x.fillStyle='#111827';x.fillText('当前价: '+fmtPrice(price),Math.min(W-padR-160,cp+10),padT+16);for(let i=0;i<=8;i++){const vp=min+(max-min)*(i/8),px=sx(vp);x.strokeStyle='#e5e7eb';x.beginPath();x.moveTo(px,by);x.lineTo(px,by+6);x.stroke();x.fillStyle='#111827';x.fillText(fmtPrice(vp),px-18,by+24);}x.fillStyle='#6b7280';x.fillText('价格按 5 USDT 粒度聚合',padL,H-18);}
async function load(){const r=await fetch('/api/dashboard');dashboard=await r.json();currentDays=dashboard.window_days||currentDays;renderActive();draw();}
async function doUpgrade(event){if(event)event.preventDefault();const answer=prompt('\u786e\u8ba4\u6267\u884c git pull \u5417\uff1f\u8f93\u5165 yes \u7ee7\u7eed\uff1a');if(answer!=='yes')return false;const r=await fetch('/api/upgrade/pull',{method:'POST'});const d=await r.json().catch(()=>({output:'',error:'response parse failed'}));alert((d.error?('\u62c9\u53d6\u5931\u8d25: '+d.error+'\n'):'\u62c9\u53d6\u5b8c\u6210\n')+(d.output||''));return false;}
document.querySelectorAll('button[data-days]').forEach(b=>b.onclick=()=>setWindow(Number(b.dataset.days)));setInterval(load,5000);load();
</script></body></html>`

const channelHTML = `<!doctype html>
<html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>&#28040;&#24687;&#36890;&#36947;</title>
<style>body{margin:0;background:#f5f7fb;color:#1f2937;font-family:Inter,system-ui,Segoe UI,Arial,sans-serif}.nav{height:56px;background:#fff;border-bottom:1px solid #d9e0ea;display:flex;align-items:center;justify-content:space-between;padding:0 20px;position:sticky;top:0;z-index:10}.nav-left,.nav-right{display:flex;align-items:center;gap:20px}.brand{font-size:18px;font-weight:700;color:#111827}.menu a{color:#4b5563;text-decoration:none;font-size:14px;margin-right:18px}.menu a.active{color:#111827;font-weight:700}.upgrade{color:#111827;font-weight:700;text-decoration:none}.wrap{max-width:900px;margin:0 auto;padding:22px}.panel{border:1px solid #dce3ec;background:#fff;margin:14px 0;padding:16px;border-radius:10px;box-shadow:0 1px 2px rgba(15,23,42,.04)}.small{font-size:12px;color:#6b7280}button.primary{background:#22c55e;color:#fff;border:0;padding:10px 16px;border-radius:8px;cursor:pointer}button.secondary{background:#fff;color:#111827;border:1px solid #cbd5e1;padding:10px 16px;border-radius:8px;cursor:pointer}input{width:100%;box-sizing:border-box;padding:10px;border:1px solid #cbd5e1;border-radius:8px;background:#fff;color:#111827;margin-top:6px}.row{display:flex;gap:10px;flex-wrap:wrap;align-items:center}</style></head>
<body><div class="nav"><div class="nav-left"><div class="brand">ETH Liquidation Map</div><div class="menu"><a href="/">&#28165;&#31639;&#28909;&#21306;</a><a href="/map">&#28165;&#31639;&#22320;&#22270;</a><a href="/channel" class="active">&#28040;&#24687;&#36890;&#36947;</a></div></div><div class="nav-right"><a href="#" class="upgrade" onclick="return doUpgrade(event)">&#21319;&#32423;</a></div></div>
<div class="wrap"><div class="panel"><h2 style="margin-top:0">Telegram &#28040;&#24687;&#36890;&#36947;</h2><div class="small">保存后会自动脱敏显示，仅保留前 4 位和后 4 位。点击“测试”可发送 Telegram 测试消息。</div><div style="margin-top:14px"><label>Telegram Bot Token</label><input id="token" autocomplete="off" placeholder="123456:ABC..."></div><div style="margin-top:14px"><label>Telegram Channel / Chat ID</label><input id="channel" autocomplete="off" placeholder="@mychannel 或 -100123456789"></div><div style="margin-top:16px" class="row"><button class="primary" onclick="save()">保存</button><button class="secondary" onclick="testTelegram()">测试</button><span id="msg" class="small" style="margin-left:10px"></span></div></div></div>
<script>
let rawToken={{printf "%q" .TelegramBotToken}},rawChannel={{printf "%q" .TelegramChannel}},rawInterval={{.NotifyIntervalMin}},tokenDirty=false,channelDirty=false;
function maskSensitive(v){v=(v||'').trim();if(!v)return '';if(v.length<=8)return v;return v.slice(0,4)+'*'.repeat(v.length-8)+v.slice(-4);}function syncInputs(){const t=document.getElementById('token'),c=document.getElementById('channel'),n=document.getElementById('notify-interval');if(!tokenDirty)t.value=rawToken?maskSensitive(rawToken):'';if(!channelDirty)c.value=rawChannel?maskSensitive(rawChannel):'';n.value=rawInterval||15;}function currentValue(i,r,d){const v=(i.value||'').trim();if(!d&&r&&v===maskSensitive(r))return r;return v;}document.getElementById('token').addEventListener('input',()=>{tokenDirty=true});document.getElementById('channel').addEventListener('input',()=>{channelDirty=true});syncInputs();
async function doUpgrade(event){if(event)event.preventDefault();const answer=prompt('\u786e\u8ba4\u6267\u884c git pull \u5417\uff1f\u8f93\u5165 yes \u7ee7\u7eed\uff1a');if(answer!=='yes')return false;const r=await fetch('/api/upgrade/pull',{method:'POST'});const d=await r.json().catch(()=>({output:'',error:'response parse failed'}));alert((d.error?('\u62c9\u53d6\u5931\u8d25: '+d.error+'\n'):'\u62c9\u53d6\u5b8c\u6210\n')+(d.output||''));return false;}
async function save(){const body={telegram_bot_token:currentValue(document.getElementById('token'),rawToken,tokenDirty),telegram_channel:currentValue(document.getElementById('channel'),rawChannel,channelDirty)};const r=await fetch('/api/settings',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify(body)});if(r.ok){rawToken=body.telegram_bot_token;rawChannel=body.telegram_channel;tokenDirty=false;channelDirty=false;syncInputs();document.getElementById('msg').textContent='保存成功';}else{document.getElementById('msg').textContent='保存失败';}}
async function testTelegram(){const msg=document.getElementById('msg');msg.textContent='正在发送测试消息...';const r=await fetch('/api/channel/test',{method:'POST'});msg.textContent=r.ok?'测试消息已发送':('测试失败：'+await r.text());}
</script></body></html>`
