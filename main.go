package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"os"
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

var bandSizes = []int{10, 20, 30, 40, 50, 60, 80, 100, 150}

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
	Recent1m      []EventRow    `json:"recent_1m"`
	Recent5m      []EventRow    `json:"recent_5m"`
	Recent15m     []EventRow    `json:"recent_15m"`
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
}

type ChannelSettings struct {
	TelegramBotToken string `json:"telegram_bot_token"`
	TelegramChannel  string `json:"telegram_channel"`
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
	db, err := sql.Open("sqlite", getenv("DB_PATH", defaultDBPath))
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()
	if err := initDB(db); err != nil {
		log.Fatal(err)
	}

	app := &App{db: db, windowDays: defaultWindowDays}
	mux := http.NewServeMux()
	mux.HandleFunc("/", app.handleIndex)
	mux.HandleFunc("/map", app.handleMap)
	mux.HandleFunc("/channel", app.handleChannel)
	mux.HandleFunc("/api/dashboard", app.handleDashboard)
	mux.HandleFunc("/api/window", app.handleWindow)
	mux.HandleFunc("/api/settings", app.handleSettings)

	log.Println("dashboard listening on http://127.0.0.1:8888")
	if err := http.ListenAndServe(defaultServerAddr, mux); err != nil {
		log.Fatal(err)
	}
}

func (a *App) handleIndex(w http.ResponseWriter, r *http.Request) {
	tpl := template.Must(template.New("index").Parse(indexHTML))
	_ = tpl.Execute(w, nil)
}

func (a *App) handleMap(w http.ResponseWriter, r *http.Request) {
	tpl := template.Must(template.New("map").Parse(mapHTML))
	_ = tpl.Execute(w, nil)
}

func (a *App) handleChannel(w http.ResponseWriter, r *http.Request) {
	tpl := template.Must(template.New("channel").Parse(channelHTML))
	_ = tpl.Execute(w, a.loadSettings())
}

func (a *App) handleWindow(w http.ResponseWriter, r *http.Request) {
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
	return ChannelSettings{
		TelegramBotToken: a.getSetting("telegram_bot_token"),
		TelegramChannel:  a.getSetting("telegram_channel"),
	}
}

func (a *App) handleSettings(w http.ResponseWriter, r *http.Request) {
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
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
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
		Recent1m:     a.loadRecentEvents(defaultSymbol, 60),
		Recent5m:     a.loadRecentEvents(defaultSymbol, 300),
		Recent15m:    a.loadRecentEvents(defaultSymbol, 900),
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
.nav{height:56px;background:#ffffff;border-bottom:1px solid #d9e0ea;display:flex;align-items:center;justify-content:space-between;padding:0 20px;position:sticky;top:0;z-index:10}
.nav-left,.nav-right{display:flex;align-items:center;gap:20px}
.brand{font-size:18px;font-weight:700;color:#111827}
.menu a{color:#4b5563;text-decoration:none;font-size:14px;margin-right:18px}
.menu a.active{color:#111827;font-weight:700}
.upgrade{color:#111827;font-weight:700;text-decoration:none}
.wrap{max-width:1200px;margin:0 auto;padding:22px}
.top{display:flex;justify-content:space-between;align-items:center;gap:12px;flex-wrap:wrap}
.panel{border:1px solid #dce3ec;background:#ffffff;margin:14px 0;padding:16px;border-radius:10px;box-shadow:0 1px 2px rgba(15,23,42,.04)}
.btns button{margin-right:8px;background:#ffffff;color:#111827;border:1px solid #cbd5e1;padding:8px 14px;border-radius:8px;cursor:pointer}
.btns button.active{background:#22c55e;color:#fff;border-color:#22c55e}
table{width:100%;border-collapse:collapse}
th,td{border-bottom:1px solid #e5e7eb;padding:8px 10px;text-align:right}
th:first-child,td:first-child{text-align:left}
.grid{display:grid;grid-template-columns:1fr;gap:14px}
.hint{color:#6b7280;font-size:12px}
.mono{font-family:ui-monospace,SFMono-Regular,Menlo,Consolas,monospace}
.small{font-size:12px;color:#6b7280}
input,textarea{width:100%;box-sizing:border-box;padding:10px;border:1px solid #cbd5e1;border-radius:8px;background:#fff;color:#111827}
button.primary{background:#22c55e;color:#fff;border:0;padding:10px 16px;border-radius:8px;cursor:pointer}
iframe{width:100%;height:760px;border:1px solid #e5e7eb;border-radius:10px;background:#fff}
.row{display:flex;gap:12px;flex-wrap:wrap;justify-content:space-between;align-items:center}
.row > *{flex:1 1 240px}
.nav-link{display:inline-block;padding:10px 14px;border:1px solid #cbd5e1;border-radius:8px;text-decoration:none;color:#111827;background:#fff}
.nav-link.active{background:#eef2ff;border-color:#a5b4fc}
</style></head>
<body>
<div class="nav">
  <div class="nav-left">
    <div class="brand">ETH Liquidation Map</div>
<body>
<div class="nav">
  <div class="nav-left">
    <div class="brand">ETH Liquidation Map</div>
    <div class="menu">
      <a href="/" class="active">清算热区</a>
      <a href="/map">清算地图</a>
      <a href="/channel">消息通道</a>
    </div>
  </div>
  <div class="nav-right"><a href="#" class="upgrade">升级</a></div>
</div>
<div class="wrap">
<div class="panel top">
  <div>
    <h2 style="margin:0 0 6px 0;color:#111827">ETH 清算热区</h2>
    <div class="hint">按 <span class="mono">1</span> / <span class="mono">7</span> / <span class="mono">3</span> 切换 1天 / 7天 / 30天</div>
  </div>
  <div class="btns">
    <button data-days="1">1?</button>
    <button data-days="7">7?</button>
    <button data-days="30">30?</button>
  </div>
</div>
<div class="panel"><div id="status">loading...</div></div>
<div class="grid">
  <div class="panel"><h3>市场状态</h3><div id="market"></div></div>
  <div class="panel"><h3>清算热区速报</h3><div id="bands"></div></div>
  <div class="panel"><h3>已发生清算统计</h3><div id="events"></div></div>
</div>
</div>
<script>
let currentDays = 1;
async function setWindow(days){
  currentDays = days;
  await fetch('/api/window',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({days})});
  renderActive();
  load();
}
function renderActive(){
  document.querySelectorAll('button[data-days]').forEach(b=>b.classList.toggle('active', Number(b.dataset.days)===currentDays));
}
function fmt(n, digits=1){ return Number(n).toLocaleString('zh-CN',{maximumFractionDigits:digits}) }
function fmt4(n){ return Number(n).toFixed(4) }
function renderTable(rows, headers){
  if(!rows || !rows.length) return '<div class="hint">暂无数据</div>';
  let html = '<table><thead><tr>'+headers.map(h=>'<th>'+h+'</th>').join('')+'</tr></thead><tbody>';
  for(const r of rows) html += '<tr>'+r.map(c=>'<td>'+c+'</td>').join('')+'</tr>';
  return html + '</tbody></table>';
}
function renderEvents(rows){
  if(!rows || !rows.length) return '<div class="hint">暂无数据</div>';
  return renderTable(rows.map(e=>[
    e.exchange, e.side, fmt(e.price), fmt(e.qty,4), fmt(e.notional_usd)
  ]), ['交易所','方向','价格','数量','名义价值USD']);
}
async function load(){
  const r = await fetch('/api/dashboard');
  const d = await r.json();
  currentDays = d.window_days || currentDays;
  renderActive();
  document.getElementById('status').textContent = '当前价: '+fmt(d.current_price)+' | 周期: '+d.window_days+'天 | 更新时间: '+new Date(d.generated_at).toLocaleString();
  document.getElementById('market').innerHTML = renderTable((d.states||[]).map(s=>[
    s.exchange, fmt(s.mark_price), s.oi_qty ? fmt(s.oi_qty) : '-', s.oi_value_usd ? fmt(s.oi_value_usd) : '-', s.funding_rate == null ? '-' : fmt4(s.funding_rate)
  ]), ['交易所','标记价','OI数量','OI价值USD','Funding']);
  document.getElementById('bands').innerHTML = renderTable((d.bands||[]).map(b=>[
    b.band+'点内', fmt(b.up_notional_usd), fmt(b.down_notional_usd), fmt(b.up_price), fmt(b.down_price)
  ]), ['点数阈值','上方空单均值','下方多单均值','上方价格','下方价格']);
  document.getElementById('events').innerHTML = '<div class="small">1分钟</div>'+renderEvents(d.recent_1m||[])+'<div class="small" style="margin-top:12px">5分钟</div>'+renderEvents(d.recent_5m||[])+'<div class="small" style="margin-top:12px">15分钟</div>'+renderEvents(d.recent_15m||[]);
}
document.querySelectorAll('button[data-days]').forEach(b=>b.onclick=()=>setWindow(Number(b.dataset.days)));
document.addEventListener('keydown',e=>{ if(e.key==='1') setWindow(1); if(e.key==='7') setWindow(7); if(e.key==='3') setWindow(30); });
setInterval(load,5000); load();
</script></body></html>`

const mapHTML = `<!doctype html>
<html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
   <title>清算地图</title>
<style>
body{margin:0;background:#f5f7fb;color:#1f2937;font-family:Inter,system-ui,Segoe UI,Arial,sans-serif}
.nav{height:56px;background:#ffffff;border-bottom:1px solid #d9e0ea;display:flex;align-items:center;justify-content:space-between;padding:0 20px;position:sticky;top:0;z-index:10}
.nav-left,.nav-right{display:flex;align-items:center;gap:20px}
.brand{font-size:18px;font-weight:700;color:#111827}
.menu a{color:#4b5563;text-decoration:none;font-size:14px;margin-right:18px}
.menu a.active{color:#111827;font-weight:700}
.upgrade{color:#111827;font-weight:700;text-decoration:none}
.wrap{max-width:1400px;margin:0 auto;padding:22px}
.panel{border:1px solid #dce3ec;background:#ffffff;margin:14px 0;padding:16px;border-radius:10px;box-shadow:0 1px 2px rgba(15,23,42,.04)}
.row{display:flex;gap:10px;align-items:center;flex-wrap:wrap;justify-content:space-between}
.btns button{background:#ffffff;color:#111827;border:1px solid #cbd5e1;padding:8px 14px;border-radius:8px;cursor:pointer}
.btns button.active{background:#22c55e;color:#fff;border-color:#22c55e}
iframe{width:100%;height:760px;border:1px solid #e5e7eb;border-radius:10px;background:#fff}
.small{font-size:12px;color:#6b7280}
</style></head>
<body>
<div class="nav">
  <div class="nav-left">
    <div class="brand">ETH Liquidation Map</div>
    <div class="menu">
      <a href="/">清算热区</a>
      <a href="/map" class="active">清算地图</a>
      <a href="/channel">消息通道</a>
    </div>
  </div>
  <div class="nav-right"><a href="#" class="upgrade">升级</a></div>
</div>
<div class="wrap">
  <div class="panel">
    <div class="row">
      <div>
        <h2 style="margin:0;color:#111827">CoinGlass 清算地图</h2>
        <div class="small">内嵌外部页面；如果被浏览器阻止，可点击下方链接直接打开。</div>
      </div>
      <div class="btns">
        <button data-days="1">1?</button>
        <button data-days="7">7?</button>
        <button data-days="30">30?</button>
      </div>
    </div>
    <div style="margin-top:10px">
      <a class="nav-link" id="cg-link" href="https://www.coinglass.com/zh/pro/futures/LiquidationMap" target="_blank">打开 CoinGlass 清算地图</a>
    </div>
  </div>
  <div class="panel">
    <iframe id="cg-frame" src="https://www.coinglass.com/zh/pro/futures/LiquidationMap"></iframe>
  </div>
</div>
<script>
function setWindow(days){
  document.querySelectorAll('button[data-days]').forEach(b=>b.classList.toggle('active', Number(b.dataset.days)===days));
  const url = 'https://www.coinglass.com/zh/pro/futures/LiquidationMap';
  document.getElementById('cg-link').href = url;
  document.getElementById('cg-frame').src = url;
}
document.querySelectorAll('button[data-days]').forEach(b=>b.onclick=()=>setWindow(Number(b.dataset.days)));
setWindow(30);
</script></body></html>`

const channelHTML = `<!doctype html>
<html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>消息通道</title>
<style>
body{margin:0;background:#f5f7fb;color:#1f2937;font-family:Inter,system-ui,Segoe UI,Arial,sans-serif}
.nav{height:56px;background:#ffffff;border-bottom:1px solid #d9e0ea;display:flex;align-items:center;justify-content:space-between;padding:0 20px;position:sticky;top:0;z-index:10}
.nav-left,.nav-right{display:flex;align-items:center;gap:20px}
.brand{font-size:18px;font-weight:700;color:#111827}
.menu a{color:#4b5563;text-decoration:none;font-size:14px;margin-right:18px}
.menu a.active{color:#111827;font-weight:700}
.upgrade{color:#111827;font-weight:700;text-decoration:none}
.wrap{max-width:900px;margin:0 auto;padding:22px}
.panel{border:1px solid #dce3ec;background:#ffffff;margin:14px 0;padding:16px;border-radius:10px;box-shadow:0 1px 2px rgba(15,23,42,.04)}
.small{font-size:12px;color:#6b7280}
button.primary{background:#22c55e;color:#fff;border:0;padding:10px 16px;border-radius:8px;cursor:pointer}
input{width:100%;box-sizing:border-box;padding:10px;border:1px solid #cbd5e1;border-radius:8px;background:#fff;color:#111827;margin-top:6px}
</style></head>
<body>
<div class="nav">
  <div class="nav-left">
    <div class="brand">ETH Liquidation Map</div>
    <div class="menu">
      <a href="/">清算热区</a>
      <a href="/map">清算地图</a>
      <a href="/channel" class="active">消息通道</a>
    </div>
  </div>
  <div class="nav-right"><a href="#" class="upgrade">升级</a></div>
</div>
<div class="wrap">
  <div class="panel">
    <h2 style="margin-top:0">Telegram 消息通道</h2>
    <div class="small">保存 Bot Token 和 Channel / Chat ID，供后续推送使用。</div>
    <div style="margin-top:14px">
      <label>Telegram Bot Token</label>
      <input id="token" value="{{.TelegramBotToken}}" placeholder="123456:ABC...">
    </div>
    <div style="margin-top:14px">
      <label>Telegram Channel / Chat ID</label>
      <input id="channel" value="{{.TelegramChannel}}" placeholder="@mychannel ? -100123456789">
    </div>
    <div style="margin-top:16px">
      <button class="primary" onclick="save()">保存</button>
      <span id="msg" class="small" style="margin-left:10px"></span>
    </div>
  </div>
</div>
<script>
async function save(){
  const body = {telegram_bot_token: document.getElementById('token').value, telegram_channel: document.getElementById('channel').value};
  const r = await fetch('/api/settings',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify(body)});
  document.getElementById('msg').textContent = r.ok ? '已保存' : '保存失败';
}
</script></body></html>`

func fmt1(v float64) string { return fmt.Sprintf("%.1f", v) }
