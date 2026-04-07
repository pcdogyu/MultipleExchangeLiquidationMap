package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/gorilla/websocket"
	"html/template"
	"io"
	"log"
	"math"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

const (
	defaultDBPath     = "liqmap.db"
	defaultSymbol     = "ETHUSDT"
	defaultServerAddr = ":8888"
	defaultWindowDays = 1
	windowIntraday    = 0
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
	Symbol       string        `json:"symbol"`
	WindowDays   int           `json:"window_days"`
	GeneratedAt  int64         `json:"generated_at"`
	States       []MarketState `json:"states"`
	CurrentPrice float64       `json:"current_price"`
	Bands        []BandRow     `json:"bands"`
	LongestShort []any         `json:"longest_short"`
	LongestLong  []any         `json:"longest_long"`
	Events       []EventRow    `json:"events"`
}

type EventRow struct {
	Exchange    string  `json:"exchange"`
	Side        string  `json:"side"`
	Price       float64 `json:"price"`
	Qty         float64 `json:"qty"`
	NotionalUSD float64 `json:"notional_usd"`
	EventTS     int64   `json:"event_ts"`
}

type PriceWallEvent struct {
	Side       string  `json:"side"`
	Price      float64 `json:"price"`
	Peak       float64 `json:"peak"`
	DurationMS int64   `json:"duration_ms"`
	EventTS    int64   `json:"event_ts"`
	Mode       string  `json:"mode"`
}

type App struct {
	db         *sql.DB
	httpClient *http.Client
	ob         *OrderBookHub
	mu         sync.RWMutex
	windowDays int
	debug      bool
}

type Level struct {
	Price float64 `json:"price"`
	Qty   float64 `json:"qty"`
}

type OrderBook struct {
	mu            sync.RWMutex
	Exchange      string
	Symbol        string
	Bids          map[string]float64
	Asks          map[string]float64
	LastUpdateID  int64
	LastSeq       int64
	UpdatedTS     int64
	LastSnapshot  int64
	LastWSEventTS int64
}

type OrderBookHub struct {
	books map[string]*OrderBook
}

var errResnapshot = errors.New("periodic resnapshot")

type Snapshot struct {
	Exchange    string
	Symbol      string
	MarkPrice   float64
	OIQty       float64
	OIValueUSD  float64
	FundingRate float64
	UpdatedTS   int64
}

type ChannelSettings struct {
	TelegramBotToken  string `json:"telegram_bot_token"`
	TelegramChannel   string `json:"telegram_channel"`
	NotifyIntervalMin int    `json:"notify_interval_min"`
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
		`CREATE TABLE IF NOT EXISTS price_wall_events (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			side TEXT NOT NULL,
			price REAL NOT NULL,
			peak_notional_usd REAL NOT NULL,
			duration_ms INTEGER NOT NULL,
			event_ts INTEGER NOT NULL,
			mode TEXT NOT NULL DEFAULT 'weighted',
			inserted_ts INTEGER NOT NULL
		);`,
		`CREATE INDEX IF NOT EXISTS idx_price_wall_events_ts ON price_wall_events(event_ts);`,
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

	app := &App{
		db: db,
		httpClient: &http.Client{
			Timeout: 12 * time.Second,
		},
		ob:         newOrderBookHub(),
		windowDays: defaultWindowDays,
		debug:      debug,
	}
	app.startCollector(context.Background())
	app.startOrderBookSync(context.Background())
	mux := http.NewServeMux()
	mux.HandleFunc("/", app.handleIndex)
	mux.HandleFunc("/map", app.handleMap)
	mux.HandleFunc("/channel", app.handleChannel)
	mux.HandleFunc("/api/dashboard", app.handleDashboard)
	mux.HandleFunc("/api/orderbook", app.handleOrderBook)
	mux.HandleFunc("/api/coinglass/map", app.handleCoinGlassMap)
	mux.HandleFunc("/api/window", app.handleWindow)
	mux.HandleFunc("/api/settings", app.handleSettings)
	mux.HandleFunc("/api/channel/test", app.handleChannelTest)
	mux.HandleFunc("/api/upgrade/pull", app.handleUpgradePull)
	mux.HandleFunc("/api/price-events", app.handlePriceEvents)

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
	case windowIntraday, 1, 7, 30:
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

func (a *App) handleCoinGlassMap(w http.ResponseWriter, r *http.Request) {
	if a.debug {
		log.Printf("%s %s", r.Method, r.URL.Path)
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	apiKey := strings.TrimSpace(os.Getenv("CG_API_KEY"))
	if apiKey == "" {
		http.Error(w, "CG_API_KEY is not set", http.StatusBadRequest)
		return
	}

	symbol := strings.TrimSpace(r.URL.Query().Get("symbol"))
	if symbol == "" {
		symbol = "ETH"
	}
	window := strings.TrimSpace(r.URL.Query().Get("window"))
	if window == "" {
		window = "1d"
	}

	url := fmt.Sprintf("https://open-api-v4.coinglass.com/api/futures/liquidation/aggregated-map?symbol=%s&interval=%s", symbol, window)
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	req.Header.Set("CG-API-KEY", apiKey)

	resp, err := a.httpClient.Do(req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write(body)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(body)
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
		TelegramBotToken:  a.getSetting("telegram_bot_token"),
		TelegramChannel:   a.getSetting("telegram_channel"),
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
	root := getenv("APP_ROOT", ".")
	pullCmd := exec.Command("git", "pull", "--ff-only")
	pullCmd.Dir = root
	pullOut, pullErr := pullCmd.CombinedOutput()
	if pullErr != nil {
		w.WriteHeader(http.StatusBadGateway)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"error":  pullErr.Error(),
			"output": "[git pull]\n" + string(pullOut),
		})
		return
	}
	buildCmd := exec.Command("go", "build", ".")
	buildCmd.Dir = root
	buildOut, buildErr := buildCmd.CombinedOutput()
	resp := map[string]any{
		"output": "[git pull]\n" + string(pullOut) + "\n[go build]\n" + string(buildOut),
	}
	if buildErr != nil {
		resp["error"] = buildErr.Error()
		w.WriteHeader(http.StatusBadGateway)
	}
	_ = json.NewEncoder(w).Encode(resp)
}

func (a *App) handlePriceEvents(w http.ResponseWriter, r *http.Request) {
	if a.debug {
		log.Printf("%s %s", r.Method, r.URL.Path)
	}
	switch r.Method {
	case http.MethodGet:
		cutoff := time.Now().Add(-1 * time.Hour).UnixMilli()
		rows, err := a.db.Query(`SELECT side, price, peak_notional_usd, duration_ms, event_ts, mode
			FROM price_wall_events
			WHERE event_ts>=?
			ORDER BY event_ts DESC
			LIMIT 200`, cutoff)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		defer rows.Close()
		out := make([]PriceWallEvent, 0, 200)
		for rows.Next() {
			var e PriceWallEvent
			if err := rows.Scan(&e.Side, &e.Price, &e.Peak, &e.DurationMS, &e.EventTS, &e.Mode); err != nil {
				continue
			}
			out = append(out, e)
		}
		_ = json.NewEncoder(w).Encode(out)
	case http.MethodPost:
		var req PriceWallEvent
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		req.Side = strings.ToLower(strings.TrimSpace(req.Side))
		if req.Side != "bid" && req.Side != "ask" {
			http.Error(w, "invalid side", http.StatusBadRequest)
			return
		}
		if req.Price <= 0 || req.Peak <= 0 || req.DurationMS <= 0 {
			http.Error(w, "invalid payload", http.StatusBadRequest)
			return
		}
		req.Mode = strings.ToLower(strings.TrimSpace(req.Mode))
		if req.Mode != "weighted" && req.Mode != "merged" {
			req.Mode = "weighted"
		}
		if req.EventTS <= 0 {
			req.EventTS = time.Now().UnixMilli()
		}
		_, err := a.db.Exec(`INSERT INTO price_wall_events(side, price, peak_notional_usd, duration_ms, event_ts, mode, inserted_ts)
			VALUES(?, ?, ?, ?, ?, ?, ?)`,
			req.Side, req.Price, req.Peak, req.DurationMS, req.EventTS, req.Mode, time.Now().UnixMilli())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
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

func windowCutoff(now time.Time, window int) int64 {
	if window == windowIntraday {
		return intradayCutoff(now).UnixMilli()
	}
	if window <= 0 {
		window = 1
	}
	return now.Add(-time.Duration(window) * 24 * time.Hour).UnixMilli()
}

func intradayCutoff(now time.Time) time.Time {
	loc := now.Location()
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		// fallback: use fixed summer open time in local zone
		t := now.In(loc)
		return time.Date(t.Year(), t.Month(), t.Day(), 8, 0, 0, 0, loc)
	}
	t := now.In(loc)
	today8 := time.Date(t.Year(), t.Month(), t.Day(), 8, 0, 0, 0, loc)
	yesterday8 := today8.Add(-24 * time.Hour)

	nowNY := now.In(ny)
	todayOpenLocal := time.Date(nowNY.Year(), nowNY.Month(), nowNY.Day(), 9, 30, 0, 0, ny).In(loc)
	yesterdayOpenLocal := time.Date(nowNY.Year(), nowNY.Month(), nowNY.Day()-1, 9, 30, 0, 0, ny).In(loc)

	candidates := []time.Time{today8, yesterday8, todayOpenLocal, yesterdayOpenLocal}
	best := time.Time{}
	for _, c := range candidates {
		if c.After(now) {
			continue
		}
		if best.IsZero() || c.After(best) {
			best = c
		}
	}
	if best.IsZero() || best.After(now) {
		return yesterday8
	}
	return best
}

func (a *App) startCollector(ctx context.Context) {
	intervalSec := 30
	if raw := strings.TrimSpace(os.Getenv("COLLECT_INTERVAL_SEC")); raw != "" {
		if v, err := strconv.Atoi(raw); err == nil && v >= 10 {
			intervalSec = v
		}
	}
	interval := time.Duration(intervalSec) * time.Second
	go func() {
		if err := a.collectAndStore(defaultSymbol); err != nil && a.debug {
			log.Printf("collector initial run failed: %v", err)
		}
		tk := time.NewTicker(interval)
		defer tk.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tk.C:
				if err := a.collectAndStore(defaultSymbol); err != nil && a.debug {
					log.Printf("collector run failed: %v", err)
				}
			}
		}
	}()
}

func (a *App) collectAndStore(symbol string) error {
	now := time.Now().UnixMilli()
	snapshots := make([]Snapshot, 0, 3)
	if s, err := a.fetchBinanceSnapshot(symbol); err == nil {
		snapshots = append(snapshots, s)
	} else if a.debug {
		log.Printf("binance fetch failed: %v", err)
	}
	if s, err := a.fetchBybitSnapshot(symbol); err == nil {
		snapshots = append(snapshots, s)
	} else if a.debug {
		log.Printf("bybit fetch failed: %v", err)
	}
	if s, err := a.fetchOKXSnapshot("ETH-USDT-SWAP"); err == nil {
		s.Symbol = symbol
		snapshots = append(snapshots, s)
	} else if a.debug {
		log.Printf("okx fetch failed: %v", err)
	}
	if len(snapshots) == 0 {
		return errors.New("all market fetches failed")
	}
	for _, s := range snapshots {
		if err := a.upsertMarketState(s); err != nil {
			return err
		}
	}
	return a.insertBandAndLongest(symbol, snapshots, now)
}

func (a *App) upsertMarketState(s Snapshot) error {
	_, err := a.db.Exec(`INSERT INTO market_state(exchange, symbol, mark_price, oi_qty, oi_value_usd, funding_rate, updated_ts)
		VALUES(?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(exchange, symbol) DO UPDATE SET
			mark_price=excluded.mark_price,
			oi_qty=excluded.oi_qty,
			oi_value_usd=excluded.oi_value_usd,
			funding_rate=excluded.funding_rate,
			updated_ts=excluded.updated_ts`,
		s.Exchange, s.Symbol, s.MarkPrice, s.OIQty, s.OIValueUSD, s.FundingRate, s.UpdatedTS)
	return err
}

func clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func (a *App) insertBandAndLongest(symbol string, snapshots []Snapshot, nowTS int64) error {
	if len(snapshots) == 0 {
		return nil
	}
	states := make([]MarketState, 0, len(snapshots))
	avgFunding := 0.0
	for _, s := range snapshots {
		oiQty := s.OIQty
		oiUSD := s.OIValueUSD
		funding := s.FundingRate
		states = append(states, MarketState{
			Exchange:    s.Exchange,
			Symbol:      s.Symbol,
			MarkPrice:   s.MarkPrice,
			OIQty:       &oiQty,
			OIValueUSD:  &oiUSD,
			FundingRate: &funding,
			UpdatedTS:   s.UpdatedTS,
		})
		avgFunding += s.FundingRate
	}
	avgFunding /= float64(len(snapshots))
	current := averageSnapshotPrice(snapshots)
	current = math.Round(current*10) / 10
	if current <= 0 {
		return nil
	}

	tx, err := a.db.Begin()
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	var maxLongBand int
	var maxLongPrice, maxLongNotional float64
	var maxShortBand int
	var maxShortPrice, maxShortNotional float64
	prevUpTotal := 0.0
	prevDownTotal := 0.0

	leverageLevels := []float64{8, 10, 12, 15, 20, 25, 30, 40, 50, 75, 100}
	levelWeights := []float64{0.07, 0.09, 0.11, 0.13, 0.14, 0.13, 0.11, 0.1, 0.07, 0.03, 0.02}
	weightSum := 0.0
	for _, w := range levelWeights {
		weightSum += w
	}
	for i := range levelWeights {
		levelWeights[i] /= weightSum
	}

	for _, band := range bandSizes {
		bandF := float64(band)
		upNotional := 0.0
		downNotional := 0.0
		for _, s := range snapshots {
			exchangeOI := s.OIValueUSD
			if exchangeOI <= 0 {
				continue
			}
			// funding > 0 usually means longs are more crowded, so downside liquidation intensity should be higher.
			longCrowd := clamp(0.52+s.FundingRate*7000, 0.22, 0.82)
			shortCrowd := 1 - longCrowd
			for i, lev := range leverageLevels {
				w := levelWeights[i]
				dist := current * (0.88 / lev)
				spread := math.Max(6.0, dist*0.12)
				cdf := 1.0 / (1.0 + math.Exp(-(bandF-dist)/spread))
				upNotional += exchangeOI * shortCrowd * w * cdf
				downNotional += exchangeOI * longCrowd * w * cdf
			}
		}

		// global funding tilt to avoid symmetric curves
		globalLongTilt := clamp(0.5+avgFunding*4000, 0.35, 0.65)
		downNotional *= (0.8 + 0.4*globalLongTilt)
		upNotional *= (0.8 + 0.4*(1-globalLongTilt))

		upPrice := current + float64(band)
		downPrice := current - float64(band)
		if downPrice <= 0 {
			downPrice = current * 0.3
		}

		if _, err = tx.Exec(`INSERT INTO band_reports(report_ts, symbol, current_price, band, up_price, up_notional_usd, down_price, down_notional_usd)
			VALUES(?, ?, ?, ?, ?, ?, ?, ?)`, nowTS, symbol, current, band, upPrice, upNotional, downPrice, downNotional); err != nil {
			return err
		}
		upInc := math.Max(0, upNotional-prevUpTotal)
		downInc := math.Max(0, downNotional-prevDownTotal)
		prevUpTotal = upNotional
		prevDownTotal = downNotional

		if upInc > maxShortNotional {
			maxShortNotional = upInc
			maxShortPrice = upPrice
			maxShortBand = band
		}
		if downInc > maxLongNotional {
			maxLongNotional = downInc
			maxLongPrice = downPrice
			maxLongBand = band
		}
	}

	if _, err = tx.Exec(`INSERT INTO longest_bar_reports(report_ts, symbol, side, bucket_size, bucket_price, bucket_notional_usd)
		VALUES(?, ?, 'short', ?, ?, ?)`, nowTS, symbol, maxShortBand, maxShortPrice, maxShortNotional); err != nil {
		return err
	}
	if _, err = tx.Exec(`INSERT INTO longest_bar_reports(report_ts, symbol, side, bucket_size, bucket_price, bucket_notional_usd)
		VALUES(?, ?, 'long', ?, ?, ?)`, nowTS, symbol, maxLongBand, maxLongPrice, maxLongNotional); err != nil {
		return err
	}
	return tx.Commit()
}

func parseFloat(raw string) float64 {
	v, _ := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	return v
}

func (a *App) fetchJSON(url string, out any) error {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "MultipleExchangeLiquidationMap/1.0")
	req.Header.Set("Accept", "application/json")
	resp, err := a.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("http %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (a *App) fetchBinanceSnapshot(symbol string) (Snapshot, error) {
	var premium struct {
		MarkPrice       string `json:"markPrice"`
		LastFundingRate string `json:"lastFundingRate"`
	}
	var oi struct {
		OpenInterest string `json:"openInterest"`
	}
	if err := a.fetchJSON("https://fapi.binance.com/fapi/v1/premiumIndex?symbol="+symbol, &premium); err != nil {
		return Snapshot{}, err
	}
	if err := a.fetchJSON("https://fapi.binance.com/fapi/v1/openInterest?symbol="+symbol, &oi); err != nil {
		return Snapshot{}, err
	}
	mark := parseFloat(premium.MarkPrice)
	oiQty := parseFloat(oi.OpenInterest)
	return Snapshot{
		Exchange:    "binance",
		Symbol:      symbol,
		MarkPrice:   mark,
		OIQty:       oiQty,
		OIValueUSD:  oiQty * mark,
		FundingRate: parseFloat(premium.LastFundingRate),
		UpdatedTS:   time.Now().UnixMilli(),
	}, nil
}

func (a *App) fetchBybitSnapshot(symbol string) (Snapshot, error) {
	var resp struct {
		RetCode int    `json:"retCode"`
		RetMsg  string `json:"retMsg"`
		Result  struct {
			List []struct {
				MarkPrice         string `json:"markPrice"`
				OpenInterest      string `json:"openInterest"`
				OpenInterestValue string `json:"openInterestValue"`
				FundingRate       string `json:"fundingRate"`
			} `json:"list"`
		} `json:"result"`
	}
	if err := a.fetchJSON("https://api.bybit.com/v5/market/tickers?category=linear&symbol="+symbol, &resp); err != nil {
		return Snapshot{}, err
	}
	if resp.RetCode != 0 || len(resp.Result.List) == 0 {
		return Snapshot{}, fmt.Errorf("bybit invalid response: code=%d msg=%s", resp.RetCode, resp.RetMsg)
	}
	row := resp.Result.List[0]
	mark := parseFloat(row.MarkPrice)
	oiQty := parseFloat(row.OpenInterest)
	oiUSD := parseFloat(row.OpenInterestValue)
	if oiUSD <= 0 {
		oiUSD = oiQty * mark
	}
	return Snapshot{
		Exchange:    "bybit",
		Symbol:      symbol,
		MarkPrice:   mark,
		OIQty:       oiQty,
		OIValueUSD:  oiUSD,
		FundingRate: parseFloat(row.FundingRate),
		UpdatedTS:   time.Now().UnixMilli(),
	}, nil
}

func (a *App) fetchOKXSnapshot(instID string) (Snapshot, error) {
	var markResp struct {
		Code string `json:"code"`
		Data []struct {
			MarkPx string `json:"markPx"`
		} `json:"data"`
	}
	var oiResp struct {
		Code string `json:"code"`
		Data []struct {
			OI    string `json:"oi"`
			OIUsd string `json:"oiUsd"`
		} `json:"data"`
	}
	var fundingResp struct {
		Code string `json:"code"`
		Data []struct {
			FundingRate string `json:"fundingRate"`
		} `json:"data"`
	}
	if err := a.fetchJSON("https://www.okx.com/api/v5/public/mark-price?instType=SWAP&instId="+instID, &markResp); err != nil {
		return Snapshot{}, err
	}
	if err := a.fetchJSON("https://www.okx.com/api/v5/public/open-interest?instType=SWAP&instId="+instID, &oiResp); err != nil {
		return Snapshot{}, err
	}
	if err := a.fetchJSON("https://www.okx.com/api/v5/public/funding-rate?instId="+instID, &fundingResp); err != nil {
		return Snapshot{}, err
	}
	if len(markResp.Data) == 0 || len(oiResp.Data) == 0 {
		return Snapshot{}, errors.New("okx empty response")
	}
	mark := parseFloat(markResp.Data[0].MarkPx)
	oiQty := parseFloat(oiResp.Data[0].OI)
	oiUSD := parseFloat(oiResp.Data[0].OIUsd)
	if oiUSD <= 0 {
		oiUSD = oiQty * mark
	}
	funding := 0.0
	if len(fundingResp.Data) > 0 {
		funding = parseFloat(fundingResp.Data[0].FundingRate)
	}
	return Snapshot{
		Exchange:    "okx",
		MarkPrice:   mark,
		OIQty:       oiQty,
		OIValueUSD:  oiUSD,
		FundingRate: funding,
		UpdatedTS:   time.Now().UnixMilli(),
	}, nil
}

func (a *App) buildDashboard(days int) (Dashboard, error) {
	states, err := a.loadMarketStates(defaultSymbol)
	if err != nil {
		return Dashboard{}, err
	}
	currentPrice := averageMarkPrice(states)
	currentPrice = math.Round(currentPrice*10) / 10
	cutoff := windowCutoff(time.Now(), days)
	bands, short, long, err := a.loadHeatSnapshot(defaultSymbol, cutoff)
	if err != nil {
		return Dashboard{}, err
	}
	eventsCutoff := cutoff
	if days != windowIntraday {
		eventsCutoff = time.Now().Add(-24 * time.Hour).UnixMilli()
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
		Events:       a.loadRecentEvents(defaultSymbol, eventsCutoff),
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
		if oiQty.Valid {
			s.OIQty = &oiQty.Float64
		}
		if oiValue.Valid {
			s.OIValueUSD = &oiValue.Float64
		}
		if funding.Valid {
			s.FundingRate = &funding.Float64
		}
		out = append(out, s)
	}
	return out, nil
}

func averageMarkPrice(states []MarketState) float64 {
	var sumPrice float64
	var cnt int
	for _, s := range states {
		if s.MarkPrice <= 0 {
			continue
		}
		sumPrice += s.MarkPrice
		cnt++
	}
	if cnt == 0 {
		return 0
	}
	return sumPrice / float64(cnt)
}

func averageSnapshotPrice(snapshots []Snapshot) float64 {
	var sum float64
	var cnt int
	for _, s := range snapshots {
		if s.MarkPrice <= 0 {
			continue
		}
		sum += s.MarkPrice
		cnt++
	}
	if cnt == 0 {
		return 0
	}
	return sum / float64(cnt)
}

func (a *App) loadHeatSnapshot(symbol string, cutoff int64) ([]BandRow, []any, []any, error) {
	var latestTS int64
	if err := a.db.QueryRow(`SELECT report_ts FROM band_reports WHERE symbol=? AND report_ts>=? ORDER BY report_ts DESC LIMIT 1`, symbol, cutoff).Scan(&latestTS); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return []BandRow{}, []any{"-", 0}, []any{"-", 0}, nil
		}
		return nil, nil, nil, err
	}

	rows, err := a.db.Query(`SELECT band, AVG(up_price), AVG(up_notional_usd), AVG(down_price), AVG(down_notional_usd)
		FROM band_reports WHERE symbol=? AND report_ts=? GROUP BY band ORDER BY band`, symbol, latestTS)
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
	short := a.loadLongestBarAt(symbol, latestTS, "short")
	long := a.loadLongestBarAt(symbol, latestTS, "long")
	return out, short, long, nil
}

func (a *App) loadLongestBarAt(symbol string, reportTS int64, side string) []any {
	row := a.db.QueryRow(`SELECT bucket_price, bucket_notional_usd FROM longest_bar_reports
		WHERE symbol=? AND side=? AND report_ts=? ORDER BY bucket_notional_usd DESC LIMIT 1`, symbol, side, reportTS)
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

func (a *App) loadRecentEvents(symbol string, cutoff int64) []EventRow {
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

func newOrderBookHub() *OrderBookHub {
	return &OrderBookHub{
		books: map[string]*OrderBook{
			"binance": {Exchange: "binance", Symbol: "ETHUSDT", Bids: map[string]float64{}, Asks: map[string]float64{}},
			"bybit":   {Exchange: "bybit", Symbol: "ETHUSDT", Bids: map[string]float64{}, Asks: map[string]float64{}},
			"okx":     {Exchange: "okx", Symbol: "ETH-USDT-SWAP", Bids: map[string]float64{}, Asks: map[string]float64{}},
		},
	}
}

func (h *OrderBookHub) get(exchange string) *OrderBook {
	return h.books[strings.ToLower(exchange)]
}

func normalizePriceKey(price float64) string {
	return strconv.FormatFloat(price, 'f', -1, 64)
}

func (b *OrderBook) applySide(side map[string]float64, price, qty float64) {
	if price <= 0 {
		return
	}
	key := normalizePriceKey(price)
	if qty <= 0 {
		delete(side, key)
		return
	}
	side[key] = qty
}

func parseAnyFloat(v any) float64 {
	switch t := v.(type) {
	case float64:
		return t
	case string:
		f, _ := strconv.ParseFloat(strings.TrimSpace(t), 64)
		return f
	case json.Number:
		f, _ := t.Float64()
		return f
	default:
		f, _ := strconv.ParseFloat(strings.TrimSpace(fmt.Sprint(v)), 64)
		return f
	}
}

func asPairs(v any) [][2]float64 {
	rows, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([][2]float64, 0, len(rows))
	for _, r := range rows {
		arr, ok := r.([]any)
		if !ok || len(arr) < 2 {
			continue
		}
		p := parseAnyFloat(arr[0])
		q := parseAnyFloat(arr[1])
		if p <= 0 {
			continue
		}
		out = append(out, [2]float64{p, q})
	}
	return out
}

func (b *OrderBook) snapshot(bids [][2]float64, asks [][2]float64, updateID int64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.Bids = map[string]float64{}
	b.Asks = map[string]float64{}
	for _, lv := range bids {
		if lv[1] > 0 {
			b.Bids[normalizePriceKey(lv[0])] = lv[1]
		}
	}
	for _, lv := range asks {
		if lv[1] > 0 {
			b.Asks[normalizePriceKey(lv[0])] = lv[1]
		}
	}
	b.LastUpdateID = updateID
	b.LastSeq = 0
	now := time.Now().UnixMilli()
	b.UpdatedTS = now
	b.LastSnapshot = now
}

func (b *OrderBook) applyDelta(bids [][2]float64, asks [][2]float64, updateID int64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, lv := range bids {
		b.applySide(b.Bids, lv[0], lv[1])
	}
	for _, lv := range asks {
		b.applySide(b.Asks, lv[0], lv[1])
	}
	if updateID > b.LastUpdateID {
		b.LastUpdateID = updateID
	}
	now := time.Now().UnixMilli()
	b.UpdatedTS = now
	b.LastWSEventTS = now
}

func sideTop(side map[string]float64, limit int, bid bool) []Level {
	out := make([]Level, 0, len(side))
	for k, q := range side {
		p, err := strconv.ParseFloat(k, 64)
		if err != nil || p <= 0 || q <= 0 {
			continue
		}
		out = append(out, Level{Price: p, Qty: q})
	}
	sort.Slice(out, func(i, j int) bool {
		if bid {
			return out[i].Price > out[j].Price
		}
		return out[i].Price < out[j].Price
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

func copySide(side map[string]float64) map[string]float64 {
	out := make(map[string]float64, len(side))
	for k, v := range side {
		out[k] = v
	}
	return out
}

func (b *OrderBook) view(limit int) map[string]any {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return map[string]any{
		"exchange":       b.Exchange,
		"symbol":         b.Symbol,
		"last_update_id": b.LastUpdateID,
		"last_seq":       b.LastSeq,
		"updated_ts":     b.UpdatedTS,
		"last_snapshot":  b.LastSnapshot,
		"last_ws_event":  b.LastWSEventTS,
		"bids":           sideTop(b.Bids, limit, true),
		"asks":           sideTop(b.Asks, limit, false),
	}
}

func (a *App) handleOrderBook(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	ex := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("exchange")))
	mode := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("mode")))
	if mode == "" {
		mode = "weighted"
	}
	if mode != "weighted" && mode != "merged" {
		http.Error(w, "unknown mode", http.StatusBadRequest)
		return
	}
	limit := 20
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 && n <= 200 {
			limit = n
		}
	}
	if ex != "" {
		book := a.ob.get(ex)
		if book == nil {
			http.Error(w, "unknown exchange", http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(book.view(limit))
		return
	}
	weights := a.orderBookWeights()
	if mode == "merged" {
		weights = map[string]float64{"binance": 1, "bybit": 1, "okx": 1}
	}
	resp := map[string]any{
		"mode":    mode,
		"binance": a.ob.get("binance").view(limit),
		"bybit":   a.ob.get("bybit").view(limit),
		"okx":     a.ob.get("okx").view(limit),
		"weights": weights,
		"merged":  a.mergedOrderBook(limit, weights),
	}
	_ = json.NewEncoder(w).Encode(resp)
}

func (a *App) orderBookWeights() map[string]float64 {
	weights := map[string]float64{"binance": 1, "bybit": 1, "okx": 1}
	rows, err := a.db.Query(`SELECT LOWER(exchange), oi_value_usd FROM market_state WHERE symbol=?`, defaultSymbol)
	if err != nil {
		return weights
	}
	defer rows.Close()
	sum := 0.0
	for rows.Next() {
		var ex string
		var oi sql.NullFloat64
		if err := rows.Scan(&ex, &oi); err != nil {
			continue
		}
		if !oi.Valid || oi.Float64 <= 0 {
			continue
		}
		weights[ex] = oi.Float64
		sum += oi.Float64
	}
	if sum <= 0 {
		weights["binance"], weights["bybit"], weights["okx"] = 1.0/3, 1.0/3, 1.0/3
		return weights
	}
	for k, v := range weights {
		weights[k] = v / sum
	}
	return weights
}

func (a *App) mergedOrderBook(limit int, weights map[string]float64) map[string]any {
	type obv struct {
		ex   string
		bids []Level
		asks []Level
	}
	makeView := func(ex string) obv {
		b := a.ob.get(ex)
		if b == nil {
			return obv{ex: ex}
		}
		b.mu.RLock()
		bidMap := copySide(b.Bids)
		askMap := copySide(b.Asks)
		b.mu.RUnlock()
		return obv{ex: ex, bids: sideTop(bidMap, 400, true), asks: sideTop(askMap, 400, false)}
	}
	views := []obv{makeView("binance"), makeView("bybit"), makeView("okx")}
	bidMap := map[string]float64{}
	askMap := map[string]float64{}
	weightedBestBid := 0.0
	weightedBestAsk := 0.0
	sumWBid := 0.0
	sumWAsk := 0.0
	for _, v := range views {
		w := weights[v.ex]
		if w <= 0 {
			continue
		}
		if len(v.bids) > 0 {
			weightedBestBid += v.bids[0].Price * w
			sumWBid += w
		}
		if len(v.asks) > 0 {
			weightedBestAsk += v.asks[0].Price * w
			sumWAsk += w
		}
		for _, lv := range v.bids {
			key := strconv.FormatFloat(math.Round(lv.Price*10)/10, 'f', 1, 64)
			bidMap[key] += lv.Qty * w
		}
		for _, lv := range v.asks {
			key := strconv.FormatFloat(math.Round(lv.Price*10)/10, 'f', 1, 64)
			askMap[key] += lv.Qty * w
		}
	}
	bestBid := 0.0
	bestAsk := 0.0
	if sumWBid > 0 {
		bestBid = weightedBestBid / sumWBid
	}
	if sumWAsk > 0 {
		bestAsk = weightedBestAsk / sumWAsk
	}
	mid := 0.0
	if bestBid > 0 && bestAsk > 0 {
		mid = (bestBid + bestAsk) / 2
	}
	bids := sideTop(bidMap, limit, true)
	asks := sideTop(askMap, limit, false)
	return map[string]any{
		"best_bid":    bestBid,
		"best_ask":    bestAsk,
		"mid":         mid,
		"bids":        bids,
		"asks":        asks,
		"depth_curve": buildDepthCurve(bids, asks, mid),
	}
}

func buildDepthCurve(bids, asks []Level, mid float64) []map[string]float64 {
	if mid <= 0 {
		return nil
	}
	steps := []float64{5, 10, 20, 30, 50, 75, 100}
	out := make([]map[string]float64, 0, len(steps))
	for _, bps := range steps {
		bidNotional := 0.0
		askNotional := 0.0
		for _, lv := range bids {
			dist := (mid - lv.Price) / mid * 10000
			if dist <= bps && dist >= 0 {
				bidNotional += lv.Price * lv.Qty
			}
		}
		for _, lv := range asks {
			dist := (lv.Price - mid) / mid * 10000
			if dist <= bps && dist >= 0 {
				askNotional += lv.Price * lv.Qty
			}
		}
		out = append(out, map[string]float64{
			"bps":         bps,
			"bid_notional": bidNotional,
			"ask_notional": askNotional,
		})
	}
	return out
}

func (a *App) startOrderBookSync(ctx context.Context) {
	go a.syncBinanceOrderBook(ctx, "ETHUSDT")
	go a.syncBybitOrderBook(ctx, "ETHUSDT")
	go a.syncOKXOrderBook(ctx, "ETH-USDT-SWAP")
}

func (a *App) syncBinanceOrderBook(ctx context.Context, symbol string) {
	book := a.ob.get("binance")
	if book == nil {
		return
	}
	for {
		if err := a.loadBinanceSnapshot(symbol); err != nil {
			if a.debug {
				log.Printf("binance snapshot err: %v", err)
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(2 * time.Second):
			}
			continue
		}
		if err := a.runBinanceWS(ctx, symbol); err != nil && a.debug {
			log.Printf("binance ws err: %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(2 * time.Second):
		}
		_ = book
	}
}

func (a *App) loadBinanceSnapshot(symbol string) error {
	var resp struct {
		LastUpdateID int64   `json:"lastUpdateId"`
		Bids         [][]any `json:"bids"`
		Asks         [][]any `json:"asks"`
	}
	if err := a.fetchJSON("https://api.binance.com/api/v3/depth?symbol="+symbol+"&limit=200", &resp); err != nil {
		return err
	}
	b := a.ob.get("binance")
	bids := make([][2]float64, 0, len(resp.Bids))
	for _, r := range resp.Bids {
		if len(r) < 2 {
			continue
		}
		bids = append(bids, [2]float64{parseAnyFloat(r[0]), parseAnyFloat(r[1])})
	}
	asks := make([][2]float64, 0, len(resp.Asks))
	for _, r := range resp.Asks {
		if len(r) < 2 {
			continue
		}
		asks = append(asks, [2]float64{parseAnyFloat(r[0]), parseAnyFloat(r[1])})
	}
	b.snapshot(bids, asks, resp.LastUpdateID)
	return nil
}

func (a *App) runBinanceWS(ctx context.Context, symbol string) error {
	u := url.URL{Scheme: "wss", Host: "stream.binance.com:9443", Path: "/ws/" + strings.ToLower(symbol) + "@depth@100ms"}
	return a.runBinanceWSConn(ctx, u.String(), false)
}

func (a *App) runBinanceWSConn(ctx context.Context, wsURL string, checkPU bool) error {
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		return err
	}
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(90 * time.Second))
	conn.SetPongHandler(func(string) error {
		_ = conn.SetReadDeadline(time.Now().Add(90 * time.Second))
		return nil
	})
	book := a.ob.get("binance")
	book.mu.RLock()
	snapshotID := book.LastUpdateID
	book.mu.RUnlock()
	firstApplied := false
	nextResnapshot := time.Now().Add(5 * time.Minute)
	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}
		if time.Now().After(nextResnapshot) {
			return errResnapshot
		}
		var msg map[string]any
		if err := conn.ReadJSON(&msg); err != nil {
			return err
		}
		uVal := int64(parseAnyFloat(msg["u"]))
		UVal := int64(parseAnyFloat(msg["U"]))
		puVal := int64(parseAnyFloat(msg["pu"]))
		if uVal <= 0 || UVal <= 0 {
			continue
		}
		if !firstApplied {
			if uVal < snapshotID {
				continue
			}
			if checkPU {
				if !(UVal <= snapshotID+1 && uVal >= snapshotID+1) {
					return fmt.Errorf("binance first delta not contiguous: snapshot=%d U=%d u=%d", snapshotID, UVal, uVal)
				}
			} else {
				if uVal <= snapshotID {
					continue
				}
			}
			firstApplied = true
		} else {
			book.mu.RLock()
			lastID := book.LastUpdateID
			book.mu.RUnlock()
			if checkPU && puVal > 0 && puVal != lastID {
				return fmt.Errorf("binance sequence broken: pu=%d last=%d", puVal, lastID)
			}
			if uVal <= lastID {
				continue
			}
		}
		bids := asPairs(msg["b"])
		asks := asPairs(msg["a"])
		book.applyDelta(bids, asks, uVal)
	}
}

func (a *App) syncBybitOrderBook(ctx context.Context, symbol string) {
	for {
		if err := a.loadBybitSnapshot(symbol); err != nil {
			if a.debug {
				log.Printf("bybit snapshot err: %v", err)
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(2 * time.Second):
			}
			continue
		}
		if err := a.runBybitWS(ctx, symbol); err != nil && a.debug {
			log.Printf("bybit ws err: %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(2 * time.Second):
		}
	}
}

func (a *App) loadBybitSnapshot(symbol string) error {
	var resp struct {
		RetCode int    `json:"retCode"`
		RetMsg  string `json:"retMsg"`
		Result  struct {
			B [][]any `json:"b"`
			A [][]any `json:"a"`
			U int64   `json:"u"`
		} `json:"result"`
	}
	if err := a.fetchJSON("https://api.bybit.com/v5/market/orderbook?category=linear&symbol="+symbol+"&limit=200", &resp); err != nil {
		return err
	}
	if resp.RetCode != 0 {
		return fmt.Errorf("bybit retCode=%d msg=%s", resp.RetCode, resp.RetMsg)
	}
	bids := make([][2]float64, 0, len(resp.Result.B))
	for _, r := range resp.Result.B {
		if len(r) < 2 {
			continue
		}
		bids = append(bids, [2]float64{parseAnyFloat(r[0]), parseAnyFloat(r[1])})
	}
	asks := make([][2]float64, 0, len(resp.Result.A))
	for _, r := range resp.Result.A {
		if len(r) < 2 {
			continue
		}
		asks = append(asks, [2]float64{parseAnyFloat(r[0]), parseAnyFloat(r[1])})
	}
	a.ob.get("bybit").snapshot(bids, asks, resp.Result.U)
	return nil
}

func (a *App) runBybitWS(ctx context.Context, symbol string) error {
	conn, _, err := websocket.DefaultDialer.Dial("wss://stream.bybit.com/v5/public/linear", nil)
	if err != nil {
		return err
	}
	defer conn.Close()
	sub := map[string]any{"op": "subscribe", "args": []string{"orderbook.50." + symbol}}
	if err := conn.WriteJSON(sub); err != nil {
		return err
	}
	book := a.ob.get("bybit")
	nextResnapshot := time.Now().Add(5 * time.Minute)
	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}
		if time.Now().After(nextResnapshot) {
			return errResnapshot
		}
		var msg map[string]any
		if err := conn.ReadJSON(&msg); err != nil {
			return err
		}
		topic, _ := msg["topic"].(string)
		if !strings.Contains(topic, "orderbook") {
			continue
		}
		data, ok := msg["data"].(map[string]any)
		if !ok {
			continue
		}
		bids := asPairs(data["b"])
		asks := asPairs(data["a"])
		uVal := int64(parseAnyFloat(data["u"]))
		seqVal := int64(parseAnyFloat(data["seq"]))
		typ, _ := msg["type"].(string)
		if strings.EqualFold(typ, "snapshot") {
			book.snapshot(bids, asks, uVal)
			book.mu.Lock()
			book.LastSeq = seqVal
			book.LastWSEventTS = time.Now().UnixMilli()
			book.mu.Unlock()
			continue
		}
		book.mu.RLock()
		lastU := book.LastUpdateID
		lastSeq := book.LastSeq
		book.mu.RUnlock()
		if uVal <= lastU {
			continue
		}
		if seqVal > 0 && lastSeq > 0 && seqVal != lastSeq+1 {
			return fmt.Errorf("bybit sequence broken: seq=%d last=%d", seqVal, lastSeq)
		}
		book.applyDelta(bids, asks, uVal)
		if seqVal > 0 {
			book.mu.Lock()
			book.LastSeq = seqVal
			book.mu.Unlock()
		}
	}
}

func (a *App) syncOKXOrderBook(ctx context.Context, instID string) {
	for {
		if err := a.loadOKXSnapshot(instID); err != nil {
			if a.debug {
				log.Printf("okx snapshot err: %v", err)
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(2 * time.Second):
			}
			continue
		}
		if err := a.runOKXWS(ctx, instID); err != nil && a.debug {
			log.Printf("okx ws err: %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(2 * time.Second):
		}
	}
}

func (a *App) loadOKXSnapshot(instID string) error {
	var resp struct {
		Code string `json:"code"`
		Data []struct {
			Bids [][]any `json:"bids"`
			Asks [][]any `json:"asks"`
			TS   string  `json:"ts"`
		} `json:"data"`
	}
	if err := a.fetchJSON("https://www.okx.com/api/v5/market/books?instId="+instID+"&sz=200", &resp); err != nil {
		return err
	}
	if len(resp.Data) == 0 {
		return errors.New("okx empty books")
	}
	d := resp.Data[0]
	bids := make([][2]float64, 0, len(d.Bids))
	for _, r := range d.Bids {
		if len(r) < 2 {
			continue
		}
		bids = append(bids, [2]float64{parseAnyFloat(r[0]), parseAnyFloat(r[1])})
	}
	asks := make([][2]float64, 0, len(d.Asks))
	for _, r := range d.Asks {
		if len(r) < 2 {
			continue
		}
		asks = append(asks, [2]float64{parseAnyFloat(r[0]), parseAnyFloat(r[1])})
	}
	u := int64(parseAnyFloat(d.TS))
	a.ob.get("okx").snapshot(bids, asks, u)
	return nil
}

func (a *App) runOKXWS(ctx context.Context, instID string) error {
	conn, _, err := websocket.DefaultDialer.Dial("wss://ws.okx.com:8443/ws/v5/public", nil)
	if err != nil {
		return err
	}
	defer conn.Close()
	sub := map[string]any{
		"op":   "subscribe",
		"args": []map[string]string{{"channel": "books", "instId": instID}},
	}
	if err := conn.WriteJSON(sub); err != nil {
		return err
	}
	book := a.ob.get("okx")
	nextResnapshot := time.Now().Add(5 * time.Minute)
	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}
		if time.Now().After(nextResnapshot) {
			return errResnapshot
		}
		var msg map[string]any
		if err := conn.ReadJSON(&msg); err != nil {
			return err
		}
		dataArr, ok := msg["data"].([]any)
		if !ok || len(dataArr) == 0 {
			continue
		}
		first, ok := dataArr[0].(map[string]any)
		if !ok {
			continue
		}
		bids := asPairs(first["bids"])
		asks := asPairs(first["asks"])
		ts := int64(parseAnyFloat(first["ts"]))
		action, _ := msg["action"].(string)
		if strings.EqualFold(action, "snapshot") {
			book.snapshot(bids, asks, ts)
			continue
		}
		book.applyDelta(bids, asks, ts)
	}
}

const indexHTML = `<!doctype html>
<html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>ETH Liquidation Map</title>
<style>
:root{--bg:#e8edf4;--nav:#475a74;--nav-border:#5c708a;--panel:#ffffff;--line:#dfe6ee;--left:#c2cad7;--left-head:#b7c0cf;--right:#efe9cf;--right-head:#e7dfbe;--text:#3b4c63;--accent:#d3873e}
body{margin:0;background:var(--bg);color:var(--text);font-family:Inter,system-ui,Segoe UI,Arial,sans-serif}
.nav{height:56px;background:var(--nav);border-bottom:1px solid var(--nav-border);display:flex;align-items:center;justify-content:space-between;padding:0 20px;position:sticky;top:0;z-index:10}
.nav-left,.nav-right{display:flex;align-items:center;gap:20px}.brand{font-size:18px;font-weight:700;color:#eef3f9}
.menu a{color:#d6deea;text-decoration:none;font-size:14px;margin-right:18px}.menu a.active{color:#fff;font-weight:700}
.upgrade{color:#fff;font-weight:700;text-decoration:none}.wrap{max-width:1200px;margin:0 auto;padding:22px}
.top{display:flex;justify-content:space-between;align-items:center;gap:12px;flex-wrap:wrap}
.panel{border:1px solid #c8d2df;background:var(--panel);margin:14px 0;padding:16px;border-radius:10px;box-shadow:0 1px 2px rgba(15,23,42,.03)}
#status{background:var(--nav);color:#eef3f9;padding:10px 12px;border-radius:8px;font-weight:700;text-align:center}
.btns button{margin-right:8px;background:#fff;color:var(--text);border:1px solid #b6c4d5;padding:8px 14px;border-radius:8px;cursor:pointer}
.btns button.active{background:var(--nav);color:#fff;border-color:var(--nav)} table{width:100%;border-collapse:collapse}
th,td{border-bottom:1px solid var(--line);padding:8px 10px;text-align:center}
.grid{display:grid;grid-template-columns:1fr;gap:14px}.hint{color:#67778d;font-size:12px}.mono{font-family:ui-monospace,SFMono-Regular,Menlo,Consolas,monospace}
.heat-table thead tr:first-child th{background:#d5dce6;font-size:18px}
.heat-table .col-threshold{background:#d8d9dc}
.heat-table .col-up-price,.heat-table .col-up-size{background:var(--left)}
.heat-table .col-down-price,.heat-table .col-down-size{background:var(--right)}
.heat-table thead .col-up-price,.heat-table thead .col-up-size{background:var(--left-head)}
.heat-table thead .col-down-price,.heat-table thead .col-down-size{background:var(--right-head)}
.heat-table .col-up-size,.heat-table .col-down-size{color:var(--accent);font-weight:700}
.panel h3,.top h2,.top .hint{text-align:center}
</style></head><body>
<div class="nav"><div class="nav-left"><div class="brand">ETH Liquidation Map</div><div class="menu"><a href="/" class="active">&#28165;&#31639;&#28909;&#21306;</a><a href="/map">&#28165;&#31639;&#22320;&#22270;</a><a href="/channel">&#28040;&#24687;&#36890;&#36947;</a></div></div><div class="nav-right"><a href="#" class="upgrade" onclick="return doUpgrade(event)">&#21319;&#32423;</a></div></div>
<div class="wrap"><div class="panel top"><div><h2 style="margin:0 0 6px 0;color:#111827">ETH &#28165;&#31639;&#28909;&#21306;</h2><div class="hint">&#25353; <span class="mono">0</span> / <span class="mono">1</span> / <span class="mono">7</span> / <span class="mono">3</span> &#20999;&#25442; &#26085;&#20869; / 1&#22825; / 7&#22825; / 30&#22825;</div></div><div class="btns"><button data-days="0">&#26085;&#20869;</button><button data-days="1">1&#22825;</button><button data-days="7">7&#22825;</button><button data-days="30">30&#22825;</button></div></div>
<div class="panel"><div id="status">loading...</div></div>
<div class="grid"><div class="panel"><h3>&#24066;&#22330;&#29366;&#24577;</h3><div id="market"></div></div><div class="panel"><h3>&#28165;&#31639;&#28909;&#21306;&#36895;&#25253;</h3><div id="bands"></div></div></div></div>
<script>
let currentDays=1;
function windowLabel(v){return Number(v)===0?'\u65e5\u5185':(Number(v)+'\u5929')}
async function setWindow(days){currentDays=days;await fetch('/api/window',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({days})});renderActive();load();}
function renderActive(){document.querySelectorAll('button[data-days]').forEach(b=>b.classList.toggle('active',Number(b.dataset.days)===currentDays));}
function fmtPrice(n){return Number(n).toLocaleString('zh-CN',{minimumFractionDigits:1,maximumFractionDigits:1})}
function fmtAmount(n){n=Number(n);if(!isFinite(n))return '-';const a=Math.abs(n);if(a>=1e8)return (n/1e8).toFixed(2)+'\u4ebf';if(a>=1e6)return (n/1e6).toFixed(2)+'\u767e\u4e07';return (n/1e4).toFixed(2)+'\u4e07';}
function fmt4(n){return Number(n).toFixed(8)}
function renderTable(rows,headers){if(!rows||!rows.length)return '<div class="hint">\u6682\u65e0\u6570\u636e</div>';let html='<table><thead><tr>'+headers.map(h=>'<th>'+h+'</th>').join('')+'</tr></thead><tbody>';for(const r of rows) html+='<tr>'+r.map(c=>'<td>'+c+'</td>').join('')+'</tr>';return html+'</tbody></table>';}
function renderHeatReport(d){
  const bands = d.bands || [];
  if(!bands.length) return '<div class="hint">\u6682\u65e0\u6570\u636e</div>';
  const showBands = [10,20,30,40,50,60,80,100,150];
  const bandMap = new Map(bands.map(b=>[Number(b.band), b]));
  let html = '<table class="heat-table"><thead>' +
    '<tr><th rowspan="2" class="col-threshold">\u70b9\u6570\u9608\u503c</th><th colspan="2">\u4e0a\u65b9\u7a7a\u5355</th><th colspan="2">\u4e0b\u65b9\u591a\u5355</th></tr>' +
    '<tr><th class="col-up-price">\u6e05\u7b97\u4ef7\u683c</th><th class="col-up-size">\u6e05\u7b97\u89c4\u6a21(\u4ebf)</th><th class="col-down-price">\u6e05\u7b97\u4ef7\u683c</th><th class="col-down-size">\u6e05\u7b97\u89c4\u6a21(\u4ebf)</th></tr>' +
    '</thead><tbody>';
  const toYi = n => (Number(n||0)/1e8).toFixed(1);
  for(const band of showBands){
    const b = bandMap.get(band);
    if(!b) continue;
    html += '<tr>' +
      '<td class="col-threshold">'+b.band+'\u70b9\u5185</td>' +
      '<td class="col-up-price">'+fmtPrice(b.up_price)+'</td>' +
      '<td class="col-up-size">'+toYi(b.up_notional_usd)+'</td>' +
      '<td class="col-down-price">'+fmtPrice(b.down_price)+'</td>' +
      '<td class="col-down-size">'+toYi(b.down_notional_usd)+'</td>' +
      '</tr>';
  }
  const ls = d.longest_short || [];
  const ll = d.longest_long || [];
  const sp = (ls.length>=1 && ls[0] !== '-') ? fmtPrice(ls[0]) : '-';
  const sn = (ls.length>=2) ? toYi(ls[1]) : '-';
  const lp = (ll.length>=1 && ll[0] !== '-') ? fmtPrice(ll[0]) : '-';
  const ln = (ll.length>=2) ? toYi(ll[1]) : '-';
  html += '<tr><td class="col-threshold">\u6700\u957f\u67f1</td><td class="col-up-price">'+sp+'</td><td class="col-up-size">'+sn+'</td><td class="col-down-price">'+lp+'</td><td class="col-down-size">'+ln+'</td></tr>';
  html += '</tbody></table>';
  return html;
}
async function load(){
  const r=await fetch('/api/dashboard');
  const d=await r.json();
  currentDays=(d.window_days===0||d.window_days)?d.window_days:currentDays;
  renderActive();
  document.getElementById('status').textContent='\u5f53\u524d\u4ef7: '+fmtPrice(d.current_price)+' | \u5468\u671f: '+windowLabel(d.window_days)+' | \u66f4\u65b0\u65f6\u95f4: '+new Date(d.generated_at).toLocaleString();
  document.getElementById('market').innerHTML=renderTable((d.states||[]).map(s=>[s.exchange,fmtPrice(s.mark_price),s.oi_qty?fmtAmount(s.oi_qty*s.mark_price):'-',s.oi_value_usd?fmtAmount(s.oi_value_usd):'-',s.funding_rate==null?'-':fmt4(s.funding_rate)]),['\u4ea4\u6613\u6240','\u6807\u8bb0\u4ef7','OI\u6570\u91cf','OI\u4ef7\u503cUSD','Funding']);
  document.getElementById('bands').innerHTML=renderHeatReport(d);
}
async function doUpgrade(event){if(event)event.preventDefault();const r=await fetch('/api/upgrade/pull',{method:'POST'});const d=await r.json().catch(()=>({output:'',error:'response parse failed'}));alert((d.error?('\u62c9\u53d6\u5931\u8d25: '+d.error+'\n'):'\u62c9\u53d6\u5b8c\u6210\n')+(d.output||''));return false;}
document.querySelectorAll('button[data-days]').forEach(b=>b.onclick=()=>setWindow(Number(b.dataset.days)));document.addEventListener('keydown',e=>{if(e.key==='0')setWindow(0);if(e.key==='1')setWindow(1);if(e.key==='7')setWindow(7);if(e.key==='3')setWindow(30);});setInterval(load,5000);load();
</script></body></html>`

const mapHTML = `<!doctype html>
<html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>清算地图</title>
<style>
body{margin:0;background:#f5f7fb;color:#1f2937;font-family:Inter,system-ui,Segoe UI,Arial,sans-serif}
.nav{height:56px;background:#fff;border-bottom:1px solid #d9e0ea;display:flex;align-items:center;justify-content:space-between;padding:0 20px;position:sticky;top:0;z-index:10}
.nav-left,.nav-right{display:flex;align-items:center;gap:20px}.brand{font-size:18px;font-weight:700;color:#111827}
.menu a{color:#4b5563;text-decoration:none;font-size:14px;margin-right:18px}.menu a.active{color:#111827;font-weight:700}
.upgrade{color:#111827;font-weight:700;text-decoration:none}.wrap{width:100%;max-width:none;margin:0;padding:12px}
.panel{border:1px solid #dce3ec;background:#fff;margin:14px 0;padding:16px;border-radius:10px;box-shadow:0 1px 2px rgba(15,23,42,.04)}
.row{display:flex;gap:12px;align-items:center;flex-wrap:wrap;justify-content:space-between}
.btns button,.mode button{background:#fff;color:#111827;border:1px solid #cbd5e1;padding:8px 14px;border-radius:8px;cursor:pointer}
.btns button.active,.mode button.active{background:#22c55e;color:#fff;border-color:#22c55e}
.small{font-size:12px;color:#6b7280}.legend{display:flex;gap:14px;align-items:center;flex-wrap:wrap;font-size:12px;color:#6b7280;margin-top:8px}
.tune{display:flex;gap:10px;align-items:center;flex-wrap:wrap;margin-top:8px}
.tune label{font-size:12px;color:#475569}
.tune select{height:30px;border:1px solid #cbd5e1;border-radius:6px;background:#fff;color:#111827;padding:0 8px}
.swatch{display:inline-block;width:10px;height:10px;border-radius:2px;margin-right:6px}
.meta{font-size:12px;color:#4b5563}.weights{font-size:12px;color:#334155;font-weight:700}
.chart-wrap{display:flex;gap:14px;align-items:stretch}.chart-main{flex:1;min-width:0}
canvas{width:100%;height:760px;display:block;border:1px solid #e5e7eb;border-radius:10px;background:#fff}
.merge-grid{display:grid;grid-template-columns:repeat(3,minmax(180px,1fr));gap:10px;margin-bottom:10px}
.merge-card{border:1px solid #e2e8f0;border-radius:8px;padding:10px;background:#f8fafc}.merge-label{font-size:12px;color:#64748b}.merge-val{font-size:20px;font-weight:700;color:#0f172a}
#depthChart{height:312px}
.event-wrap{margin-top:12px;border:1px solid #e2e8f0;border-radius:10px;background:#f8fafc;padding:10px}
.event-title{font-size:16px;font-weight:700;color:#0f172a;margin-bottom:8px}
.event-grid{display:grid;grid-template-columns:1fr 1fr;gap:12px}
.event-card{border:1px solid #dbe3ef;border-radius:8px;background:#fff;padding:8px}
.event-card h4{margin:0 0 6px 0;font-size:14px}
.event-card table{width:100%;border-collapse:collapse}
.event-card th,.event-card td{font-size:12px;border-top:1px solid #eef2f7;padding:4px 6px;text-align:center}
.event-card thead th{border-top:0;color:#64748b}
</style></head><body>
<div class="nav"><div class="nav-left"><div class="brand">ETH Liquidation Map</div><div class="menu"><a href="/">清算热区</a><a href="/map" class="active">清算地图</a><a href="/channel">消息通道</a></div></div><div class="nav-right"><a href="#" class="upgrade" onclick="return doUpgrade(event)">升级</a></div></div>
<div class="wrap">
  <div class="panel">
    <div class="row">
      <div>
        <h2 style="margin:0;color:#111827">清算地图</h2>
        <div class="small">基于市场数据 + 杠杆分布模型（Binance / Bybit / OKX 整合）估算清算强度</div>
      </div>
      <div class="row">
        <div class="mode">
          <button data-mode="weighted">加权</button>
          <button data-mode="merged">合并</button>
        </div>
        <div class="btns"><button data-days="0">日内</button><button data-days="1">1天</button><button data-days="7">7天</button><button data-days="30">30天</button></div>
      </div>
    </div>
    <div id="meta" class="meta"></div>
    <div id="weights" class="weights"></div>
    <div class="legend"><span><i class="swatch" style="background:#f59e0b"></i>Binance</span><span><i class="swatch" style="background:#eab308"></i>OKX</span><span><i class="swatch" style="background:#67e8f9"></i>Bybit</span></div>
    <div class="tune">
      <label>残影半衰期
        <select id="halfLifeSel">
          <option value="60">60秒</option>
          <option value="120">120秒</option>
          <option value="180">180秒</option>
        </select>
      </label>
      <label>滚动窗口
        <select id="rollWinSel">
          <option value="3">3分钟</option>
          <option value="5">5分钟</option>
          <option value="10">10分钟</option>
        </select>
      </label>
    </div>
  </div>
  <div class="panel">
    <div class="row" style="justify-content:flex-start"><h3 id="mergeTitle" style="margin:0">合并盘口（加权）</h3><div id="mergeHint" class="small">基于三家 OI 权重聚合</div></div>
    <div id="mergeStats" class="merge-grid"></div>
    <canvas id="depthChart" width="1600" height="312"></canvas>
    <div class="event-wrap">
      <div class="event-title">价格事件</div>
      <div class="event-grid">
        <div class="event-card"><h4 style="color:#16a34a">买方事件</h4><div id="bidEvents"></div></div>
        <div class="event-card"><h4 style="color:#dc2626">卖方事件</h4><div id="askEvents"></div></div>
      </div>
    </div>
  </div>
</div>
<script>
let currentDays=30,currentMode='weighted',dashboard=null,orderbook=null,depthState=null,isDraggingDepth=false,depthLastX=0,persistedEvents={bid:[],ask:[]};
const depthMemory={halfLifeMs:120000,rollingWindowMs:5*60*1000,bidGhost:{},askGhost:{},bidMax:{},askMax:{},lastUpdate:0};
const depthEvents={minPersistMs:3000,linkFactor:0.6,bidTrack:{},askTrack:{},bidList:[],askList:[]};
function fmtPrice(n){return Number(n).toLocaleString('zh-CN',{minimumFractionDigits:1,maximumFractionDigits:1})}
function fmtAmount(n){n=Number(n);if(!isFinite(n))return '-';const a=Math.abs(n);if(a>=1e8)return (n/1e8).toFixed(2)+'亿';if(a>=1e6)return (n/1e6).toFixed(2)+'百万';return (n/1e4).toFixed(2)+'万';}
function fmtQty(n){n=Number(n);if(!isFinite(n))return '-';return n.toLocaleString('zh-CN',{maximumFractionDigits:2})}
function windowLabel(v){return Number(v)===0?'日内':(Number(v)+'天')}
function syncTuneUI(){const h=document.getElementById('halfLifeSel'),r=document.getElementById('rollWinSel');if(h)h.value=String(Math.round(depthMemory.halfLifeMs/1000));if(r)r.value=String(Math.round(depthMemory.rollingWindowMs/60000));}
function applyTuneFromUI(){const h=document.getElementById('halfLifeSel'),r=document.getElementById('rollWinSel');const hs=Number(h&&h.value||120),rm=Number(r&&r.value||5);depthMemory.halfLifeMs=Math.max(1000,hs*1000);depthMemory.rollingWindowMs=Math.max(60000,rm*60000);try{localStorage.setItem('depth_tune',JSON.stringify({half_life_sec:hs,rolling_min:rm}));}catch(_){ }drawDepth();}
function loadTuneFromStorage(){try{const raw=localStorage.getItem('depth_tune');if(!raw)return;const t=JSON.parse(raw);const hs=Number(t&&t.half_life_sec||0),rm=Number(t&&t.rolling_min||0);if(hs===60||hs===120||hs===180)depthMemory.halfLifeMs=hs*1000;if(rm===3||rm===5||rm===10)depthMemory.rollingWindowMs=rm*60000;}catch(_){ }}
function priceKey(v){return Number(v).toFixed(1)}
function levelMap(levels){const out={};for(const lv of (levels||[])){const p=Number(lv.price||0),q=Number(lv.qty||0);if(!(p>0&&q>0))continue;out[priceKey(p)]=p*q;}return out;}
function decayGhost(mapObj,f){for(const k of Object.keys(mapObj)){mapObj[k]*=f;if(mapObj[k]<1e-3)delete mapObj[k];}}
function updateRollingMax(maxObj,curObj,now,windowMs){for(const k of Object.keys(maxObj)){if((now-maxObj[k].ts)>windowMs)delete maxObj[k];}for(const [k,v] of Object.entries(curObj)){const n=Number(v||0);if(!(n>0))continue;const old=maxObj[k];if(!old||n>=old.v)maxObj[k]={v:n,ts:now};}}
function wallThreshold(levelMapObj){const vals=Object.values(levelMapObj).map(v=>Number(v||0)).filter(v=>v>0).sort((a,b)=>a-b);if(!vals.length)return 1e12;const p=Math.floor(vals.length*0.85);const base=vals[Math.max(0,Math.min(vals.length-1,p))]||0;return Math.max(2.5e5,base);}
function hasLinkedNeighbor(levelMapObj,key,th){const p=Number(key),step=0.1,l=priceKey(p-step),r=priceKey(p+step),lv=Number(levelMapObj[l]||0),rv=Number(levelMapObj[r]||0);return lv>=th*depthEvents.linkFactor||rv>=th*depthEvents.linkFactor;}
function trackWalls(side,levelNow,th,now){const track=side==='bid'?depthEvents.bidTrack:depthEvents.askTrack;const live={};for(const [k,vRaw] of Object.entries(levelNow)){const v=Number(vRaw||0);if(v<th)continue;const linked=hasLinkedNeighbor(levelNow,k,th);const old=track[k];if(!old){track[k]={start:now,last:now,peak:v,duration:0,linkedEver:linked,emitted:false};}else{const gap=Math.max(0,now-Number(old.last||now));old.last=now;old.duration=(gap<=15000)?(Number(old.duration||0)+gap):0;old.peak=Math.max(Number(old.peak||0),v);old.linkedEver=Boolean(old.linkedEver||linked);}live[k]=true;}for(const k of Object.keys(track)){if(!live[k]&&now-Number(track[k].last||0)>15000)delete track[k];}}
async function postPriceEvent(side,item){try{await fetch('/api/price-events',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({side:side,price:item.price,peak:item.peak,duration_ms:item.dur_ms,event_ts:item.ts,mode:currentMode})});}catch(_){ }}
function pushPriceEvent(side,price,peak,durMs,ts){const item={price:Number(price),peak:Number(peak),dur_ms:Number(durMs),ts:Number(ts)};const list=side==='bid'?depthEvents.bidList:depthEvents.askList;list.unshift(item);if(list.length>18)list.length=18;postPriceEvent(side,item);}
function emitQualifiedEvents(side,now){const track=side==='bid'?depthEvents.bidTrack:depthEvents.askTrack;for(const [k,t] of Object.entries(track)){if(t.emitted)continue;if(Number(t.duration||0)<depthEvents.minPersistMs)continue;if(!t.linkedEver)continue;t.emitted=true;pushPriceEvent(side,Number(k),Number(t.peak||0),Number(t.duration||0),now);}}
function computeFilteredMap(side,levelNow,th){const out={};const track=side==='bid'?depthEvents.bidTrack:depthEvents.askTrack;for(const [k,vRaw] of Object.entries(levelNow)){const v=Number(vRaw||0);if(!(v>0))continue;const t=track[k];const strong=Boolean(t&&t.linkedEver&&Number(t.duration||0)>=depthEvents.minPersistMs);const linked=hasLinkedNeighbor(levelNow,k,th);const w=strong?1:(linked?0.35:0.1);out[k]=v*w;}return out;}
function updateDepthMemory(){const m=orderbook&&orderbook.merged;if(!m)return;const now=Date.now();if(depthMemory.lastUpdate===0)depthMemory.lastUpdate=now;const dt=Math.max(0,now-depthMemory.lastUpdate);depthMemory.lastUpdate=now;const decay=Math.pow(0.5,dt/depthMemory.halfLifeMs);decayGhost(depthMemory.bidGhost,decay);decayGhost(depthMemory.askGhost,decay);const bidNow=levelMap(m.bids),askNow=levelMap(m.asks),bidTh=wallThreshold(bidNow),askTh=wallThreshold(askNow);trackWalls('bid',bidNow,bidTh,now);trackWalls('ask',askNow,askTh,now);emitQualifiedEvents('bid',now);emitQualifiedEvents('ask',now);const bidFiltered=computeFilteredMap('bid',bidNow,bidTh),askFiltered=computeFilteredMap('ask',askNow,askTh);for(const [k,v] of Object.entries(bidFiltered)){depthMemory.bidGhost[k]=Math.max(Number(depthMemory.bidGhost[k]||0),Number(v||0));}for(const [k,v] of Object.entries(askFiltered)){depthMemory.askGhost[k]=Math.max(Number(depthMemory.askGhost[k]||0),Number(v||0));}updateRollingMax(depthMemory.bidMax,bidFiltered,now,depthMemory.rollingWindowMs);updateRollingMax(depthMemory.askMax,askFiltered,now,depthMemory.rollingWindowMs);}
function renderActive(){document.querySelectorAll('button[data-days]').forEach(b=>b.classList.toggle('active',Number(b.dataset.days)===currentDays));document.querySelectorAll('button[data-mode]').forEach(b=>b.classList.toggle('active',(b.dataset.mode||'')===currentMode));}
async function setWindow(days){currentDays=days;await fetch('/api/window',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({days})});renderActive();await load();}
function resetDepthMemory(){depthMemory.bidGhost={};depthMemory.askGhost={};depthMemory.bidMax={};depthMemory.askMax={};depthMemory.lastUpdate=0;depthState=null;depthEvents.bidTrack={};depthEvents.askTrack={};depthEvents.bidList=[];depthEvents.askList=[];persistedEvents={bid:[],ask:[]};}
function setMode(mode){const m=(mode==='merged')?'merged':'weighted';if(currentMode===m)return;currentMode=m;resetDepthMemory();try{localStorage.setItem('orderbook_mode',currentMode);}catch(_){ }renderActive();load();}
function loadModeFromStorage(){try{const m=localStorage.getItem('orderbook_mode');if(m==='weighted'||m==='merged')currentMode=m;}catch(_){ }}
function renderMergedPanel(){const el=document.getElementById('mergeStats');const m=orderbook&&orderbook.merged;const t=document.getElementById('mergeTitle');const h=document.getElementById('mergeHint');if(t)t.textContent=currentMode==='weighted'?'合并盘口（加权）':'合并盘口（合并）';if(h)h.textContent=currentMode==='weighted'?'基于三家 OI 权重聚合':'三家交易所盘口直接合并（不按 OI 占比）';if(!m){el.innerHTML='<div class=\"small\">暂无合并盘口数据</div>';return;}const spread=(Number(m.best_ask||0)-Number(m.best_bid||0));el.innerHTML='<div class=\"merge-card\"><div class=\"merge-label\">Best Bid</div><div class=\"merge-val\">'+fmtPrice(m.best_bid)+'</div></div>'+'<div class=\"merge-card\"><div class=\"merge-label\">Best Ask</div><div class=\"merge-val\">'+fmtPrice(m.best_ask)+'</div></div>'+'<div class=\"merge-card\"><div class=\"merge-label\">Mid / Spread</div><div class=\"merge-val\">'+fmtPrice(m.mid)+' <span style=\"font-size:12px;color:#64748b\">('+fmtPrice(spread)+')</span></div></div>';}
function fmtEventTime(ts){try{return new Date(ts).toLocaleTimeString('zh-CN',{hour12:false});}catch(_){return '-';}}
function fmtDur(ms){const s=Math.max(0,Math.round(Number(ms||0)/1000));return s+'s';}
function eventTable(list){if(!list||!list.length)return '<div class=\"small\">暂无事件</div>';let h='<table><thead><tr><th>价格</th><th>峰值</th><th>持续</th><th>出现</th></tr></thead><tbody>';for(const e of list.slice(0,8)){h+='<tr><td>'+fmtPrice(e.price)+'</td><td>'+fmtAmount(e.peak)+'</td><td>'+fmtDur(e.dur_ms)+'</td><td>'+fmtEventTime(e.ts)+'</td></tr>';}return h+'</tbody></table>';}
function renderEventTables(){const be=document.getElementById('bidEvents'),ae=document.getElementById('askEvents');if(be)be.innerHTML=eventTable(persistedEvents.bid||[]);if(ae)ae.innerHTML=eventTable(persistedEvents.ask||[]);}
function setPersistedEvents(rows){const cutoff=Date.now()-60*60*1000;const bid=[],ask=[];for(const r of (rows||[])){const ts=Number(r.event_ts||0);if(ts<cutoff)continue;const side=(r.side||'').toLowerCase();const item={price:Number(r.price||0),peak:Number(r.peak||0),dur_ms:Number(r.duration_ms||0),ts:ts};if(side==='bid')bid.push(item);if(side==='ask')ask.push(item);}persistedEvents.bid=bid.slice(0,8);persistedEvents.ask=ask.slice(0,8);}
function syncDepthCanvas(c){const rect=c.getBoundingClientRect();const dpr=window.devicePixelRatio||1;const W=Math.max(320,Math.floor(rect.width));const H=Math.max(240,Math.floor(rect.height));const rw=Math.floor(W*dpr),rh=Math.floor(H*dpr);if(c.width!==rw||c.height!==rh){c.width=rw;c.height=rh;}const x=c.getContext('2d');x.setTransform(dpr,0,0,dpr,0,0);return{x,W,H};}
function clampDepthView(minP,maxP){if(!depthState||!(depthState.fullMax>depthState.fullMin))return[minP,maxP];const fullMin=depthState.fullMin,fullMax=depthState.fullMax,fullSpan=Math.max(1e-6,fullMax-fullMin);const minSpan=Math.max(2,fullSpan*0.05);let span=Math.max(minSpan,maxP-minP);span=Math.min(fullSpan,span);let vMin=minP,vMax=vMin+span;if(vMin<fullMin){vMin=fullMin;vMax=vMin+span;}if(vMax>fullMax){vMax=fullMax;vMin=vMax-span;}return[vMin,vMax];}
function initDepthView(allPrices,cur){const dataMin=Math.min(...allPrices,cur),dataMax=Math.max(...allPrices,cur);if(!depthState||!(depthState.fullMax>depthState.fullMin)){const half=Math.max(1,Math.max(cur-dataMin,dataMax-cur));let vMin=cur-half,vMax=cur+half;depthState={fullMin:dataMin,fullMax:dataMax,viewMin:vMin,viewMax:vMax,padL:64,padR:24,padT:20,padB:44,W:0,H:0};[depthState.viewMin,depthState.viewMax]=clampDepthView(depthState.viewMin,depthState.viewMax);return;}depthState.fullMin=dataMin;depthState.fullMax=dataMax;const span=(depthState.viewMax>depthState.viewMin)?(depthState.viewMax-depthState.viewMin):(dataMax-dataMin);let vMin=cur-span/2,vMax=cur+span/2;[vMin,vMax]=clampDepthView(vMin,vMax);depthState.viewMin=vMin;depthState.viewMax=vMax;}
function drawDepth(){const c=document.getElementById('depthChart');const v=syncDepthCanvas(c),x=v.x,W=v.W,H=v.H;x.clearRect(0,0,W,H);x.fillStyle='#fff';x.fillRect(0,0,W,H);const m=orderbook&&orderbook.merged;const bids=(m&&m.bids)||[];const asks=(m&&m.asks)||[];const cur=Number((m&&m.mid)||0)||Number((dashboard&&dashboard.current_price)||0);if(!(cur>0)||(!bids.length&&!asks.length)){x.fillStyle='#6b7280';x.font='14px sans-serif';x.fillText('暂无盘口柱状数据',20,24);return;}const all=bids.concat(asks).map(v=>({p:Number(v.price||0),n:Number(v.qty||0)*Number(v.price||0)})).filter(v=>v.p>0&&v.n>0);if(!all.length){x.fillStyle='#6b7280';x.font='14px sans-serif';x.fillText('暂无盘口柱状数据',20,24);return;}initDepthView(all.map(v=>v.p),cur);const padL=64,padR=24,padT=20,padB=44,pw=W-padL-padR,ph=H-padT-padB,by=padT+ph;depthState.padL=padL;depthState.padR=padR;depthState.padT=padT;depthState.padB=padB;depthState.W=W;depthState.H=H;const minP=depthState.viewMin,maxP=depthState.viewMax,span=Math.max(1e-6,maxP-minP);const bidNow=levelMap(bids),askNow=levelMap(asks);const visible=all.filter(v=>v.p>=minP&&v.p<=maxP);const ghostVals=[];for(const [k,vv] of Object.entries(depthMemory.bidGhost)){const p=Number(k);if(p>=minP&&p<=maxP&&vv>0)ghostVals.push(vv);}for(const [k,vv] of Object.entries(depthMemory.askGhost)){const p=Number(k);if(p>=minP&&p<=maxP&&vv>0)ghostVals.push(vv);}const maxVals=[];for(const [k,o] of Object.entries(depthMemory.bidMax)){const p=Number(k);if(p>=minP&&p<=maxP&&o&&o.v>0)maxVals.push(o.v);}for(const [k,o] of Object.entries(depthMemory.askMax)){const p=Number(k);if(p>=minP&&p<=maxP&&o&&o.v>0)maxVals.push(o.v);}const maxN=Math.max(1,...(visible.length?visible.map(v=>v.n):all.map(v=>v.n)),...(ghostVals.length?ghostVals:[1]),...(maxVals.length?maxVals:[1]));const sx=v=>padL+((v-minP)/span)*pw,sy=v=>by-(v/maxN)*ph;x.strokeStyle='#e5e7eb';x.lineWidth=1;x.font='12px sans-serif';for(let i=0;i<=4;i++){const y=padT+ph*(i/4),val=maxN*(1-i/4);x.beginPath();x.moveTo(padL,y);x.lineTo(W-padR,y);x.stroke();x.fillStyle='#475569';x.fillText(fmtAmount(val),6,y+4);}const tickCount=10;for(let i=0;i<=tickCount;i++){const p=minP+span*(i/tickCount),px=sx(p);x.strokeStyle='#e5e7eb';x.beginPath();x.moveTo(px,by);x.lineTo(px,by+4);x.stroke();x.fillStyle='#64748b';x.fillText(fmtPrice(p),px-16,by+18);}if(cur>=minP&&cur<=maxP){const cp=sx(cur);x.strokeStyle='#dc2626';x.setLineDash([6,4]);x.beginPath();x.moveTo(cp,padT);x.lineTo(cp,by);x.stroke();x.setLineDash([]);x.fillStyle='#111827';x.fillText('当前价:'+fmtPrice(cur),Math.max(padL,Math.min(cp-34,W-150)),padT-4);}const barCount=Math.max(1,visible.length);const barW=Math.max(1,Math.min(12,pw/Math.max(50,barCount)));for(const [k,n] of Object.entries(depthMemory.bidGhost)){const p=Number(k);if(!(p>=minP&&p<=maxP&&n>0))continue;const px=sx(p),y=sy(n);x.fillStyle='rgba(22,163,74,0.18)';x.fillRect(px-barW/2,y,barW,by-y);}for(const [k,n] of Object.entries(depthMemory.askGhost)){const p=Number(k);if(!(p>=minP&&p<=maxP&&n>0))continue;const px=sx(p),y=sy(n);x.fillStyle='rgba(220,38,38,0.16)';x.fillRect(px-barW/2,y,barW,by-y);}for(const [k,o] of Object.entries(depthMemory.bidMax)){const p=Number(k),n=Number(o&&o.v||0);if(!(p>=minP&&p<=maxP&&n>0))continue;const px=sx(p),y=sy(n);x.strokeStyle='rgba(22,163,74,0.85)';x.lineWidth=1.3;x.beginPath();x.moveTo(px-barW/2,y);x.lineTo(px+barW/2,y);x.stroke();}for(const [k,o] of Object.entries(depthMemory.askMax)){const p=Number(k),n=Number(o&&o.v||0);if(!(p>=minP&&p<=maxP&&n>0))continue;const px=sx(p),y=sy(n);x.strokeStyle='rgba(220,38,38,0.85)';x.lineWidth=1.3;x.beginPath();x.moveTo(px-barW/2,y);x.lineTo(px+barW/2,y);x.stroke();}x.lineWidth=1;for(const b of bids){const p=Number(b.price||0),n=Number(b.qty||0)*p;if(!(p>=minP&&p<=maxP&&n>0))continue;const px=sx(p),y=sy(n);x.fillStyle='rgba(22,163,74,0.75)';x.fillRect(px-barW/2,y,barW,by-y);}for(const a of asks){const p=Number(a.price||0),n=Number(a.qty||0)*p;if(!(p>=minP&&p<=maxP&&n>0))continue;const px=sx(p),y=sy(n);x.fillStyle='rgba(220,38,38,0.72)';x.fillRect(px-barW/2,y,barW,by-y);}x.fillStyle='#16a34a';x.fillText('Bid 柱（左侧）',padL,14);x.fillStyle='#dc2626';x.fillText('Ask 柱（右侧）',padL+100,14);x.fillStyle='#64748b';x.fillText('浅色:残影(半衰期'+Math.round(depthMemory.halfLifeMs/1000)+'s)  细线:'+Math.round(depthMemory.rollingWindowMs/60000)+'分钟峰值',padL+220,14);x.fillText('X轴：ETH价格（滚轮缩放 / 拖动平移）',W-190,14);}
function bindDepthInteraction(){const c=document.getElementById('depthChart');if(!c||c.dataset.bound==='1')return;c.dataset.bound='1';c.addEventListener('wheel',e=>{if(!depthState||!(depthState.viewMax>depthState.viewMin))return;e.preventDefault();const rect=c.getBoundingClientRect();const xPos=e.clientX-rect.left;const s=depthState,pw=s.W-s.padL-s.padR;if(pw<=0)return;const ratio=Math.max(0,Math.min(1,(xPos-s.padL)/pw));const focus=s.viewMin+(s.viewMax-s.viewMin)*ratio;const factor=e.deltaY<0?0.88:1.14;let vMin=focus-(focus-s.viewMin)*factor,vMax=vMin+(s.viewMax-s.viewMin)*factor;[vMin,vMax]=clampDepthView(vMin,vMax);s.viewMin=vMin;s.viewMax=vMax;drawDepth();},{passive:false});c.addEventListener('mousedown',e=>{if(e.button!==0||!depthState)return;isDraggingDepth=true;depthLastX=e.clientX;});window.addEventListener('mousemove',e=>{if(!isDraggingDepth||!depthState)return;const s=depthState,pw=s.W-s.padL-s.padR;if(pw<=0)return;const dx=e.clientX-depthLastX;depthLastX=e.clientX;const dp=(dx/pw)*(s.viewMax-s.viewMin);let vMin=s.viewMin-dp,vMax=s.viewMax-dp;[vMin,vMax]=clampDepthView(vMin,vMax);s.viewMin=vMin;s.viewMax=vMax;drawDepth();});window.addEventListener('mouseup',()=>{isDraggingDepth=false;});window.addEventListener('mouseleave',()=>{isDraggingDepth=false;});window.addEventListener('resize',()=>drawDepth());}
function buildShares(states){const order=['binance','okx','bybit'];const out={};let total=0;for(const ex of order){const s=(states||[]).find(v=>(v.exchange||'').toLowerCase()===ex);const oi=Number((s&&s.oi_value_usd)||0);out[ex]=oi;total+=oi;}if(total<=0){for(const ex of order)out[ex]=1/order.length;}else{for(const ex of order)out[ex]/=total;}return out;}
function renderMeta(){if(!dashboard)return;const t=new Date(dashboard.generated_at).toLocaleString();document.getElementById('meta').textContent='口径：估算清算强度（市场数据 + 杠杆分布模型） | 数据源：Binance / Bybit / OKX | 周期：'+windowLabel(dashboard.window_days)+' | 更新时间：'+t+' | 当前价来源：盘口mid';const s=buildShares(dashboard.states);document.getElementById('weights').textContent=currentMode==='weighted'?('三家 OI 占比：Binance '+(s.binance*100).toFixed(1)+'% | Bybit '+(s.bybit*100).toFixed(1)+'% | OKX '+(s.okx*100).toFixed(1)+'%'):'模式：合并（不使用 OI 占比）；三家盘口数量直接求和显示';}
async function load(){const [r1,r2,r3]=await Promise.all([fetch('/api/dashboard'),fetch('/api/orderbook?limit=60&mode='+encodeURIComponent(currentMode)),fetch('/api/price-events')]);dashboard=await r1.json();orderbook=await r2.json();const evts=await r3.json().catch(()=>[]);updateDepthMemory();setPersistedEvents(evts);currentDays=(dashboard.window_days===0||dashboard.window_days)?dashboard.window_days:currentDays;renderActive();renderMeta();renderMergedPanel();drawDepth();renderEventTables();}
async function doUpgrade(event){if(event)event.preventDefault();const r=await fetch('/api/upgrade/pull',{method:'POST'});const d=await r.json().catch(()=>({output:'',error:'response parse failed'}));alert((d.error?('拉取失败: '+d.error+'\n'):'拉取完成\n')+(d.output||''));return false;}
document.querySelectorAll('button[data-days]').forEach(b=>b.onclick=()=>setWindow(Number(b.dataset.days)));
document.querySelectorAll('button[data-mode]').forEach(b=>b.onclick=()=>setMode((b.dataset.mode||'weighted')));
loadModeFromStorage();
loadTuneFromStorage();
syncTuneUI();
const hs=document.getElementById('halfLifeSel'),rw=document.getElementById('rollWinSel');
if(hs)hs.onchange=applyTuneFromUI;
if(rw)rw.onchange=applyTuneFromUI;
bindDepthInteraction();
setInterval(load,5000);load();
</script></body></html>`

const channelHTML = `<!doctype html>
<html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>&#28040;&#24687;&#36890;&#36947;</title>
<style>body{margin:0;background:#f5f7fb;color:#1f2937;font-family:Inter,system-ui,Segoe UI,Arial,sans-serif}.nav{height:56px;background:#fff;border-bottom:1px solid #d9e0ea;display:flex;align-items:center;justify-content:space-between;padding:0 20px;position:sticky;top:0;z-index:10}.nav-left,.nav-right{display:flex;align-items:center;gap:20px}.brand{font-size:18px;font-weight:700;color:#111827}.menu a{color:#4b5563;text-decoration:none;font-size:14px;margin-right:18px}.menu a.active{color:#111827;font-weight:700}.upgrade{color:#111827;font-weight:700;text-decoration:none}.wrap{max-width:900px;margin:0 auto;padding:22px}.panel{border:1px solid #dce3ec;background:#fff;margin:14px 0;padding:16px;border-radius:10px;box-shadow:0 1px 2px rgba(15,23,42,.04)}.small{font-size:12px;color:#6b7280}button.primary{background:#22c55e;color:#fff;border:0;padding:10px 16px;border-radius:8px;cursor:pointer}button.secondary{background:#fff;color:#111827;border:1px solid #cbd5e1;padding:10px 16px;border-radius:8px;cursor:pointer}input{width:100%;box-sizing:border-box;padding:10px;border:1px solid #cbd5e1;border-radius:8px;background:#fff;color:#111827;margin-top:6px}.row{display:flex;gap:10px;flex-wrap:wrap;align-items:center}</style></head>
<body><div class="nav"><div class="nav-left"><div class="brand">ETH Liquidation Map</div><div class="menu"><a href="/">&#28165;&#31639;&#28909;&#21306;</a><a href="/map">&#28165;&#31639;&#22320;&#22270;</a><a href="/channel" class="active">&#28040;&#24687;&#36890;&#36947;</a></div></div><div class="nav-right"><a href="#" class="upgrade" onclick="return doUpgrade(event)">&#21319;&#32423;</a></div></div>
<div class="wrap"><div class="panel"><h2 style="margin-top:0">Telegram &#28040;&#24687;&#36890;&#36947;</h2><div class="small">保存后会自动脱敏显示，仅保留前 4 位和后 4 位。点击“测试”可发送 Telegram 测试消息。</div><div style="margin-top:14px"><label>Telegram Bot Token</label><input id="token" autocomplete="off" placeholder="123456:ABC..."></div><div style="margin-top:14px"><label>Telegram Channel / Chat ID</label><input id="channel" autocomplete="off" placeholder="@mychannel 或 -100123456789"></div><div style="margin-top:16px" class="row"><button class="primary" onclick="save()">保存</button><button class="secondary" onclick="testTelegram()">测试</button><span id="msg" class="small" style="margin-left:10px"></span></div></div></div>
<script>
let rawToken={{printf "%q" .TelegramBotToken}},rawChannel={{printf "%q" .TelegramChannel}},rawInterval={{.NotifyIntervalMin}},tokenDirty=false,channelDirty=false;
function maskSensitive(v){v=(v||'').trim();if(!v)return '';if(v.length<=8)return v;return v.slice(0,4)+'*'.repeat(v.length-8)+v.slice(-4);}function syncInputs(){const t=document.getElementById('token'),c=document.getElementById('channel'),n=document.getElementById('notify-interval');if(!tokenDirty)t.value=rawToken?maskSensitive(rawToken):'';if(!channelDirty)c.value=rawChannel?maskSensitive(rawChannel):'';n.value=rawInterval||15;}function currentValue(i,r,d){const v=(i.value||'').trim();if(!d&&r&&v===maskSensitive(r))return r;return v;}document.getElementById('token').addEventListener('input',()=>{tokenDirty=true});document.getElementById('channel').addEventListener('input',()=>{channelDirty=true});syncInputs();
async function doUpgrade(event){if(event)event.preventDefault();const r=await fetch('/api/upgrade/pull',{method:'POST'});const d=await r.json().catch(()=>({output:'',error:'response parse failed'}));alert((d.error?('\u62c9\u53d6\u5931\u8d25: '+d.error+'\n'):'\u62c9\u53d6\u5b8c\u6210\n')+(d.output||''));return false;}
async function save(){const body={telegram_bot_token:currentValue(document.getElementById('token'),rawToken,tokenDirty),telegram_channel:currentValue(document.getElementById('channel'),rawChannel,channelDirty)};const r=await fetch('/api/settings',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify(body)});if(r.ok){rawToken=body.telegram_bot_token;rawChannel=body.telegram_channel;tokenDirty=false;channelDirty=false;syncInputs();document.getElementById('msg').textContent='保存成功';}else{document.getElementById('msg').textContent='保存失败';}}
async function testTelegram(){const msg=document.getElementById('msg');msg.textContent='正在发送测试消息...';const r=await fetch('/api/channel/test',{method:'POST'});msg.textContent=r.ok?'测试消息已发送':('测试失败：'+await r.text());}
</script></body></html>`
