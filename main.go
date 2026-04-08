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
	"os/signal"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	_ "modernc.org/sqlite"
)

const (
	defaultDBPath      = "liqmap.db"
	defaultSymbol      = "ETHUSDT"
	defaultServerAddr  = ":8888"
	defaultWindowDays  = 1
	windowIntraday     = 0
	defaultLookbackMin = 360
	defaultBucketMin   = 5
	defaultPriceStep   = 5.0
	defaultPriceRange  = 400.0
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

type ImbalanceStat struct {
	Band            int     `json:"band"`
	UpNotionalUSD   float64 `json:"up_notional_usd"`
	DownNotionalUSD float64 `json:"down_notional_usd"`
	Ratio           float64 `json:"ratio"`
	Verdict         string  `json:"verdict"`
}

type DensityLayer struct {
	Label           string  `json:"label"`
	UpNotionalUSD   float64 `json:"up_notional_usd"`
	DownNotionalUSD float64 `json:"down_notional_usd"`
}

type ChangeTracking struct {
	Up20DeltaUSD    float64 `json:"up20_delta_usd"`
	Down20DeltaUSD  float64 `json:"down20_delta_usd"`
	LongestDeltaUSD float64 `json:"longest_delta_usd"`
	FundingDelta    float64 `json:"funding_delta"`
}

type CoreZone struct {
	UpPrice            float64 `json:"up_price"`
	UpNotionalUSD      float64 `json:"up_notional_usd"`
	DownPrice          float64 `json:"down_price"`
	DownNotionalUSD    float64 `json:"down_notional_usd"`
	NearestSide        string  `json:"nearest_side"`
	NearestDistance    float64 `json:"nearest_distance"`
	NearestStrongPrice float64 `json:"nearest_strong_price"`
}

type ExchangeContribution struct {
	Exchange    string  `json:"exchange"`
	NotionalUSD float64 `json:"notional_usd"`
	Share       float64 `json:"share"`
}

type AlertSummary struct {
	Level          string  `json:"level"`
	RecentExchange string  `json:"recent_exchange"`
	RecentSide     string  `json:"recent_side"`
	RecentPrice    float64 `json:"recent_price"`
	Recent1mUSD    float64 `json:"recent_1m_usd"`
	Suggestion     string  `json:"suggestion"`
}

type TopConclusion struct {
	ShortBias   string  `json:"short_bias"`
	Bias20Delta float64 `json:"bias20_delta"`
	Bias50Label string  `json:"bias50_label"`
}

type MarketSummary struct {
	BinanceOIUSD float64 `json:"binance_oi_usd"`
	OKXOIUSD     float64 `json:"okx_oi_usd"`
	BybitOIUSD   float64 `json:"bybit_oi_usd"`
	AvgFunding   float64 `json:"avg_funding"`
}

type HeatZoneAnalytics struct {
	Top              TopConclusion          `json:"top"`
	Market           MarketSummary          `json:"market"`
	ImbalanceStats   []ImbalanceStat        `json:"imbalance_stats"`
	DensityLayers    []DensityLayer         `json:"density_layers"`
	ChangeTracking   ChangeTracking         `json:"change_tracking"`
	CoreZone         CoreZone               `json:"core_zone"`
	ExchangeContrib  []ExchangeContribution `json:"exchange_contrib"`
	DominantExchange string                 `json:"dominant_exchange"`
	Alert            AlertSummary           `json:"alert"`
}

type Dashboard struct {
	Symbol       string            `json:"symbol"`
	WindowDays   int               `json:"window_days"`
	GeneratedAt  int64             `json:"generated_at"`
	States       []MarketState     `json:"states"`
	CurrentPrice float64           `json:"current_price"`
	Bands        []BandRow         `json:"bands"`
	LongestShort []any             `json:"longest_short"`
	LongestLong  []any             `json:"longest_long"`
	Events       []EventRow        `json:"events"`
	Analytics    HeatZoneAnalytics `json:"analytics"`
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
	lastNotify int64
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
		`CREATE TABLE IF NOT EXISTS oi_snapshots (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			exchange TEXT NOT NULL,
			symbol TEXT NOT NULL,
			mark_price REAL NOT NULL,
			oi_value_usd REAL NOT NULL,
			funding_rate REAL,
			updated_ts INTEGER NOT NULL
		);`,
		`CREATE INDEX IF NOT EXISTS idx_oi_snapshots_symbol_ts ON oi_snapshots(symbol, updated_ts);`,
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
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_liquidation_events_uniq
			ON liquidation_events(exchange, symbol, side, price, qty, event_ts);`,
		`CREATE INDEX IF NOT EXISTS idx_liquidation_events_symbol_ts ON liquidation_events(symbol, event_ts);`,
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
	rootCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	app.startCollector(rootCtx)
	app.startOrderBookSync(rootCtx)
	app.startLiquidationSync(rootCtx)
	app.startTelegramNotifier(rootCtx)
	mux := http.NewServeMux()
	mux.HandleFunc("/", app.handleIndex)
	mux.HandleFunc("/monitor", app.handleMonitor)
	mux.HandleFunc("/map", app.handleMap)
	mux.HandleFunc("/config", app.handleConfig)
	mux.HandleFunc("/liquidations", app.handleLiquidations)
	mux.HandleFunc("/bubbles", app.handleBubbles)
	mux.HandleFunc("/channel", app.handleChannel)
	mux.HandleFunc("/api/dashboard", app.handleDashboard)
	mux.HandleFunc("/api/model/liquidation-map", app.handleModelLiquidationMap)
	mux.HandleFunc("/api/model-config", app.handleModelConfig)
	mux.HandleFunc("/api/liquidations", app.handleLiquidationsAPI)
	mux.HandleFunc("/api/klines", app.handleKlinesAPI)
	mux.HandleFunc("/api/orderbook", app.handleOrderBook)
	mux.HandleFunc("/api/coinglass/map", app.handleCoinGlassMap)
	mux.HandleFunc("/api/window", app.handleWindow)
	mux.HandleFunc("/api/settings", app.handleSettings)
	mux.HandleFunc("/api/channel/test", app.handleChannelTest)
	mux.HandleFunc("/api/upgrade/pull", app.handleUpgradePull)
	mux.HandleFunc("/api/upgrade/progress", app.handleUpgradeProgress)
	mux.HandleFunc("/api/version", app.handleVersion)
	mux.HandleFunc("/api/price-events", app.handlePriceEvents)

	srv := &http.Server{Addr: defaultServerAddr, Handler: mux}
	go func() {
		<-rootCtx.Done()
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutCtx)
	}()
	log.Printf("dashboard listening on http://127.0.0.1%s", defaultServerAddr)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
	log.Printf("normal exit")
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

func (a *App) handleMonitor(w http.ResponseWriter, r *http.Request) {
	if a.debug {
		log.Printf("%s %s", r.Method, r.URL.Path)
	}
	tpl := template.Must(template.New("monitor").Parse(monitorHTML))
	_ = tpl.Execute(w, nil)
}

func (a *App) handleConfig(w http.ResponseWriter, r *http.Request) {
	if a.debug {
		log.Printf("%s %s", r.Method, r.URL.Path)
	}
	tpl := template.Must(template.New("config").Parse(configHTML))
	_ = tpl.Execute(w, a.loadModelConfig())
}

func (a *App) handleLiquidations(w http.ResponseWriter, r *http.Request) {
	if a.debug {
		log.Printf("%s %s", r.Method, r.URL.Path)
	}
	tpl := template.Must(template.New("liquidations").Parse(liquidationsHTML))
	_ = tpl.Execute(w, nil)
}

func (a *App) handleBubbles(w http.ResponseWriter, r *http.Request) {
	if a.debug {
		log.Printf("%s %s", r.Method, r.URL.Path)
	}
	tpl := template.Must(template.New("bubbles").Parse(bubblesHTML))
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

func (a *App) handleModelLiquidationMap(w http.ResponseWriter, r *http.Request) {
	if a.debug {
		log.Printf("%s %s", r.Method, r.URL.Path)
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	cfg := a.loadModelConfig()
	lookbackMin := cfg.LookbackMin
	if raw := strings.TrimSpace(r.URL.Query().Get("lookback_min")); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n >= 60 && n <= 1440 {
			lookbackMin = n
		}
	}
	bucketMin := cfg.BucketMin
	if raw := strings.TrimSpace(r.URL.Query().Get("bucket_min")); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n >= 1 && n <= 30 {
			bucketMin = n
		}
	}
	priceStep := cfg.PriceStep
	if raw := strings.TrimSpace(r.URL.Query().Get("price_step")); raw != "" {
		if v, err := strconv.ParseFloat(raw, 64); err == nil && v >= 1 && v <= 50 {
			priceStep = v
		}
	}
	priceRange := cfg.PriceRange
	if raw := strings.TrimSpace(r.URL.Query().Get("price_range")); raw != "" {
		if v, err := strconv.ParseFloat(raw, 64); err == nil && v >= 100 && v <= 1000 {
			priceRange = v
		}
	}
	resp, err := a.buildModelLiquidationMap(defaultSymbol, lookbackMin, bucketMin, priceStep, priceRange, cfg)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	_ = json.NewEncoder(w).Encode(resp)
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

func (a *App) getSettingInt(key string, fallback int) int {
	raw := strings.TrimSpace(a.getSetting(key))
	if raw == "" {
		return fallback
	}
	if v, err := strconv.Atoi(raw); err == nil {
		return v
	}
	return fallback
}

func (a *App) getSettingFloat(key string, fallback float64) float64 {
	raw := strings.TrimSpace(a.getSetting(key))
	if raw == "" {
		return fallback
	}
	if v, err := strconv.ParseFloat(raw, 64); err == nil {
		return v
	}
	return fallback
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

type ModelConfig struct {
	LookbackMin   int
	BucketMin     int
	PriceStep     float64
	PriceRange    float64
	LeverageCSV   string
	WeightCSV     string
	MaintMargin   float64
	FundingScale  float64
	DecayK        float64
	NeighborShare float64
}

func (a *App) loadModelConfig() ModelConfig {
	cfg := ModelConfig{
		LookbackMin:   a.getSettingInt("model_lookback_min", defaultLookbackMin),
		BucketMin:     a.getSettingInt("model_bucket_min", defaultBucketMin),
		PriceStep:     a.getSettingFloat("model_price_step", defaultPriceStep),
		PriceRange:    a.getSettingFloat("model_price_range", defaultPriceRange),
		LeverageCSV:   strings.TrimSpace(a.getSetting("model_leverage_levels")),
		WeightCSV:     strings.TrimSpace(a.getSetting("model_leverage_weights")),
		MaintMargin:   a.getSettingFloat("model_mm", 0.005),
		FundingScale:  a.getSettingFloat("model_funding_scale", 7000),
		DecayK:        a.getSettingFloat("model_decay_k", 2.2),
		NeighborShare: a.getSettingFloat("model_neighbor_share", 0.28),
	}
	if cfg.LeverageCSV == "" {
		cfg.LeverageCSV = "20,50,100"
	}
	if cfg.WeightCSV == "" {
		cfg.WeightCSV = "0.30,0.40,0.30"
	}
	return cfg
}

func parseCSVFloats(raw string) []float64 {
	parts := strings.Split(raw, ",")
	out := make([]float64, 0, len(parts))
	for _, p := range parts {
		v, err := strconv.ParseFloat(strings.TrimSpace(p), 64)
		if err != nil || v <= 0 {
			continue
		}
		out = append(out, v)
	}
	return out
}
func (a *App) handleModelConfig(w http.ResponseWriter, r *http.Request) {
	if a.debug {
		log.Printf("%s %s", r.Method, r.URL.Path)
	}
	switch r.Method {
	case http.MethodGet:
		_ = json.NewEncoder(w).Encode(a.loadModelConfig())
	case http.MethodPost:
		var req ModelConfig
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if req.LookbackMin < 60 || req.LookbackMin > 1440 {
			req.LookbackMin = defaultLookbackMin
		}
		if req.BucketMin < 1 || req.BucketMin > 30 {
			req.BucketMin = defaultBucketMin
		}
		if req.PriceStep < 1 || req.PriceStep > 50 {
			req.PriceStep = defaultPriceStep
		}
		if req.PriceRange < 100 || req.PriceRange > 1000 {
			req.PriceRange = defaultPriceRange
		}
		if req.MaintMargin <= 0 || req.MaintMargin > 0.02 {
			req.MaintMargin = 0.005
		}
		if req.FundingScale < 1000 || req.FundingScale > 20000 {
			req.FundingScale = 7000
		}
		if req.DecayK <= 0.1 || req.DecayK > 10 {
			req.DecayK = 2.2
		}
		if req.NeighborShare < 0 || req.NeighborShare > 1 {
			req.NeighborShare = 0.28
		}
		if len(parseCSVFloats(req.LeverageCSV)) == 0 {
			req.LeverageCSV = "20,50,100"
		}
		if len(parseCSVFloats(req.WeightCSV)) == 0 {
			req.WeightCSV = "0.30,0.40,0.30"
		}
		_ = a.setSetting("model_lookback_min", strconv.Itoa(req.LookbackMin))
		_ = a.setSetting("model_bucket_min", strconv.Itoa(req.BucketMin))
		_ = a.setSetting("model_price_step", fmt.Sprintf("%.4f", req.PriceStep))
		_ = a.setSetting("model_price_range", fmt.Sprintf("%.2f", req.PriceRange))
		_ = a.setSetting("model_leverage_levels", strings.TrimSpace(req.LeverageCSV))
		_ = a.setSetting("model_leverage_weights", strings.TrimSpace(req.WeightCSV))
		_ = a.setSetting("model_mm", fmt.Sprintf("%.6f", req.MaintMargin))
		_ = a.setSetting("model_funding_scale", fmt.Sprintf("%.2f", req.FundingScale))
		_ = a.setSetting("model_decay_k", fmt.Sprintf("%.4f", req.DecayK))
		_ = a.setSetting("model_neighbor_share", fmt.Sprintf("%.4f", req.NeighborShare))
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
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
	// Run upgrade steps in background without creating a transient systemd unit,
	// because users expect to only see liqmap.service (not liqmap-upgrade.service).
	upgradeCmd := exec.Command("bash", "-lc", "rm -f /tmp/liqmap-upgrade.exit /tmp/liqmap-upgrade.pid; : >/tmp/liqmap-upgrade.log; (cd /opt/MultipleExchangeLiquidationMap && echo [git fetch] && echo 'root@jiansu-openvpn-japan:/opt/MultipleExchangeLiquidationMap# git fetch --all --prune' && git fetch --all --prune && echo [git reset] && echo 'root@jiansu-openvpn-japan:/opt/MultipleExchangeLiquidationMap# git reset --hard origin/golang' && git reset --hard origin/golang && echo [go build] && echo 'root@jiansu-openvpn-japan:/opt/MultipleExchangeLiquidationMap# go build -o multipleexchangeliquidationmap.exe .' && go build -o multipleexchangeliquidationmap.exe . && echo [restart service] && echo 'root@jiansu-openvpn-japan:/opt/MultipleExchangeLiquidationMap# systemctl restart liqmap.service' && systemctl restart liqmap.service && echo [service status] && echo 'root@jiansu-openvpn-japan:/opt/MultipleExchangeLiquidationMap# systemctl status liqmap.service --no-pager' && systemctl status liqmap.service --no-pager; ec=$?; echo $ec >/tmp/liqmap-upgrade.exit) >>/tmp/liqmap-upgrade.log 2>&1 & echo $! >/tmp/liqmap-upgrade.pid")
	out, err := upgradeCmd.CombinedOutput()
	if err != nil {
		w.WriteHeader(http.StatusBadGateway)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"error":  err.Error(),
			"output": string(out),
		})
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{
		"output": "upgrade triggered in background; will restart liqmap.service",
	})
}

func (a *App) handleUpgradeProgress(w http.ResponseWriter, r *http.Request) {
	if a.debug {
		log.Printf("%s %s", r.Method, r.URL.Path)
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	logOut, _ := exec.Command("bash", "-lc", "tail -n 260 /tmp/liqmap-upgrade.log 2>/dev/null || true").CombinedOutput()
	runningOut, _ := exec.Command("bash", "-lc", "pid=$(cat /tmp/liqmap-upgrade.pid 2>/dev/null || true); if [ -n \"$pid\" ] && kill -0 \"$pid\" 2>/dev/null; then echo 1; else echo 0; fi").CombinedOutput()
	exitOut, _ := exec.Command("bash", "-lc", "cat /tmp/liqmap-upgrade.exit 2>/dev/null || true").CombinedOutput()
	running := strings.TrimSpace(string(runningOut)) == "1"
	exitCode := strings.TrimSpace(string(exitOut))
	done := !running && exitCode != ""
	_ = json.NewEncoder(w).Encode(map[string]any{
		"running":   running,
		"done":      done,
		"exit_code": exitCode,
		"log":       string(logOut),
	})
}

func (a *App) handleVersion(w http.ResponseWriter, r *http.Request) {
	if a.debug {
		log.Printf("%s %s", r.Method, r.URL.Path)
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	branchOut, _ := exec.Command("bash", "-lc", "git rev-parse --abbrev-ref HEAD 2>/dev/null || true").CombinedOutput()
	commitOut, _ := exec.Command("bash", "-lc", "git rev-parse --short HEAD 2>/dev/null || true").CombinedOutput()
	timeOut, _ := exec.Command("bash", "-lc", "git show -s --format=%ci HEAD 2>/dev/null || true").CombinedOutput()
	_ = json.NewEncoder(w).Encode(map[string]any{
		"branch":      strings.TrimSpace(string(branchOut)),
		"commit_id":   strings.TrimSpace(string(commitOut)),
		"commit_time": strings.TrimSpace(string(timeOut)),
	})
}

func (a *App) handlePriceEvents(w http.ResponseWriter, r *http.Request) {
	if a.debug {
		log.Printf("%s %s", r.Method, r.URL.Path)
	}
	switch r.Method {
	case http.MethodGet:
		cutoff := time.Now().Add(-30 * time.Minute).UnixMilli()
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
	return a.sendTelegramText("ETH Liquidation Map test message")
}

func (a *App) sendTelegramText(text string) error {
	token := strings.TrimSpace(a.getSetting("telegram_bot_token"))
	channel := strings.TrimSpace(a.getSetting("telegram_channel"))
	if token == "" || channel == "" {
		return fmt.Errorf("telegram bot token or channel is empty")
	}

	url := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", token)
	payload := map[string]string{
		"chat_id": channel,
		"text":    text,
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

func (a *App) startTelegramNotifier(ctx context.Context) {
	go func() {
		tk := time.NewTicker(30 * time.Second)
		defer tk.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tk.C:
				interval := a.loadSettings().NotifyIntervalMin
				if interval <= 0 {
					interval = 15
				}
				now := time.Now().UnixMilli()
				if now-a.lastNotify < int64(interval)*60*1000 {
					continue
				}
				dash, err := a.buildDashboard(1)
				if err != nil || len(dash.Bands) == 0 {
					continue
				}
				msg := a.buildHeatReportMessage(dash)
				if err := a.sendTelegramText(msg); err != nil {
					if a.debug {
						log.Printf("telegram auto send failed: %v", err)
					}
					continue
				}
				a.lastNotify = now
			}
		}
	}()
}

func (a *App) buildHeatReportMessage(d Dashboard) string {
	showBands := map[int]bool{10: true, 20: true, 30: true, 40: true, 50: true, 60: true, 80: true, 100: true, 150: true}
	lines := []string{
		"清算热区速报",
		fmt.Sprintf("当前价: %.1f", d.CurrentPrice),
		"周期: 1天",
	}
	for _, b := range d.Bands {
		if !showBands[b.Band] {
			continue
		}
		lines = append(lines, fmt.Sprintf("%d点内 上方%.1f %.2f万 | 下方%.1f %.2f万", b.Band, b.UpPrice, b.UpNotionalUSD/1e4, b.DownPrice, b.DownNotionalUSD/1e4))
	}
	return strings.Join(lines, "\n")
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
		_ = a.insertOISnapshot(s)
	}
	_ = now
	return nil
}

func (a *App) insertOISnapshot(s Snapshot) error {
	if s.MarkPrice <= 0 || s.OIValueUSD <= 0 {
		return nil
	}
	_, err := a.db.Exec(`INSERT INTO oi_snapshots(exchange, symbol, mark_price, oi_value_usd, funding_rate, updated_ts)
		VALUES(?, ?, ?, ?, ?, ?)`, strings.ToLower(strings.TrimSpace(s.Exchange)), s.Symbol, s.MarkPrice, s.OIValueUSD, s.FundingRate, s.UpdatedTS)
	return err
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
	bands, short, long, err := a.buildHeatFromLiquidationEvents(defaultSymbol, currentPrice, cutoff)
	if err != nil {
		return Dashboard{}, err
	}
	eventsCutoff := cutoff
	if days != windowIntraday {
		eventsCutoff = time.Now().Add(-24 * time.Hour).UnixMilli()
	}
	events := a.loadRecentEvents(defaultSymbol, eventsCutoff)
	analytics := a.buildHeatZoneAnalytics(defaultSymbol, currentPrice, states, bands, short, long, events, cutoff)
	return Dashboard{
		Symbol:       defaultSymbol,
		WindowDays:   days,
		GeneratedAt:  time.Now().UnixMilli(),
		States:       states,
		CurrentPrice: currentPrice,
		Bands:        bands,
		LongestShort: short,
		LongestLong:  long,
		Events:       events,
		Analytics:    analytics,
	}, nil
}

func (a *App) buildHeatFromLiquidationEvents(symbol string, currentPrice float64, cutoff int64) ([]BandRow, []any, []any, error) {
	if currentPrice <= 0 {
		return []BandRow{}, []any{"-", 0}, []any{"-", 0}, nil
	}
	rows, err := a.db.Query(`SELECT side, price, notional_usd FROM liquidation_events WHERE symbol=? AND event_ts>=?`, symbol, cutoff)
	if err != nil {
		return nil, nil, nil, err
	}
	defer rows.Close()
	type ev struct {
		side     string
		price    float64
		notional float64
	}
	events := make([]ev, 0, 1024)
	for rows.Next() {
		var side string
		var price, notional float64
		if err := rows.Scan(&side, &price, &notional); err != nil {
			continue
		}
		if price <= 0 || notional <= 0 {
			continue
		}
		events = append(events, ev{side: strings.ToLower(strings.TrimSpace(side)), price: price, notional: notional})
	}
	out := make([]BandRow, 0, len(bandSizes))
	prevUpTotal := 0.0
	prevDownTotal := 0.0
	maxShortPrice := 0.0
	maxLongPrice := 0.0
	maxShortNotional := 0.0
	maxLongNotional := 0.0
	for _, band := range bandSizes {
		b := float64(band)
		upPrice := math.Round((currentPrice+b)*10) / 10
		downPrice := math.Round((currentPrice-b)*10) / 10
		upTotal := 0.0
		downTotal := 0.0
		for _, e := range events {
			if e.price < downPrice || e.price > upPrice {
				continue
			}
			switch e.side {
			case "short":
				if e.price >= currentPrice {
					upTotal += e.notional
				}
			case "long":
				if e.price <= currentPrice {
					downTotal += e.notional
				}
			default:
				if e.price >= currentPrice {
					upTotal += e.notional
				}
				if e.price <= currentPrice {
					downTotal += e.notional
				}
			}
		}
		out = append(out, BandRow{
			Band:            band,
			UpPrice:         upPrice,
			UpNotionalUSD:   upTotal,
			DownPrice:       downPrice,
			DownNotionalUSD: downTotal,
		})
		upInc := math.Max(0, upTotal-prevUpTotal)
		downInc := math.Max(0, downTotal-prevDownTotal)
		prevUpTotal = upTotal
		prevDownTotal = downTotal
		if upInc > maxShortNotional {
			maxShortNotional = upInc
			maxShortPrice = upPrice
		}
		if downInc > maxLongNotional {
			maxLongNotional = downInc
			maxLongPrice = downPrice
		}
	}
	short := []any{"-", 0}
	if maxShortPrice > 0 {
		short = []any{maxShortPrice, maxShortNotional}
	}
	long := []any{"-", 0}
	if maxLongPrice > 0 {
		long = []any{maxLongPrice, maxLongNotional}
	}
	return out, short, long, nil
}

func bandRowOrDefault(bands []BandRow, currentPrice float64, band int) BandRow {
	for _, b := range bands {
		if b.Band == band {
			return b
		}
	}
	bandF := float64(band)
	return BandRow{
		Band:            band,
		UpPrice:         math.Round((currentPrice+bandF)*10) / 10,
		DownPrice:       math.Round((currentPrice-bandF)*10) / 10,
		UpNotionalUSD:   0,
		DownNotionalUSD: 0,
	}
}

func toFloatAny(v any) float64 {
	switch t := v.(type) {
	case float64:
		return t
	case float32:
		return float64(t)
	case int:
		return float64(t)
	case int64:
		return float64(t)
	case string:
		f, _ := strconv.ParseFloat(strings.TrimSpace(t), 64)
		return f
	default:
		f, _ := strconv.ParseFloat(strings.TrimSpace(fmt.Sprint(v)), 64)
		return f
	}
}

func classifyImbalance(up, down float64) (string, float64) {
	if up <= 0 && down <= 0 {
		return "暂无明显失衡", 1
	}
	if up >= down {
		ratio := up / math.Max(down, 1e-9)
		if ratio >= 1.25 {
			return "上方偏强", ratio
		}
		return "基本均衡", ratio
	}
	ratio := down / math.Max(up, 1e-9)
	if ratio >= 1.25 {
		return "下方偏强", ratio
	}
	return "基本均衡", ratio
}

func inferShortBias(up, down float64) string {
	total := up + down
	if total <= 0 {
		return "暂无数据"
	}
	delta := (up - down) / total
	if delta >= 0.18 {
		return "偏上杀"
	}
	if delta <= -0.18 {
		return "偏下杀"
	}
	return "基本均衡"
}

func normalizeExchangeName(ex string) string {
	switch strings.ToLower(strings.TrimSpace(ex)) {
	case "binance":
		return "Binance"
	case "bybit":
		return "Bybit"
	case "okx":
		return "OKX"
	default:
		return strings.ToUpper(strings.TrimSpace(ex))
	}
}

func (a *App) sumBandNotionalInRange(symbol string, currentPrice, band float64, startTS, endTS int64) (float64, float64) {
	if currentPrice <= 0 || band <= 0 || endTS <= startTS {
		return 0, 0
	}
	rows, err := a.db.Query(`SELECT side, price, notional_usd FROM liquidation_events
		WHERE symbol=? AND event_ts>=? AND event_ts<?`, symbol, startTS, endTS)
	if err != nil {
		return 0, 0
	}
	defer rows.Close()
	up := 0.0
	down := 0.0
	for rows.Next() {
		var side string
		var price, notional float64
		if err := rows.Scan(&side, &price, &notional); err != nil {
			continue
		}
		if price <= 0 || notional <= 0 {
			continue
		}
		if math.Abs(price-currentPrice) > band {
			continue
		}
		side = strings.ToLower(strings.TrimSpace(side))
		switch side {
		case "short":
			if price >= currentPrice {
				up += notional
			}
		case "long":
			if price <= currentPrice {
				down += notional
			}
		default:
			if price >= currentPrice {
				up += notional
			}
			if price <= currentPrice {
				down += notional
			}
		}
	}
	return up, down
}

func (a *App) maxBandNotionalInRange(symbol string, currentPrice, band float64, startTS, endTS int64) float64 {
	if currentPrice <= 0 || band <= 0 || endTS <= startTS {
		return 0
	}
	rows, err := a.db.Query(`SELECT price, notional_usd FROM liquidation_events
		WHERE symbol=? AND event_ts>=? AND event_ts<?`, symbol, startTS, endTS)
	if err != nil {
		return 0
	}
	defer rows.Close()
	maxV := 0.0
	for rows.Next() {
		var price, notional float64
		if err := rows.Scan(&price, &notional); err != nil {
			continue
		}
		if price <= 0 || notional <= 0 {
			continue
		}
		if math.Abs(price-currentPrice) > band {
			continue
		}
		if notional > maxV {
			maxV = notional
		}
	}
	return maxV
}

func (a *App) avgFundingInRange(symbol string, startTS, endTS int64) float64 {
	if endTS <= startTS {
		return 0
	}
	var v sql.NullFloat64
	if err := a.db.QueryRow(`SELECT AVG(funding_rate) FROM oi_snapshots
		WHERE symbol=? AND updated_ts>=? AND updated_ts<? AND funding_rate IS NOT NULL`, symbol, startTS, endTS).Scan(&v); err != nil {
		return 0
	}
	if !v.Valid {
		return 0
	}
	return v.Float64
}

func (a *App) sumLiquidationNotionalSince(symbol string, startTS int64) float64 {
	var total sql.NullFloat64
	if err := a.db.QueryRow(`SELECT SUM(notional_usd) FROM liquidation_events WHERE symbol=? AND event_ts>=?`, symbol, startTS).Scan(&total); err != nil {
		return 0
	}
	if !total.Valid {
		return 0
	}
	return total.Float64
}

func (a *App) computeExchangeContribution(symbol string, currentPrice, band float64, cutoff int64, states []MarketState) []ExchangeContribution {
	sums := map[string]float64{
		"binance": 0,
		"bybit":   0,
		"okx":     0,
	}
	rows, err := a.db.Query(`SELECT LOWER(exchange), price, notional_usd FROM liquidation_events
		WHERE symbol=? AND event_ts>=?`, symbol, cutoff)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var ex string
			var price, notional float64
			if err := rows.Scan(&ex, &price, &notional); err != nil {
				continue
			}
			if price <= 0 || notional <= 0 {
				continue
			}
			if math.Abs(price-currentPrice) > band {
				continue
			}
			sums[ex] += notional
		}
	}
	total := 0.0
	for _, v := range sums {
		total += v
	}
	if total <= 0 {
		for _, s := range states {
			if s.OIValueUSD == nil || *s.OIValueUSD <= 0 {
				continue
			}
			ex := strings.ToLower(strings.TrimSpace(s.Exchange))
			sums[ex] += *s.OIValueUSD
		}
		total = 0
		for _, v := range sums {
			total += v
		}
	}
	out := make([]ExchangeContribution, 0, len(sums))
	for ex, v := range sums {
		share := 0.0
		if total > 0 {
			share = v / total
		}
		out = append(out, ExchangeContribution{
			Exchange:    normalizeExchangeName(ex),
			NotionalUSD: v,
			Share:       share,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].NotionalUSD == out[j].NotionalUSD {
			return out[i].Exchange < out[j].Exchange
		}
		return out[i].NotionalUSD > out[j].NotionalUSD
	})
	return out
}

func (a *App) buildHeatZoneAnalytics(symbol string, currentPrice float64, states []MarketState, bands []BandRow, longestShort, longestLong []any, events []EventRow, cutoff int64) HeatZoneAnalytics {
	b20 := bandRowOrDefault(bands, currentPrice, 20)
	b50 := bandRowOrDefault(bands, currentPrice, 50)
	b100 := bandRowOrDefault(bands, currentPrice, 100)

	verdict20, ratio20 := classifyImbalance(b20.UpNotionalUSD, b20.DownNotionalUSD)
	verdict50, ratio50 := classifyImbalance(b50.UpNotionalUSD, b50.DownNotionalUSD)
	verdict100, ratio100 := classifyImbalance(b100.UpNotionalUSD, b100.DownNotionalUSD)

	imbalance := []ImbalanceStat{
		{Band: 20, UpNotionalUSD: b20.UpNotionalUSD, DownNotionalUSD: b20.DownNotionalUSD, Ratio: ratio20, Verdict: verdict20},
		{Band: 50, UpNotionalUSD: b50.UpNotionalUSD, DownNotionalUSD: b50.DownNotionalUSD, Ratio: ratio50, Verdict: verdict50},
		{Band: 100, UpNotionalUSD: b100.UpNotionalUSD, DownNotionalUSD: b100.DownNotionalUSD, Ratio: ratio100, Verdict: verdict100},
	}
	density := []DensityLayer{
		{Label: "0-20点", UpNotionalUSD: math.Max(0, b20.UpNotionalUSD), DownNotionalUSD: math.Max(0, b20.DownNotionalUSD)},
		{Label: "20-50点", UpNotionalUSD: math.Max(0, b50.UpNotionalUSD-b20.UpNotionalUSD), DownNotionalUSD: math.Max(0, b50.DownNotionalUSD-b20.DownNotionalUSD)},
		{Label: "50-100点", UpNotionalUSD: math.Max(0, b100.UpNotionalUSD-b50.UpNotionalUSD), DownNotionalUSD: math.Max(0, b100.DownNotionalUSD-b50.DownNotionalUSD)},
	}

	market := MarketSummary{}
	fundingSum := 0.0
	fundingCnt := 0
	for _, s := range states {
		ex := strings.ToLower(strings.TrimSpace(s.Exchange))
		oi := 0.0
		if s.OIValueUSD != nil && *s.OIValueUSD > 0 {
			oi = *s.OIValueUSD
		}
		switch ex {
		case "binance":
			market.BinanceOIUSD = oi
		case "okx":
			market.OKXOIUSD = oi
		case "bybit":
			market.BybitOIUSD = oi
		}
		if s.FundingRate != nil {
			fundingSum += *s.FundingRate
			fundingCnt++
		}
	}
	if fundingCnt > 0 {
		market.AvgFunding = fundingSum / float64(fundingCnt)
	}

	shortPrice := 0.0
	shortNotional := 0.0
	if len(longestShort) >= 2 {
		shortPrice = toFloatAny(longestShort[0])
		shortNotional = toFloatAny(longestShort[1])
	}
	longPrice := 0.0
	longNotional := 0.0
	if len(longestLong) >= 2 {
		longPrice = toFloatAny(longestLong[0])
		longNotional = toFloatAny(longestLong[1])
	}
	core := CoreZone{
		UpPrice:         shortPrice,
		UpNotionalUSD:   shortNotional,
		DownPrice:       longPrice,
		DownNotionalUSD: longNotional,
	}
	distUp := math.MaxFloat64
	if shortPrice > 0 {
		distUp = math.Abs(shortPrice - currentPrice)
	}
	distDown := math.MaxFloat64
	if longPrice > 0 {
		distDown = math.Abs(currentPrice - longPrice)
	}
	if distUp < distDown {
		core.NearestSide = "上方强区"
		core.NearestDistance = distUp
		core.NearestStrongPrice = shortPrice
	} else if distDown < math.MaxFloat64 {
		core.NearestSide = "下方强区"
		core.NearestDistance = distDown
		core.NearestStrongPrice = longPrice
	}

	nowTS := time.Now().UnixMilli()
	slot := int64(5 * 60 * 1000)
	upNow, downNow := a.sumBandNotionalInRange(symbol, currentPrice, 20, nowTS-slot, nowTS)
	upPrev, downPrev := a.sumBandNotionalInRange(symbol, currentPrice, 20, nowTS-2*slot, nowTS-slot)
	maxNow := a.maxBandNotionalInRange(symbol, currentPrice, 100, nowTS-slot, nowTS)
	maxPrev := a.maxBandNotionalInRange(symbol, currentPrice, 100, nowTS-2*slot, nowTS-slot)
	fundingNow := a.avgFundingInRange(symbol, nowTS-slot, nowTS)
	fundingPrev := a.avgFundingInRange(symbol, nowTS-2*slot, nowTS-slot)
	tracking := ChangeTracking{
		Up20DeltaUSD:    upNow - upPrev,
		Down20DeltaUSD:  downNow - downPrev,
		LongestDeltaUSD: maxNow - maxPrev,
		FundingDelta:    fundingNow - fundingPrev,
	}

	contrib := a.computeExchangeContribution(symbol, currentPrice, 50, cutoff, states)
	dominant := "-"
	if len(contrib) > 0 {
		dominant = contrib[0].Exchange
	}

	alert := AlertSummary{
		Level:       "常规监控",
		Recent1mUSD: a.sumLiquidationNotionalSince(symbol, nowTS-60*1000),
		Suggestion:  fmt.Sprintf("%.1f / %.1f 关注上下关键价位", b20.UpPrice, b20.DownPrice),
	}
	if len(events) > 0 {
		e := events[0]
		alert.RecentExchange = normalizeExchangeName(e.Exchange)
		alert.RecentSide = strings.ToLower(strings.TrimSpace(e.Side))
		alert.RecentPrice = e.Price
	}
	if core.NearestDistance > 0 && core.NearestDistance <= 35 {
		alert.Level = "临近强区预警"
	} else if alert.Recent1mUSD >= 2_000_000 {
		alert.Level = "波动放大预警"
	}
	if verdict20 == "上方偏强" {
		alert.Suggestion = fmt.Sprintf("%.1f / %.1f 警惕上下双向插针风险", b20.UpPrice, b20.DownPrice)
	}
	if verdict20 == "下方偏强" {
		alert.Suggestion = fmt.Sprintf("%.1f / %.1f 下方流动性风险更高", b20.UpPrice, b20.DownPrice)
	}

	return HeatZoneAnalytics{
		Top: TopConclusion{
			ShortBias:   inferShortBias(b20.UpNotionalUSD, b20.DownNotionalUSD),
			Bias20Delta: b20.UpNotionalUSD - b20.DownNotionalUSD,
			Bias50Label: verdict50,
		},
		Market:           market,
		ImbalanceStats:   imbalance,
		DensityLayers:    density,
		ChangeTracking:   tracking,
		CoreZone:         core,
		ExchangeContrib:  contrib,
		DominantExchange: dominant,
		Alert:            alert,
	}
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

func (a *App) buildModelLiquidationMap(symbol string, lookbackMin, bucketMin int, priceStep, priceRange float64, cfg ModelConfig) (map[string]any, error) {
	now := time.Now().UnixMilli()
	lookbackMS := int64(lookbackMin) * 60 * 1000
	start := now - lookbackMS
	states, err := a.loadMarketStates(symbol)
	if err != nil {
		return nil, err
	}
	current := averageMarkPrice(states)
	if current <= 0 {
		return map[string]any{
			"generated_at":   now,
			"current_price":  0,
			"lookback_min":   lookbackMin,
			"bucket_min":     bucketMin,
			"price_step":     priceStep,
			"price_range":    priceRange,
			"times":          []int64{},
			"prices":         []float64{},
			"intensity_grid": [][]float64{},
			"max_intensity":  0.0,
		}, nil
	}
	current = math.Round(current*10) / 10

	type snap struct {
		ex      string
		mark    float64
		oi      float64
		funding float64
		ts      int64
	}
	rows, err := a.db.Query(`SELECT LOWER(exchange), mark_price, oi_value_usd, funding_rate, updated_ts
		FROM oi_snapshots WHERE symbol=? AND updated_ts>=? ORDER BY exchange, updated_ts`, symbol, start)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	snaps := make([]snap, 0, 4096)
	for rows.Next() {
		var s snap
		var funding sql.NullFloat64
		if err := rows.Scan(&s.ex, &s.mark, &s.oi, &funding, &s.ts); err != nil {
			continue
		}
		if funding.Valid {
			s.funding = funding.Float64
		}
		if s.mark <= 0 || s.oi <= 0 {
			continue
		}
		snaps = append(snaps, s)
	}

	type contrib struct {
		ts        int64
		liqPrice  float64
		intensity float64
		side      string
	}
	contribs := make([]contrib, 0, len(snaps)*6)
	prevOI := map[string]float64{}
	levs := parseCSVFloats(cfg.LeverageCSV)
	weights := parseCSVFloats(cfg.WeightCSV)
	if len(levs) == 0 {
		levs = []float64{20, 50, 100}
	}
	if len(weights) == 0 || len(weights) != len(levs) {
		weights = make([]float64, len(levs))
		for i := range weights {
			weights[i] = 1.0
		}
	}
	sumW := 0.0
	for _, w := range weights {
		sumW += w
	}
	if sumW <= 0 {
		sumW = 1
	}
	for i := range weights {
		weights[i] /= sumW
	}
	mm := cfg.MaintMargin
	if mm <= 0 {
		mm = 0.005
	}
	fundingScale := cfg.FundingScale
	if fundingScale <= 0 {
		fundingScale = 7000
	}
	decayK := cfg.DecayK
	if decayK <= 0 {
		decayK = 2.2
	}
	neighborShare := cfg.NeighborShare
	if neighborShare < 0 {
		neighborShare = 0
	}
	if neighborShare > 1 {
		neighborShare = 0.28
	}
	for _, s := range snaps {
		prev, ok := prevOI[s.ex]
		prevOI[s.ex] = s.oi
		if !ok {
			continue
		}
		delta := s.oi - prev
		if delta <= 0 {
			continue
		}
		longShare := clamp(0.5+s.funding*fundingScale, 0.2, 0.8)
		shortShare := 1 - longShare
		for i, lev := range levs {
			w := weights[i]
			longAmt := delta * longShare * w
			shortAmt := delta * shortShare * w
			liqLong := s.mark * (1 - 1/lev + mm)
			liqShort := s.mark * (1 + 1/lev - mm)
			if liqLong > 0 && longAmt > 0 {
				contribs = append(contribs, contrib{ts: s.ts, liqPrice: liqLong, intensity: longAmt, side: "long"})
			}
			if liqShort > 0 && shortAmt > 0 {
				contribs = append(contribs, contrib{ts: s.ts, liqPrice: liqShort, intensity: shortAmt, side: "short"})
			}
		}
	}

	bucketMS := int64(bucketMin) * 60 * 1000
	baseStart := (start / bucketMS) * bucketMS
	cols := int((now-baseStart)/bucketMS) + 1
	if cols < 2 {
		cols = 2
	}
	times := make([]int64, cols)
	for i := 0; i < cols; i++ {
		times[i] = baseStart + int64(i)*bucketMS
	}

	pMin := math.Round((current-priceRange)/priceStep) * priceStep
	pMax := math.Round((current+priceRange)/priceStep) * priceStep
	if pMin <= 0 {
		pMin = priceStep
	}
	rowsN := int(math.Round((pMax-pMin)/priceStep)) + 1
	if rowsN < 20 {
		rowsN = 20
	}
	prices := make([]float64, rowsN)
	for i := 0; i < rowsN; i++ {
		prices[i] = pMin + float64(i)*priceStep
	}
	grid := make([][]float64, rowsN)
	for i := range grid {
		grid[i] = make([]float64, cols)
	}

	for _, c := range contribs {
		if c.ts < baseStart || c.ts > now {
			continue
		}
		// remove triggered zones
		if c.side == "long" && current <= c.liqPrice {
			continue
		}
		if c.side == "short" && current >= c.liqPrice {
			continue
		}
		col := int((c.ts - baseStart) / bucketMS)
		if col < 0 || col >= cols {
			continue
		}
		row := int(math.Round((c.liqPrice - pMin) / priceStep))
		if row < 0 || row >= rowsN {
			continue
		}
		age := float64(now-c.ts) / float64(lookbackMS)
		decay := math.Exp(-decayK * age)
		v := c.intensity * decay
		grid[row][col] += v
		if row > 0 {
			grid[row-1][col] += v * neighborShare
		}
		if row < rowsN-1 {
			grid[row+1][col] += v * neighborShare
		}
	}

	maxV := 0.0
	for i := range grid {
		for j := range grid[i] {
			if grid[i][j] > maxV {
				maxV = grid[i][j]
			}
		}
	}
	return map[string]any{
		"generated_at":   now,
		"current_price":  current,
		"lookback_min":   lookbackMin,
		"bucket_min":     bucketMin,
		"price_step":     priceStep,
		"price_range":    priceRange,
		"times":          times,
		"prices":         prices,
		"intensity_grid": grid,
		"max_intensity":  maxV,
	}, nil
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

func (a *App) loadLiquidations(symbol string, limit, offset int, startTS, endTS int64) []EventRow {
	if limit <= 0 {
		limit = 25
	}
	if limit > 10000 {
		limit = 10000
	}
	if offset < 0 {
		offset = 0
	}
	baseSQL := `SELECT exchange, side, price, qty, notional_usd, event_ts
		FROM liquidation_events WHERE symbol=?`
	args := []any{symbol}
	if startTS > 0 {
		baseSQL += ` AND event_ts >= ?`
		args = append(args, startTS)
	}
	if endTS > 0 {
		baseSQL += ` AND event_ts <= ?`
		args = append(args, endTS)
	}
	baseSQL += ` ORDER BY event_ts DESC LIMIT ? OFFSET ?`
	args = append(args, limit, offset)
	rows, err := a.db.Query(baseSQL, args...)
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := make([]EventRow, 0, limit)
	for rows.Next() {
		var e EventRow
		if err := rows.Scan(&e.Exchange, &e.Side, &e.Price, &e.Qty, &e.NotionalUSD, &e.EventTS); err != nil {
			continue
		}
		out = append(out, e)
	}
	return out
}

func (a *App) handleLiquidationsAPI(w http.ResponseWriter, r *http.Request) {
	if a.debug {
		log.Printf("%s %s", r.Method, r.URL.Path)
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	limit := 25
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 && n <= 1000 {
			limit = n
		}
	}
	minQty := 0.0
	if raw := strings.TrimSpace(r.URL.Query().Get("min_qty")); raw != "" {
		if v, err := strconv.ParseFloat(raw, 64); err == nil && v >= 0 {
			minQty = v
		}
	}
	page := 1
	if raw := strings.TrimSpace(r.URL.Query().Get("page")); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			page = n
		}
	}
	startTS := int64(0)
	if raw := strings.TrimSpace(r.URL.Query().Get("start_ts")); raw != "" {
		if n, err := strconv.ParseInt(raw, 10, 64); err == nil && n > 0 {
			startTS = n
		}
	}
	endTS := int64(0)
	if raw := strings.TrimSpace(r.URL.Query().Get("end_ts")); raw != "" {
		if n, err := strconv.ParseInt(raw, 10, 64); err == nil && n > 0 {
			endTS = n
		}
	}
	offset := (page - 1) * limit
	rows := a.loadLiquidations(defaultSymbol, limit, offset, startTS, endTS)
	if minQty > 0 {
		filtered := make([]EventRow, 0, len(rows))
		for _, it := range rows {
			if it.Qty >= minQty {
				filtered = append(filtered, it)
			}
		}
		rows = filtered
	}
	_ = json.NewEncoder(w).Encode(map[string]any{
		"page":      page,
		"page_size": limit,
		"rows":      rows,
	})
}

func (a *App) handleKlinesAPI(w http.ResponseWriter, r *http.Request) {
	if a.debug {
		log.Printf("%s %s", r.Method, r.URL.Path)
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	interval := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("interval")))
	limit := 300
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n >= 50 && n <= 1000 {
			limit = n
		}
	}
	startTS := int64(0)
	if raw := strings.TrimSpace(r.URL.Query().Get("start_ts")); raw != "" {
		if n, err := strconv.ParseInt(raw, 10, 64); err == nil && n > 0 {
			startTS = n
		}
	}
	endTS := int64(0)
	if raw := strings.TrimSpace(r.URL.Query().Get("end_ts")); raw != "" {
		if n, err := strconv.ParseInt(raw, 10, 64); err == nil && n > 0 {
			endTS = n
		}
	}
	allowed := map[string]bool{"1m": true, "5m": true, "15m": true, "30m": true, "1h": true, "4h": true, "8h": true, "12h": true, "1d": true, "3d": true, "1w": true}
	binInterval := interval
	if interval == "" {
		binInterval = "15m"
	}
	if interval == "2m" {
		binInterval = "1m"
	}
	if interval == "10m" {
		binInterval = "5m"
	}
	if interval == "7d" {
		binInterval = "1w"
	}
	if !allowed[binInterval] {
		http.Error(w, "unsupported interval", http.StatusBadRequest)
		return
	}
	var rows [][]any
	url := fmt.Sprintf("https://api.binance.com/api/v3/klines?symbol=ETHUSDT&interval=%s&limit=%d", binInterval, limit)
	if startTS > 0 {
		url += fmt.Sprintf("&startTime=%d", startTS)
	}
	if endTS > 0 {
		url += fmt.Sprintf("&endTime=%d", endTS)
	}
	if err := a.fetchJSON(url, &rows); err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{
		"interval": interval,
		"source":   binInterval,
		"rows":     rows,
	})
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
	perExBid := map[string]map[string]float64{"binance": {}, "okx": {}, "bybit": {}}
	perExAsk := map[string]map[string]float64{"binance": {}, "okx": {}, "bybit": {}}
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
			q := lv.Qty * w
			bidMap[key] += q
			perExBid[v.ex][key] += q
		}
		for _, lv := range v.asks {
			key := strconv.FormatFloat(math.Round(lv.Price*10)/10, 'f', 1, 64)
			q := lv.Qty * w
			askMap[key] += q
			perExAsk[v.ex][key] += q
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
	perExchange := map[string]any{
		"binance": map[string]any{"bids": sideTop(perExBid["binance"], limit, true), "asks": sideTop(perExAsk["binance"], limit, false)},
		"okx":     map[string]any{"bids": sideTop(perExBid["okx"], limit, true), "asks": sideTop(perExAsk["okx"], limit, false)},
		"bybit":   map[string]any{"bids": sideTop(perExBid["bybit"], limit, true), "asks": sideTop(perExAsk["bybit"], limit, false)},
	}
	return map[string]any{
		"best_bid":     bestBid,
		"best_ask":     bestAsk,
		"mid":          mid,
		"bids":         bids,
		"asks":         asks,
		"per_exchange": perExchange,
		"depth_curve":  buildDepthCurve(bids, asks, mid),
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
			"bps":          bps,
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

func normalizeLiquidationSide(raw string) string {
	s := strings.ToLower(strings.TrimSpace(raw))
	switch s {
	case "buy", "b", "short":
		return "short"
	case "sell", "s", "long":
		return "long"
	default:
		return s
	}
}

func (a *App) insertLiquidationEvent(exchange, symbol, side, rawSide string, price, qty, markPrice float64, eventTS int64) {
	if price <= 0 || qty <= 0 {
		return
	}
	if markPrice <= 0 {
		markPrice = price
	}
	if eventTS <= 0 {
		eventTS = time.Now().UnixMilli()
	}
	notional := price * qty
	normSide := normalizeLiquidationSide(side)
	if normSide == "" {
		normSide = normalizeLiquidationSide(rawSide)
	}
	_, _ = a.db.Exec(`INSERT OR IGNORE INTO liquidation_events(exchange, symbol, side, raw_side, qty, price, mark_price, notional_usd, event_ts, inserted_ts)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		exchange, symbol, normSide, rawSide, qty, price, markPrice, notional, eventTS, time.Now().UnixMilli())
}

func (a *App) startLiquidationSync(ctx context.Context) {
	okxCtVal := a.fetchOKXContractValue("ETH-USDT-SWAP")
	if okxCtVal <= 0 {
		okxCtVal = 1
	}
	go a.backfillOKXLiquidations(ctx, "ETH-USDT-SWAP", okxCtVal, 24)
	go a.syncBinanceLiquidations(ctx, "ETHUSDT")
	go a.syncBybitLiquidations(ctx, "ETHUSDT")
	go a.syncOKXLiquidations(ctx, "ETH-USDT-SWAP", okxCtVal)
}

func (a *App) fetchOKXContractValue(instID string) float64 {
	var resp struct {
		Code string `json:"code"`
		Data []struct {
			CtVal string `json:"ctVal"`
		} `json:"data"`
	}
	if err := a.fetchJSON("https://www.okx.com/api/v5/public/instruments?instType=SWAP&instId="+instID, &resp); err != nil {
		return 1
	}
	if len(resp.Data) == 0 {
		return 1
	}
	v := parseFloat(resp.Data[0].CtVal)
	if v <= 0 {
		return 1
	}
	return v
}

func (a *App) backfillOKXLiquidations(ctx context.Context, instID string, ctVal float64, lookbackHours int) {
	var resp struct {
		Code string `json:"code"`
		Data []struct {
			Details []struct {
				BkPx    string `json:"bkPx"`
				Side    string `json:"side"`
				PosSide string `json:"posSide"`
				Sz      string `json:"sz"`
				TS      string `json:"ts"`
				Time    int64  `json:"time"`
			} `json:"details"`
		} `json:"data"`
	}
	if err := a.fetchJSON("https://www.okx.com/api/v5/public/liquidation-orders?instType=SWAP&state=filled&uly=ETH-USDT", &resp); err != nil {
		if a.debug {
			log.Printf("okx liquidation backfill err: %v", err)
		}
		return
	}
	cutoff := time.Now().Add(-time.Duration(lookbackHours) * time.Hour).UnixMilli()
	for _, grp := range resp.Data {
		for _, d := range grp.Details {
			select {
			case <-ctx.Done():
				return
			default:
			}
			ts := int64(parseAnyFloat(d.TS))
			if ts <= 0 {
				ts = d.Time
			}
			if ts < cutoff {
				continue
			}
			price := parseFloat(d.BkPx)
			qty := parseFloat(d.Sz) * ctVal
			side := d.PosSide
			if side == "" {
				side = d.Side
			}
			a.insertLiquidationEvent("okx", defaultSymbol, side, d.Side, price, qty, 0, ts)
		}
	}
}

func (a *App) syncBinanceLiquidations(ctx context.Context, symbol string) {
	wsURL := "wss://fstream.binance.com/ws/" + strings.ToLower(symbol) + "@forceOrder"
	for {
		conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
		if err != nil {
			if a.debug {
				log.Printf("binance liquidation ws dial err: %v", err)
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(2 * time.Second):
				continue
			}
		}
		for {
			select {
			case <-ctx.Done():
				_ = conn.Close()
				return
			default:
			}
			var msg map[string]any
			if err := conn.ReadJSON(&msg); err != nil {
				break
			}
			o, ok := msg["o"].(map[string]any)
			if !ok {
				continue
			}
			side := fmt.Sprint(o["S"])
			price := parseAnyFloat(o["ap"])
			if price <= 0 {
				price = parseAnyFloat(o["p"])
			}
			qty := parseAnyFloat(o["q"])
			if qty <= 0 {
				qty = parseAnyFloat(o["z"])
			}
			ts := int64(parseAnyFloat(o["T"]))
			a.insertLiquidationEvent("binance", symbol, side, side, price, qty, 0, ts)
		}
		_ = conn.Close()
		select {
		case <-ctx.Done():
			return
		case <-time.After(2 * time.Second):
		}
	}
}

func (a *App) syncBybitLiquidations(ctx context.Context, symbol string) {
	for {
		conn, _, err := websocket.DefaultDialer.Dial("wss://stream.bybit.com/v5/public/linear", nil)
		if err != nil {
			if a.debug {
				log.Printf("bybit liquidation ws dial err: %v", err)
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(2 * time.Second):
				continue
			}
		}
		sub := map[string]any{"op": "subscribe", "args": []string{"allLiquidation." + symbol}}
		if err := conn.WriteJSON(sub); err != nil {
			_ = conn.Close()
			select {
			case <-ctx.Done():
				return
			case <-time.After(2 * time.Second):
				continue
			}
		}
		for {
			select {
			case <-ctx.Done():
				_ = conn.Close()
				return
			default:
			}
			var msg map[string]any
			if err := conn.ReadJSON(&msg); err != nil {
				break
			}
			dataArr, ok := msg["data"].([]any)
			if !ok {
				continue
			}
			for _, it := range dataArr {
				row, ok := it.(map[string]any)
				if !ok {
					continue
				}
				side := fmt.Sprint(row["S"])
				price := parseAnyFloat(row["p"])
				qty := parseAnyFloat(row["v"])
				if qty <= 0 {
					qty = parseAnyFloat(row["q"])
				}
				ts := int64(parseAnyFloat(row["T"]))
				a.insertLiquidationEvent("bybit", symbol, side, side, price, qty, 0, ts)
			}
		}
		_ = conn.Close()
		select {
		case <-ctx.Done():
			return
		case <-time.After(2 * time.Second):
		}
	}
}

func (a *App) syncOKXLiquidations(ctx context.Context, instID string, ctVal float64) {
	for {
		conn, _, err := websocket.DefaultDialer.Dial("wss://ws.okx.com:8443/ws/v5/public", nil)
		if err != nil {
			if a.debug {
				log.Printf("okx liquidation ws dial err: %v", err)
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(2 * time.Second):
				continue
			}
		}
		sub := map[string]any{"op": "subscribe", "args": []map[string]string{{"channel": "liquidation-orders", "instType": "SWAP", "instId": instID}}}
		if err := conn.WriteJSON(sub); err != nil {
			_ = conn.Close()
			select {
			case <-ctx.Done():
				return
			case <-time.After(2 * time.Second):
				continue
			}
		}
		for {
			select {
			case <-ctx.Done():
				_ = conn.Close()
				return
			default:
			}
			var msg map[string]any
			if err := conn.ReadJSON(&msg); err != nil {
				break
			}
			dataArr, ok := msg["data"].([]any)
			if !ok {
				continue
			}
			for _, it := range dataArr {
				row, ok := it.(map[string]any)
				if !ok {
					continue
				}
				side := fmt.Sprint(row["side"])
				price := parseAnyFloat(row["bkPx"])
				if price <= 0 {
					price = parseAnyFloat(row["px"])
				}
				qty := parseAnyFloat(row["sz"]) * ctVal
				ts := int64(parseAnyFloat(row["ts"]))
				posSide := fmt.Sprint(row["posSide"])
				if strings.TrimSpace(posSide) == "" {
					posSide = side
				}
				a.insertLiquidationEvent("okx", defaultSymbol, posSide, side, price, qty, 0, ts)
			}
		}
		_ = conn.Close()
		select {
		case <-ctx.Done():
			return
		case <-time.After(2 * time.Second):
		}
	}
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
:root{--bg:#e8edf4;--nav:#0b1220;--nav-border:#243145;--panel:#ffffff;--line:#dfe6ee;--left:#c2cad7;--left-head:#b7c0cf;--right:#efe9cf;--right-head:#e7dfbe;--text:#3b4c63;--accent:#d3873e}
body{margin:0;background:var(--bg);color:var(--text);font-family:Inter,system-ui,Segoe UI,Arial,sans-serif}
.nav{height:56px;background:var(--nav);border-bottom:1px solid var(--nav-border);display:flex;align-items:center;justify-content:space-between;padding:0 20px;position:sticky;top:0;z-index:10}
.nav-left,.nav-right{display:flex;align-items:center;gap:20px}.brand{font-size:18px;font-weight:700;color:#eef3f9}
.menu a{color:#d6deea;text-decoration:none;font-size:16px;margin-right:18px}.menu a.active{color:#fff;font-weight:700}
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
.heatmap-wrap{border:1px solid var(--line);border-radius:10px;background:#fff;padding:10px}
#liqHeatMap{width:100%;height:320px;display:block;border:1px solid var(--line);border-radius:8px;background:#fff}
.desc{font-size:12px;color:#5b6b7f;line-height:1.5;margin-top:8px}
.upgrade-modal{position:fixed;inset:0;background:rgba(2,6,23,.55);display:none;align-items:center;justify-content:center;z-index:9999}.upgrade-modal.show{display:flex}.upgrade-card{width:min(880px,92vw);max-height:82vh;background:#0b1220;color:#e2e8f0;border:1px solid #334155;border-radius:10px;box-shadow:0 10px 30px rgba(2,6,23,.45);overflow:hidden}.upgrade-head{display:flex;align-items:center;justify-content:space-between;padding:10px 12px;border-bottom:1px solid #334155}.upgrade-title{font-size:14px;font-weight:700}.upgrade-close{background:transparent;border:1px solid #475569;color:#e2e8f0;border-radius:6px;padding:4px 8px;cursor:pointer}.upgrade-log{margin:0;padding:12px;white-space:pre-wrap;overflow:auto;max-height:62vh;font-family:ui-monospace,SFMono-Regular,Menlo,Consolas,monospace;font-size:12px;line-height:1.45}.upgrade-foot{padding:8px 12px;border-top:1px solid #334155;font-size:12px;color:#94a3b8}.footer{margin:18px auto 0 auto;max-width:1200px;padding:10px 12px;font-size:12px;color:#64748b;text-align:center}</style></head><body>
<div class="nav"><div class="nav-left"><div class="brand">ETH Liquidation Map</div><div class="menu"><a href="/" class="active">清算热区</a><a href="/config">模型配置</a><a href="/monitor">雷区监控</a><a href="/map">盘口汇总</a><a href="/liquidations">强平清算</a><a href="/bubbles">气泡图</a><a href="/channel">消息通道</a></div></div><div class="nav-right"><a href="#" class="upgrade" onclick="return doUpgrade(event)">&#21319;&#32423;</a></div></div>
<div class="wrap"><div class="panel top"><div><h2 style="margin:0 0 6px 0;color:#111827">ETH &#28165;&#31639;&#28909;&#21306;</h2><div class="hint">&#25353; <span class="mono">0</span> / <span class="mono">1</span> / <span class="mono">7</span> / <span class="mono">3</span> &#20999;&#25442; &#26085;&#20869; / 1&#22825; / 7&#22825; / 30&#22825;</div></div><div class="btns"><button data-days="0">&#26085;&#20869;</button><button data-days="1">1&#22825;</button><button data-days="7">7&#22825;</button><button data-days="30">30&#22825;</button></div></div>
<div class="panel"><div id="status">loading...</div></div>
<div class="grid"><div class="panel"><h3>&#24066;&#22330;&#29366;&#24577;</h3><div id="market"></div></div><div class="panel"><h3>&#28165;&#31639;&#28909;&#21306;&#36895;&#25253;</h3><div id="bands"></div></div><div class="panel"><h3>ETH清算地图（OI增量模型）</h3><div class="heatmap-wrap"><canvas id="liqHeatMap" width="1400" height="320"></canvas><div class="hint">X轴: 价格 | Y轴: 清算金额（按价格聚合）</div><div id="liqDesc" class="desc"></div></div></div></div></div>
<script>
let currentDays=1;
let liqMapData=null;
let modelConfig=null;
function windowLabel(v){return Number(v)===0?'\u65e5\u5185':(Number(v)+'\u5929')}
async function setWindow(days){currentDays=days;await fetch('/api/window',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({days})});renderActive();load();}
function renderActive(){document.querySelectorAll('button[data-days]').forEach(b=>b.classList.toggle('active',Number(b.dataset.days)===currentDays));}
function fmtPrice(n){return Number(n).toLocaleString('zh-CN',{minimumFractionDigits:1,maximumFractionDigits:1})}
function fmtAmount(n){n=Number(n);if(!isFinite(n))return '-';const a=Math.abs(n);if(a>=1e8)return (n/1e8).toFixed(2)+'\u4ebf';if(a>=1e6)return (n/1e6).toFixed(2)+'\u767e\u4e07';return (n/1e4).toFixed(2)+'\u4e07';}
function fmt4(n){return Number(n).toFixed(8)}
function fitCanvas(id){const c=document.getElementById(id);if(!c)return null;const dpr=window.devicePixelRatio||1;const rect=c.getBoundingClientRect();const W=Math.max(320,Math.floor(rect.width));const H=Math.max(220,Math.floor(rect.height));const rw=Math.floor(W*dpr),rh=Math.floor(H*dpr);if(c.width!==rw||c.height!==rh){c.width=rw;c.height=rh;}const x=c.getContext('2d');x.setTransform(dpr,0,0,dpr,0,0);return {c,x,W,H};}
function heatColor(v,max){if(!(max>0)||!(v>0))return 'rgb(248,250,252)';let t=Math.max(0,Math.min(1,v/max));t=Math.pow(t,0.55);let r,g,b;if(t<0.5){const k=t/0.5;r=Math.round(15+(46-15)*k);g=Math.round(23+(163-23)*k);b=Math.round(42+(242-42)*k);}else{const k=(t-0.5)/0.5;r=Math.round(46+(250-46)*k);g=Math.round(163+(204-163)*k);b=Math.round(242+(21-242)*k);}return 'rgb('+r+','+g+','+b+')';}
function drawLiqHeat(){const v=fitCanvas('liqHeatMap');if(!v)return;const {x,W,H}=v;x.clearRect(0,0,W,H);x.fillStyle='#fff';x.fillRect(0,0,W,H);const d=liqMapData;if(!d||!d.prices||!d.intensity_grid||!d.prices.length){x.fillStyle='#64748b';x.font='13px sans-serif';x.fillText('暂无清算地图数据',16,24);return;}const rows=d.prices.length,cols=((d.intensity_grid&&d.intensity_grid[0])?d.intensity_grid[0].length:0);if(rows<2||cols<1){x.fillStyle='#64748b';x.font='13px sans-serif';x.fillText('暂无清算地图数据',16,24);return;}const pts=[];for(let ri=0;ri<rows;ri++){const p=Number(d.prices[ri]||0);if(!(p>0))continue;let s=0;for(let ci=0;ci<cols;ci++)s+=Math.max(0,Number((d.intensity_grid[ri]||[])[ci]||0));if(s>0)pts.push({p:p,v:s});}if(!pts.length){x.fillStyle='#64748b';x.font='13px sans-serif';x.fillText('暂无清算地图数据',16,24);return;}const padL=66,padR=20,padT=16,padB=42,pw=W-padL-padR,ph=H-padT-padB,by=padT+ph;const minP=pts[0].p,maxP=pts[pts.length-1].p,span=Math.max(1e-6,maxP-minP),maxV=Math.max(1,...pts.map(it=>it.v));const sx=v=>padL+((v-minP)/span)*pw,sy=v=>by-(v/maxV)*ph;x.strokeStyle='#e2e8f0';x.strokeRect(padL,padT,pw,ph);x.font='12px sans-serif';for(let i=0;i<=4;i++){const y=padT+ph*(i/4),val=maxV*(1-i/4);x.strokeStyle='#e5e7eb';x.beginPath();x.moveTo(padL,y);x.lineTo(W-padR,y);x.stroke();x.fillStyle='#475569';x.fillText(fmtAmount(val),6,y+4);}const minLabelGap=12;const approxLabelW=Math.max(36,x.measureText(fmtPrice(minP)).width,x.measureText(fmtPrice(maxP)).width);const maxTicks=Math.max(2,Math.floor(pw/(approxLabelW+minLabelGap)));const tickCount=Math.max(2,Math.min(10,maxTicks));for(let i=0;i<=tickCount;i++){const p=minP+span*(i/tickCount),px=sx(p);x.strokeStyle='#e5e7eb';x.beginPath();x.moveTo(px,by);x.lineTo(px,by+4);x.stroke();const label=fmtPrice(p);const lw=x.measureText(label).width;const tx=Math.max(padL,Math.min(px-lw/2,W-padR-lw));x.fillStyle='#64748b';x.fillText(label,tx,by+18);}const barW=Math.max(2,Math.min(12,pw/Math.max(40,pts.length)));const cp=Number(d.current_price||0);for(const it of pts){const px=sx(it.p),y=sy(it.v);const leftSide=(cp>0)?(it.p<=cp):(it.p<=(minP+maxP)/2);x.fillStyle=leftSide?'rgba(249,115,22,0.78)':'rgba(125,211,252,0.78)';x.fillRect(px-barW/2,y,barW,by-y);}x.lineWidth=1;if(cp>=minP&&cp<=maxP){const cpX=sx(cp);x.strokeStyle='rgba(220,38,38,0.9)';x.setLineDash([5,4]);x.beginPath();x.moveTo(cpX,padT);x.lineTo(cpX,by);x.stroke();x.setLineDash([]);x.fillStyle='#111827';const txt='当前价:'+fmtPrice(cp);const tw=x.measureText(txt).width;x.fillText(txt,Math.max(padL,Math.min(cpX+4,W-padR-tw)),padT+12);}x.fillStyle='#475569';x.fillText('清算金额',padL,padT-4);const xt='价格';const xtw=x.measureText(xt).width;x.fillText(xt,padL+pw/2-xtw/2,H-8);} function renderTable(rows,headers){if(!rows||!rows.length)return '<div class="hint">\u6682\u65e0\u6570\u636e</div>';let html='<table><thead><tr>'+headers.map(h=>'<th>'+h+'</th>').join('')+'</tr></thead><tbody>';for(const r of rows) html+='<tr>'+r.map(c=>'<td>'+c+'</td>').join('')+'</tr>';return html+'</tbody></table>';}
function renderLiqDesc(cfg){
  if(!cfg){const el=document.getElementById('liqDesc');if(el)el.textContent='';return;}
  const levs=String(cfg.leverage_csv||cfg.LeverageCSV||cfg.leverage_levels||cfg.leverage||'20,50,100');
  const ws=String(cfg.weight_csv||cfg.WeightCSV||cfg.leverage_weights||cfg.weights||'0.30,0.40,0.30');
  const mm=Number(cfg.maint_margin||cfg.MaintMargin||cfg.mm||0.005);
  const fs=Number(cfg.funding_scale||cfg.FundingScale||7000);
  const dk=Number(cfg.decay_k||cfg.DecayK||2.2);
  const ns=Number(cfg.neighbor_share||cfg.NeighborShare||0.28);
  const lookback=Number(cfg.lookback_min||cfg.LookbackMin||360);
  const bucket=Number(cfg.bucket_min||cfg.BucketMin||5);
  const step=Number(cfg.price_step||cfg.PriceStep||5);
  const range=Number(cfg.price_range||cfg.PriceRange||400);
  const el=document.getElementById('liqDesc');
  if(el){
    el.textContent='数据源：Binance/Bybit/OKX 公共接口的标记价、OI、资金费率。模型：对 OI 增量按杠杆 '+levs+
      ' 与权重 '+ws+' 分配；多空比例=clamp(0.5+funding×'+fs+',0.2,0.8)；清算价=mark×(1±1/lev∓'+mm.toFixed(4)+')；时间衰减 exp(-'+dk.toFixed(2)+'×age)，邻近价扩散 '+ns.toFixed(2)+
      '。参数：回看 '+lookback+' 分钟，时间桶 '+bucket+' 分钟，价格步长 '+step+'，范围 ±'+range+'。配置入口 /config。';
  }
}
function renderTable(rows,headers){if(!rows||!rows.length)return '<div class="hint">\u6682\u65e0\u6570\u636e</div>';let html='<table><thead><tr>'+headers.map(h=>'<th>'+h+'</th>').join('')+'</tr></thead><tbody>';for(const r of rows) html+='<tr>'+r.map(c=>'<td>'+c+'</td>').join('')+'</tr>';return html+'</tbody></table>';}
function buildHeatBandsFromModel(model){
  if(!model||!model.prices||!model.intensity_grid||!model.prices.length) return [];
  const rows=model.prices.length, cols=((model.intensity_grid&&model.intensity_grid[0])?model.intensity_grid[0].length:0);
  if(rows<1||cols<1) return [];
  const cp=Number(model.current_price||0);
  if(!(cp>0)) return [];
  const pts=[];
  for(let ri=0;ri<rows;ri++){
    const p=Number(model.prices[ri]||0);
    if(!(p>0)) continue;
    let s=0;
    for(let ci=0;ci<cols;ci++) s+=Math.max(0,Number((model.intensity_grid[ri]||[])[ci]||0));
    if(s>0) pts.push({price:p,notional:s});
  }
  if(!pts.length) return [];
  const showBands=[10,20,30,40,50,60,80,100,150];
  return showBands.map(band=>{
    let upPrice=0,upNotional=0,upMax=0,downPrice=0,downNotional=0,downMax=0;
    for(const pt of pts){
      const dist=pt.price-cp;
      if(dist>=0&&dist<=band){
        upNotional+=pt.notional;
        if(pt.notional>upMax){upMax=pt.notional;upPrice=pt.price;}
      }
      if(dist<=0&&Math.abs(dist)<=band){
        downNotional+=pt.notional;
        if(pt.notional>downMax){downMax=pt.notional;downPrice=pt.price;}
      }
    }
    return {band,up_price:upPrice,up_notional_usd:upNotional,down_price:downPrice,down_notional_usd:downNotional};
  });
}
function renderHeatReport(d){
  const bands = buildHeatBandsFromModel(liqMapData);
  if(!bands.length) return '<div class="hint">\u6682\u65e0\u6570\u636e</div>';
  let html = '<table class="heat-table"><thead>' +
    '<tr><th rowspan="2" class="col-threshold">\u70b9\u6570\u9608\u503c</th><th colspan="2">\u4e0a\u65b9\u7a7a\u5355</th><th colspan="2">\u4e0b\u65b9\u591a\u5355</th></tr>' +
    '<tr><th class="col-up-price">\u6e05\u7b97\u4ef7\u683c</th><th class="col-up-size">\u6e05\u7b97\u89c4\u6a21</th><th class="col-down-price">\u6e05\u7b97\u4ef7\u683c</th><th class="col-down-size">\u6e05\u7b97\u89c4\u6a21</th></tr>' +
    '</thead><tbody>';
  const toScale = n => {n=Number(n||0);const a=Math.abs(n);if(a>=1e8)return (n/1e8).toFixed(2)+'亿';if(a>=1e4)return (n/1e4).toFixed(2)+'万';return n.toFixed(2);};
  for(const b of bands){
    html += '<tr>' +
      '<td class="col-threshold">'+b.band+'\u70b9\u5185</td>' +
      '<td class="col-up-price">'+(Number(b.up_price)>0?fmtPrice(b.up_price):'-')+'</td>' +
      '<td class="col-up-size">'+toScale(b.up_notional_usd)+'</td>' +
      '<td class="col-down-price">'+(Number(b.down_price)>0?fmtPrice(b.down_price):'-')+'</td>' +
      '<td class="col-down-size">'+toScale(b.down_notional_usd)+'</td>' +
      '</tr>';
  }
  html += '</tbody></table>';
  return html;
}
async function load(){
  const cfg=await fetch('/api/model-config').then(r=>r.json()).catch(()=>null);
  modelConfig=cfg;
  renderLiqDesc(cfg);
  const lookback=Number(cfg&&cfg.LookbackMin||cfg&&cfg.lookback_min||360);
  const bucket=Number(cfg&&cfg.BucketMin||cfg&&cfg.bucket_min||5);
  const step=Number(cfg&&cfg.PriceStep||cfg&&cfg.price_step||5);
  const range=Number(cfg&&cfg.PriceRange||cfg&&cfg.price_range||400);
  const mapUrl='/api/model/liquidation-map?lookback_min='+lookback+'&bucket_min='+bucket+'&price_step='+step+'&price_range='+range;
  const [r,m]=await Promise.all([fetch('/api/dashboard'),fetch(mapUrl)]);
  const d=await r.json(); liqMapData=await m.json().catch(()=>null);
  currentDays=(d.window_days===0||d.window_days)?d.window_days:currentDays;
  renderActive();
  document.getElementById('status').textContent='\u5f53\u524d\u4ef7: '+fmtPrice(d.current_price)+' | \u5468\u671f: '+windowLabel(d.window_days)+' | \u66f4\u65b0\u65f6\u95f4: '+new Date(d.generated_at).toLocaleString();
  document.getElementById('market').innerHTML=renderTable((d.states||[]).map(s=>[s.exchange,fmtPrice(s.mark_price),s.oi_qty?fmtAmount(s.oi_qty*s.mark_price):'-',s.oi_value_usd?fmtAmount(s.oi_value_usd):'-',s.funding_rate==null?'-':fmt4(s.funding_rate)]),['\u4ea4\u6613\u6240','\u6807\u8bb0\u4ef7','OI\u6570\u91cf','OI\u4ef7\u503cUSD','Funding']);
  document.getElementById('bands').innerHTML=renderHeatReport(d);
  drawLiqHeat();
}
async function openUpgradeModal(){const m=document.getElementById('upgradeModal'),logEl=document.getElementById('upgradeLog'),foot=document.getElementById('upgradeFoot');if(!m||!logEl||!foot)return;m.classList.add('show');logEl.textContent='';foot.textContent='正在触发升级...';const r=await fetch('/api/upgrade/pull',{method:'POST'});const d=await r.json().catch(()=>({error:'response parse failed',output:''}));if(d.error){logEl.textContent=String(d.output||'');foot.textContent='触发失败: '+d.error;return;}foot.textContent='已触发，正在执行...';let stable=0;for(let i=0;i<180;i++){await new Promise(res=>setTimeout(res,1000));const pr=await fetch('/api/upgrade/progress').then(x=>x.json()).catch(()=>null);if(!pr)continue;logEl.textContent=String(pr.log||'');logEl.scrollTop=logEl.scrollHeight;if(pr.done){foot.textContent=(String(pr.exit_code||'')==='0')?'升级完成并已重启':'升级完成，退出码 '+String(pr.exit_code||'?');return;}if(!pr.running)stable++;else stable=0;if(stable>=3){foot.textContent='升级进程已结束（状态未知），请检查日志';return;}}foot.textContent='升级仍在进行，请稍后再看';}
function closeUpgradeModal(){const m=document.getElementById('upgradeModal');if(m)m.classList.remove('show');}
async function doUpgrade(event){if(event)event.preventDefault();openUpgradeModal();return false;}
document.querySelectorAll('button[data-days]').forEach(b=>b.onclick=()=>setWindow(Number(b.dataset.days)));document.addEventListener('keydown',e=>{if(e.key==='0')setWindow(0);if(e.key==='1')setWindow(1);if(e.key==='7')setWindow(7);if(e.key==='3')setWindow(30);});window.addEventListener('resize',()=>drawLiqHeat());setInterval(load,5000);load();(async()=>{try{const r=await fetch('/api/version');const v=await r.json();const el=document.getElementById('globalFooter');if(el)el.textContent='Code by Yuhao@jiansutech.com - '+(v.commit_time||'-')+' - '+(v.commit_id||'-')+' - '+(v.branch||'-');}catch(_){const el=document.getElementById('globalFooter');if(el)el.textContent='Code by Yuhao@jiansutech.com - - - -';}})();
</script><div id="upgradeModal" class="upgrade-modal"><div class="upgrade-card"><div class="upgrade-head"><div class="upgrade-title">升级过程</div><button class="upgrade-close" onclick="closeUpgradeModal()">关闭</button></div><pre id="upgradeLog" class="upgrade-log"></pre><div id="upgradeFoot" class="upgrade-foot">等待开始...</div></div></div><div id="globalFooter" class="footer">Code by Yuhao@jiansutech.com - loading - loading - loading</div></body></html>`

const monitorHTML = `<!doctype html>
<html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>ETH 清算雷区监控</title>
<style>
:root{--bg:#e8edf4;--nav:#0b1220;--nav-border:#243145;--panel:#ffffff;--line:#d8e0ea;--text:#20324a;--muted:#6d7c93;--accent:#39557c;--good:#169b62;--warn:#e58a17;--danger:#dc3d3d}
body{margin:0;background:var(--bg);color:var(--text);font-family:Inter,system-ui,Segoe UI,Arial,sans-serif}
.nav{height:56px;background:var(--nav);border-bottom:1px solid var(--nav-border);display:flex;align-items:center;justify-content:space-between;padding:0 20px;position:sticky;top:0;z-index:10}
.nav-left,.nav-right{display:flex;align-items:center;gap:20px}.brand{font-size:18px;font-weight:700;color:#eef3f9}
.menu a{color:#d6deea;text-decoration:none;font-size:16px;margin-right:18px}.menu a.active{color:#fff;font-weight:700}
.upgrade{color:#fff;font-weight:700;text-decoration:none}.wrap{max-width:1280px;margin:0 auto;padding:14px}
.hero{background:linear-gradient(135deg,#274160,#3d5d86);color:#fff;border-radius:12px;padding:14px 18px;display:flex;justify-content:space-between;align-items:center;gap:16px}
.hero-title{font-size:18px;font-weight:800}.hero-meta{font-size:14px;font-weight:700}
.panel{border:1px solid var(--line);background:var(--panel);margin:10px 0;padding:10px 12px;border-radius:10px;box-shadow:0 1px 2px rgba(15,23,42,.03)}
.topbar{display:flex;justify-content:space-between;align-items:center;gap:12px;flex-wrap:wrap}
.hint{font-size:12px;color:var(--muted)} .btns button{margin-right:8px;background:#fff;color:var(--text);border:1px solid #b6c4d5;padding:8px 12px;border-radius:8px;cursor:pointer}
.btns button.active{background:var(--accent);color:#fff;border-color:var(--accent)}
.grid{display:grid;grid-template-columns:2fr 1fr;gap:10px}.subgrid{display:grid;grid-template-columns:repeat(4,1fr);gap:0;border:1px solid var(--line);border-radius:10px;overflow:hidden}
.card{padding:14px;background:#fff;border-right:1px solid var(--line)}.card:last-child{border-right:0}.card .label{font-size:12px;color:var(--muted);margin-bottom:10px}.card .val{font-size:22px;font-weight:800;line-height:1.05}
.good{color:var(--good)}.warn{color:var(--warn)}.danger{color:var(--danger)}
.sidebox{padding:10px 12px}.sidebox h3,.panel h3{margin:0 0 10px 0}
.state-list{display:grid;gap:6px;font-weight:700}.state-line{display:flex;gap:10px;flex-wrap:wrap}
.main-grid{display:grid;grid-template-columns:1.45fr .9fr;gap:10px}.stack{display:grid;gap:10px}
table{width:100%;border-collapse:collapse}th,td{border-bottom:1px solid var(--line);padding:7px 8px;text-align:center}thead th{background:#5d7291;color:#fff;font-size:13px}
.metric{display:flex;justify-content:space-between;gap:12px;padding:6px 0;border-bottom:1px solid var(--line)}.metric:last-child{border-bottom:0}
.bar-row{display:grid;grid-template-columns:72px 1fr 54px;gap:10px;align-items:center;margin:8px 0}.bar{height:10px;background:#e7edf5;border-radius:999px;overflow:hidden}.fill{height:100%;background:linear-gradient(90deg,#56b6d7,#3f7fb1)}
.triple{display:grid;grid-template-columns:repeat(3,1fr);gap:10px}
.mini{font-size:12px;color:var(--muted)} .big{font-size:16px;font-weight:800}
.upgrade-modal{position:fixed;inset:0;background:rgba(2,6,23,.55);display:none;align-items:center;justify-content:center;z-index:9999}.upgrade-modal.show{display:flex}.upgrade-card{width:min(880px,92vw);max-height:82vh;background:#0b1220;color:#e2e8f0;border:1px solid #334155;border-radius:10px;box-shadow:0 10px 30px rgba(2,6,23,.45);overflow:hidden}.upgrade-head{display:flex;align-items:center;justify-content:space-between;padding:10px 12px;border-bottom:1px solid #334155}.upgrade-title{font-size:14px;font-weight:700}.upgrade-close{background:transparent;border:1px solid #475569;color:#e2e8f0;border-radius:6px;padding:4px 8px;cursor:pointer}.upgrade-log{margin:0;padding:12px;white-space:pre-wrap;overflow:auto;max-height:62vh;font-family:ui-monospace,SFMono-Regular,Menlo,Consolas,monospace;font-size:12px;line-height:1.45}.upgrade-foot{padding:8px 12px;border-top:1px solid #334155;font-size:12px;color:#94a3b8}.footer{margin:18px auto 0 auto;max-width:1200px;padding:10px 12px;font-size:12px;color:#64748b;text-align:center}
@media (max-width:980px){.grid,.main-grid,.triple{grid-template-columns:1fr}.subgrid{grid-template-columns:1fr 1fr}.card:nth-child(2){border-right:0}.hero{align-items:flex-start;flex-direction:column}}
</style></head><body>
<div class="nav"><div class="nav-left"><div class="brand">ETH Liquidation Map</div><div class="menu"><a href="/">清算热区</a><a href="/monitor" class="active">雷区监控</a><a href="/config">模型配置</a><a href="/map">盘口汇总</a><a href="/liquidations">强平清算</a><a href="/bubbles">气泡图</a><a href="/channel">消息通道</a></div></div><div class="nav-right"><a href="#" class="upgrade" onclick="return doUpgrade(event)">升级</a></div></div>
<div class="wrap">
<div class="hero"><div class="hero-title">ETH 清算雷区监控（Beta）</div><div id="heroTime" class="hero-meta">--</div></div>
<div class="panel topbar"><div class="hint">按 0 / 1 / 7 / 3 快捷切换 日内 / 1天 / 7天 / 30天</div><div class="btns"><button data-days="0">日内</button><button data-days="1">1天</button><button data-days="7">7天</button><button data-days="30">30天</button></div></div>
<div class="grid">
<div class="panel"><h3>顶部结论卡</h3><div id="topCards" class="subgrid"></div></div>
<div class="panel sidebox"><h3>市场状态</h3><div id="marketState" class="state-list"></div></div>
</div>
<div class="main-grid">
<div class="panel"><h3>清算热区速报</h3><div id="heatReport"></div></div>
<div class="stack"><div class="panel"><h3>失衡统计</h3><div id="imbalanceStats"></div></div><div class="panel"><h3>密度分层</h3><div id="densityLayers"></div></div><div class="panel"><h3>变化跟踪（较上一时点）</h3><div id="changeTrack"></div></div></div>
</div>
<div class="triple">
<div class="panel"><h3>最长柱 / 核心磁区</h3><div id="coreZone"></div></div>
<div class="panel"><h3>交易所贡献拆分（50点内）</h3><div id="exchangeContrib"></div></div>
<div class="panel"><h3>预警与已发生清算</h3><div id="alerts"></div></div>
</div>
</div>
<script>
let currentDays=1;
function fmtPrice(n){return Number(n).toLocaleString('zh-CN',{minimumFractionDigits:1,maximumFractionDigits:1})}
function fmtAmount(n){n=Number(n);if(!isFinite(n))return '-';const a=Math.abs(n);if(a>=1e8)return (n/1e8).toFixed(2)+'亿';if(a>=1e6)return (n/1e6).toFixed(2)+'百万';return (n/1e4).toFixed(1)+'万';}
function fmtFunding(n){const v=Number(n);if(!isFinite(v))return '-';return v.toFixed(4)}
function renderActive(){document.querySelectorAll('button[data-days]').forEach(b=>b.classList.toggle('active',Number(b.dataset.days)===currentDays));}
async function setWindow(days){currentDays=days;await fetch('/api/window',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({days})});renderActive();load();}
function metricRow(label,val,cls=''){return '<div class="metric"><span>'+label+'</span><strong class="'+cls+'">'+val+'</strong></div>'}
function renderTopCards(d){const a=d.analytics||{},t=a.top||{},mk=a.market||{};const cards=[{label:'ETH现价',val:fmtPrice(d.current_price)},{label:'短线偏向',val:t.short_bias||'-',cls:(String(t.short_bias||'').includes('上')?'warn':(String(t.short_bias||'').includes('下')?'good':''))},{label:'20点内失衡',val:fmtAmount(Math.abs(Number(t.bias20_delta||0))),cls:Number(t.bias20_delta||0)>=0?'warn':'good'},{label:'50点内失衡',val:t.bias50_label||'-',cls:(String(t.bias50_label||'').includes('上')?'warn':(String(t.bias50_label||'').includes('下')?'good':''))}];document.getElementById('topCards').innerHTML=cards.map(c=>'<div class="card"><div class="label">'+c.label+'</div><div class="val '+(c.cls||'')+'">'+c.val+'</div></div>').join('');document.getElementById('marketState').innerHTML='<div class="state-line"><span>Binance OI</span><strong>'+fmtAmount(mk.binance_oi_usd)+'</strong></div><div class="state-line"><span>Bybit OI</span><strong>'+fmtAmount(mk.bybit_oi_usd)+'</strong></div><div class="state-line"><span>OKX OI</span><strong>'+fmtAmount(mk.okx_oi_usd)+'</strong></div><div class="state-line"><span>Funding</span><strong>'+fmtFunding(mk.avg_funding)+'</strong></div>'}
function renderHeatReport(d){const bands=(d.bands||[]).filter(b=>[10,20,30,50,100,150].includes(Number(b.band)));if(!bands.length){document.getElementById('heatReport').innerHTML='<div class="hint">暂无数据</div>';return;}let html='<table><thead><tr><th>点数阈值</th><th>上方空单清算价</th><th>上方规模</th><th>下方多单清算价</th><th>下方规模</th></tr></thead><tbody>';for(const b of bands){html+='<tr><td>'+b.band+'点内</td><td>'+fmtPrice(b.up_price)+'</td><td class="warn">'+fmtAmount(b.up_notional_usd)+'</td><td>'+fmtPrice(b.down_price)+'</td><td class="good">'+fmtAmount(b.down_notional_usd)+'</td></tr>';}html+='</tbody></table>';document.getElementById('heatReport').innerHTML=html}
function renderImbalance(d){const rows=((d.analytics||{}).imbalance_stats)||[];document.getElementById('imbalanceStats').innerHTML=rows.length?rows.map(r=>metricRow(r.band+'点内','上'+fmtAmount(r.up_notional_usd)+' / 下'+fmtAmount(r.down_notional_usd)+' / '+(r.verdict||'-'),String(r.verdict||'').includes('上')?'warn':(String(r.verdict||'').includes('下')?'good':''))).join(''):'<div class="hint">暂无数据</div>'}
function renderDensity(d){const rows=((d.analytics||{}).density_layers)||[];if(!rows.length){document.getElementById('densityLayers').innerHTML='<div class="hint">暂无数据</div>';return;}let html='<table><thead><tr><th>区间</th><th>上方空单</th><th>下方多单</th></tr></thead><tbody>';for(const r of rows){html+='<tr><td>'+r.label+'</td><td>'+fmtAmount(r.up_notional_usd)+'</td><td>'+fmtAmount(r.down_notional_usd)+'</td></tr>';}html+='</tbody></table>';document.getElementById('densityLayers').innerHTML=html}
function renderTrack(d){const t=((d.analytics||{}).change_tracking)||{};document.getElementById('changeTrack').innerHTML=metricRow('20点内上方空单',fmtAmount(t.up20_delta_usd),Number(t.up20_delta_usd)>=0?'warn':'good')+metricRow('20点内下方多单',fmtAmount(t.down20_delta_usd),Number(t.down20_delta_usd)>=0?'warn':'good')+metricRow('最长柱规模',fmtAmount(t.longest_delta_usd),Number(t.longest_delta_usd)>=0?'warn':'good')+metricRow('Funding',fmtFunding(t.funding_delta),Number(t.funding_delta)>=0?'warn':'good')}
function renderCore(d){const c=((d.analytics||{}).core_zone)||{};document.getElementById('coreZone').innerHTML=metricRow('上方空单最长柱',fmtPrice(c.up_price)+' / '+fmtAmount(c.up_notional_usd),'warn')+metricRow('下方多单最长柱',fmtPrice(c.down_price)+' / '+fmtAmount(c.down_notional_usd),'good')+metricRow('距离最近强区',fmtPrice(c.nearest_strong_price)+' / '+(Number(c.nearest_distance||0).toFixed(1))+'点',String(c.nearest_side||'').includes('上')?'warn':'good')+metricRow('最近强区方向',c.nearest_side||'-')}
function renderContrib(d){const rows=((d.analytics||{}).exchange_contrib)||[];if(!rows.length){document.getElementById('exchangeContrib').innerHTML='<div class="hint">暂无数据</div>';return;}document.getElementById('exchangeContrib').innerHTML=rows.map(r=>'<div class="bar-row"><div>'+r.exchange+'</div><div class="bar"><div class="fill" style="width:'+Math.max(4,Number(r.share||0)*100)+'%"></div></div><div>'+(Number(r.share||0)*100).toFixed(0)+'%</div></div>').join('')+'<div class="mini">主贡献交易所：'+(((d.analytics||{}).dominant_exchange)||'-')+'</div>'}
function renderAlerts(d){const a=((d.analytics||{}).alert)||{};document.getElementById('alerts').innerHTML=metricRow('当前预警',a.level||'-',String(a.level||'').includes('预警')?'danger':'')+metricRow('最近事件',[(a.recent_exchange||'-'),(a.recent_side||'-'),(a.recent_price?fmtPrice(a.recent_price):'-')].join(' / '))+metricRow('近1分钟已发生清算',fmtAmount(a.recent_1m_usd),'warn')+metricRow('建议',a.suggestion||'-')}
async function load(){const r=await fetch('/api/dashboard');const d=await r.json();currentDays=(d.window_days===0||d.window_days)?d.window_days:currentDays;renderActive();document.getElementById('heroTime').textContent=new Date(d.generated_at).toLocaleString('zh-CN',{hour12:false});renderTopCards(d);renderHeatReport(d);renderImbalance(d);renderDensity(d);renderTrack(d);renderCore(d);renderContrib(d);renderAlerts(d);}
async function openUpgradeModal(){const m=document.getElementById('upgradeModal'),logEl=document.getElementById('upgradeLog'),foot=document.getElementById('upgradeFoot');if(!m||!logEl||!foot)return;m.classList.add('show');logEl.textContent='';foot.textContent='正在触发升级...';const r=await fetch('/api/upgrade/pull',{method:'POST'});const d=await r.json().catch(()=>({error:'response parse failed',output:''}));if(d.error){logEl.textContent=String(d.output||'');foot.textContent='触发失败: '+d.error;return;}foot.textContent='已触发，正在执行...';let stable=0;for(let i=0;i<180;i++){await new Promise(res=>setTimeout(res,1000));const pr=await fetch('/api/upgrade/progress').then(x=>x.json()).catch(()=>null);if(!pr)continue;logEl.textContent=String(pr.log||'');logEl.scrollTop=logEl.scrollHeight;if(pr.done){foot.textContent=(String(pr.exit_code||'')==='0')?'升级完成并已重启':'升级完成，退出码 '+String(pr.exit_code||'?');return;}if(!pr.running)stable++;else stable=0;if(stable>=3){foot.textContent='升级进程已结束（状态未知），请检查日志';return;}}foot.textContent='升级仍在进行，请稍后再看';}
function closeUpgradeModal(){const m=document.getElementById('upgradeModal');if(m)m.classList.remove('show');}
async function doUpgrade(event){if(event)event.preventDefault();openUpgradeModal();return false;}
document.querySelectorAll('button[data-days]').forEach(b=>b.onclick=()=>setWindow(Number(b.dataset.days)));document.addEventListener('keydown',e=>{if(e.key==='0')setWindow(0);if(e.key==='1')setWindow(1);if(e.key==='7')setWindow(7);if(e.key==='3')setWindow(30);});setInterval(load,5000);load();(async()=>{try{const r=await fetch('/api/version');const v=await r.json();const el=document.getElementById('globalFooter');if(el)el.textContent='Code by Yuhao@jiansutech.com - '+(v.commit_time||'-')+' - '+(v.commit_id||'-')+' - '+(v.branch||'-');}catch(_){const el=document.getElementById('globalFooter');if(el)el.textContent='Code by Yuhao@jiansutech.com - - - -';}})();
</script><div id="upgradeModal" class="upgrade-modal"><div class="upgrade-card"><div class="upgrade-head"><div class="upgrade-title">升级过程</div><button class="upgrade-close" onclick="closeUpgradeModal()">关闭</button></div><pre id="upgradeLog" class="upgrade-log"></pre><div id="upgradeFoot" class="upgrade-foot">等待开始...</div></div></div><div id="globalFooter" class="footer">Code by Yuhao@jiansutech.com - loading - loading - loading</div></body></html>`

const mapHTML = `<!doctype html>
<html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>盘口汇总</title>
<style>
body{margin:0;background:#f5f7fb;color:#1f2937;font-family:Inter,system-ui,Segoe UI,Arial,sans-serif}
.nav{height:56px;background:#0b1220;border-bottom:1px solid #243145;display:flex;align-items:center;justify-content:space-between;padding:0 20px;position:sticky;top:0;z-index:10}
.nav-left,.nav-right{display:flex;align-items:center;gap:20px}.brand{font-size:18px;font-weight:700;color:#eef3f9}
.menu a{color:#d6deea;text-decoration:none;font-size:16px;margin-right:18px}.menu a.active{color:#fff;font-weight:700}
.upgrade{color:#fff;font-weight:700;text-decoration:none}.wrap{width:100%;max-width:none;margin:0;padding:12px}
.panel{border:1px solid #dce3ec;background:#fff;margin:14px 0;padding:16px;border-radius:10px;box-shadow:0 1px 2px rgba(15,23,42,.04)}
.row{display:flex;gap:12px;align-items:center;flex-wrap:wrap;justify-content:space-between}
.btns button,.mode button{background:#fff;color:#111827;border:1px solid #cbd5e1;padding:8px 14px;border-radius:8px;cursor:pointer}
.btns button.active,.mode button.active{background:#22c55e;color:#fff;border-color:#22c55e}
.small{font-size:12px;color:#6b7280}.legend{display:flex;gap:14px;align-items:center;flex-wrap:wrap;font-size:12px;color:#6b7280;margin-top:8px}
.tune{display:flex;gap:10px;align-items:center;flex-wrap:wrap;margin-top:8px}
.tune label{font-size:12px;color:#475569}
.tune select{height:30px;border:1px solid #cbd5e1;border-radius:6px;background:#fff;color:#111827;padding:0 8px}
.swatch{display:inline-block;width:10px;height:10px;border-radius:2px;margin-right:6px}
.meta{font-size:12px;color:#4b5563}.weights{font-size:12px;color:#334155;font-weight:700}.wsline{font-size:13px;color:#0f172a;font-weight:700;margin-top:2px}
.chart-wrap{display:flex;gap:14px;align-items:stretch}.chart-main{flex:1;min-width:0}
canvas{width:100%;height:760px;display:block;border:1px solid #e5e7eb;border-radius:10px;background:#fff}
.merge-grid{display:grid;grid-template-columns:repeat(3,minmax(180px,1fr));gap:10px;margin-bottom:10px}
.merge-card{border:1px solid #e2e8f0;border-radius:8px;padding:10px;background:#f8fafc}.merge-label{font-size:12px;color:#64748b}.merge-val{font-size:20px;font-weight:700;color:#0f172a}
#depthChart{height:312px}
.event-wrap{margin-top:12px;border:1px solid #e2e8f0;border-radius:10px;background:#f8fafc;padding:10px}
.event-title{font-size:16px;font-weight:700;color:#0f172a;margin-bottom:8px}
.event-grid{display:grid;grid-template-columns:1fr 1fr;gap:12px}
.event-card{border:1px solid #dbe3ef;border-radius:8px;background:#fff;padding:8px;max-height:360px;overflow:auto}
.event-card h4{margin:0 0 6px 0;font-size:14px}
.event-card table{width:100%;border-collapse:collapse}
.event-card th,.event-card td{font-size:12px;border-top:1px solid #eef2f7;padding:4px 6px;text-align:center}
.event-card thead th{border-top:0;color:#64748b}
.upgrade-modal{position:fixed;inset:0;background:rgba(2,6,23,.55);display:none;align-items:center;justify-content:center;z-index:9999}.upgrade-modal.show{display:flex}.upgrade-card{width:min(880px,92vw);max-height:82vh;background:#0b1220;color:#e2e8f0;border:1px solid #334155;border-radius:10px;box-shadow:0 10px 30px rgba(2,6,23,.45);overflow:hidden}.upgrade-head{display:flex;align-items:center;justify-content:space-between;padding:10px 12px;border-bottom:1px solid #334155}.upgrade-title{font-size:14px;font-weight:700}.upgrade-close{background:transparent;border:1px solid #475569;color:#e2e8f0;border-radius:6px;padding:4px 8px;cursor:pointer}.upgrade-log{margin:0;padding:12px;white-space:pre-wrap;overflow:auto;max-height:62vh;font-family:ui-monospace,SFMono-Regular,Menlo,Consolas,monospace;font-size:12px;line-height:1.45}.upgrade-foot{padding:8px 12px;border-top:1px solid #334155;font-size:12px;color:#94a3b8}.footer{margin:18px auto 0 auto;max-width:1200px;padding:10px 12px;font-size:12px;color:#64748b;text-align:center}</style></head><body>
<div class="nav"><div class="nav-left"><div class="brand">ETH Liquidation Map</div><div class="menu"><a href="/">清算热区</a><a href="/config">模型配置</a><a href="/monitor">雷区监控</a><a href="/map" class="active">盘口汇总</a><a href="/liquidations">强平清算</a><a href="/bubbles">气泡图</a><a href="/channel">消息通道</a></div></div><div class="nav-right"><a href="#" class="upgrade" onclick="return doUpgrade(event)">升级</a></div></div>
<div class="wrap">
  <div class="panel">
    <div class="row">
      <div>
        <h2 style="margin:0;color:#111827">盘口汇总</h2>
        <div class="small">覆盖 Binance / Bybit / OKX 盘口与流动性</div>
      </div>
      <div class="row"><div class="mode"><button data-mode="weighted">加权模式</button><button data-mode="merged">合并模式</button></div></div>
    </div>
    <div id="meta" class="meta"></div>
    <div id="weights" class="weights"></div>
    <div id="wsStatus" class="wsline"></div>
    <div class="legend"><span><i class="swatch" style="background:#8b5cf6"></i>Binance</span><span><i class="swatch" style="background:#eab308"></i>OKX</span><span><i class="swatch" style="background:#67e8f9"></i>Bybit</span></div>
    <div class="tune">
      <label>半衰期
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
    <div class="row" style="justify-content:flex-start"><h3 id="mergeTitle" style="margin:0">合并盘口统计</h3><div id="mergeHint" class="small">基于成交与 OI 估计</div></div>
    <div id="mergeStats" class="merge-grid"></div>
    <canvas id="depthChart" width="1600" height="312"></canvas>
    <div class="event-wrap">
      <div class="event-title">价格事件</div>
      <div class="event-grid">
        <div class="event-card"><h4 style="color:#16a34a">买盘事件</h4><div id="bidEvents"></div></div>
        <div class="event-card"><h4 style="color:#dc2626">卖盘事件</h4><div id="askEvents"></div></div>
      </div>
    </div>
  </div>
</div>
<script>
let currentMode='weighted',dashboard=null,orderbook=null,depthState=null,isDraggingDepth=false,depthLastX=0,persistedEvents={bid:[],ask:[]};
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
function renderActive(){document.querySelectorAll('button[data-mode]').forEach(b=>b.classList.toggle('active',(b.dataset.mode||'')===currentMode));}
function resetDepthMemory(){depthMemory.bidGhost={};depthMemory.askGhost={};depthMemory.bidMax={};depthMemory.askMax={};depthMemory.lastUpdate=0;depthState=null;depthEvents.bidTrack={};depthEvents.askTrack={};depthEvents.bidList=[];depthEvents.askList=[];persistedEvents={bid:[],ask:[]};}
function setMode(mode){const m=(mode==='merged')?'merged':'weighted';if(currentMode===m)return;currentMode=m;resetDepthMemory();try{localStorage.setItem('orderbook_mode',currentMode);}catch(_){ }renderActive();load();}
function loadModeFromStorage(){try{const m=localStorage.getItem('orderbook_mode');if(m==='weighted'||m==='merged')currentMode=m;}catch(_){ }}
function renderMergedPanel(){const el=document.getElementById('mergeStats');const m=orderbook&&orderbook.merged;const t=document.getElementById('mergeTitle');const h=document.getElementById('mergeHint');if(t)t.textContent=currentMode==='weighted'?'加权盘口统计':'合并盘口统计';if(h)h.textContent=currentMode==='weighted'?'基于成交与 OI 估计':'多交易所合并盘口与价差';if(!m){el.innerHTML='<div class=\"small\">暂无盘口数据</div>';return;}const spread=(Number(m.best_ask||0)-Number(m.best_bid||0));el.innerHTML='<div class=\"merge-card\"><div class=\"merge-label\">Best Bid</div><div class=\"merge-val\">'+fmtPrice(m.best_bid)+'</div></div>'+'<div class=\"merge-card\"><div class=\"merge-label\">Best Ask</div><div class=\"merge-val\">'+fmtPrice(m.best_ask)+'</div></div>'+'<div class=\"merge-card\"><div class=\"merge-label\">Mid / Spread</div><div class=\"merge-val\">'+fmtPrice(m.mid)+' <span style=\"font-size:12px;color:#64748b\">('+fmtPrice(spread)+')</span></div></div>';}
function fmtEventTime(ts){try{return new Date(ts).toLocaleTimeString('zh-CN',{hour12:false});}catch(_){return '-';}}
function fmtDur(ms){const s=Math.max(0,Math.round(Number(ms||0)/1000));return s+'s';}
function eventTable(list){if(!list||!list.length)return '<div class=\"small\">暂无事件</div>';let h='<table><thead><tr><th>价格</th><th>峰值</th><th>持续时间</th><th>时间</th></tr></thead><tbody>';for(const e of list.slice(0,8)){h+='<tr><td>'+fmtPrice(e.price)+'</td><td>'+fmtAmount(e.peak)+'</td><td>'+fmtDur(e.dur_ms)+'</td><td>'+fmtEventTime(e.ts)+'</td></tr>';}return h+'</tbody></table>';}
function renderEventTables(){const be=document.getElementById('bidEvents'),ae=document.getElementById('askEvents');if(be)be.innerHTML=eventTable(persistedEvents.bid||[]);if(ae)ae.innerHTML=eventTable(persistedEvents.ask||[]);}
function setPersistedEvents(rows){const cutoff=Date.now()-30*60*1000;const bid=[],ask=[];for(const r of (rows||[])){const ts=Number(r.event_ts||0);if(ts<cutoff)continue;const side=(r.side||'').toLowerCase();const item={price:Number(r.price||0),peak:Number(r.peak||0),dur_ms:Number(r.duration_ms||0),ts:ts};if(side==='bid')bid.push(item);if(side==='ask')ask.push(item);}persistedEvents.bid=bid;persistedEvents.ask=ask;}
function syncDepthCanvas(c){const rect=c.getBoundingClientRect();const dpr=window.devicePixelRatio||1;const W=Math.max(320,Math.floor(rect.width));const H=Math.max(240,Math.floor(rect.height));const rw=Math.floor(W*dpr),rh=Math.floor(H*dpr);if(c.width!==rw||c.height!==rh){c.width=rw;c.height=rh;}const x=c.getContext('2d');x.setTransform(dpr,0,0,dpr,0,0);return{x,W,H};}
function clampDepthView(minP,maxP){if(!depthState||!(depthState.fullMax>depthState.fullMin))return[minP,maxP];const fullMin=depthState.fullMin,fullMax=depthState.fullMax,fullSpan=Math.max(1e-6,fullMax-fullMin);const minSpan=Math.max(2,fullSpan*0.05);let span=Math.max(minSpan,maxP-minP);span=Math.min(fullSpan,span);let vMin=minP,vMax=vMin+span;if(vMin<fullMin){vMin=fullMin;vMax=vMin+span;}if(vMax>fullMax){vMax=fullMax;vMin=vMax-span;}return[vMin,vMax];}
function initDepthView(allPrices,cur){const rawMin=Math.min(...allPrices,cur),rawMax=Math.max(...allPrices,cur);const rawSpan=Math.max(2,rawMax-rawMin);const pad=Math.max(1,rawSpan*0.06);const dataMin=rawMin-pad,dataMax=rawMax+pad;if(!depthState||!(depthState.fullMax>depthState.fullMin)){depthState={fullMin:dataMin,fullMax:dataMax,viewMin:dataMin,viewMax:dataMax,padL:64,padR:24,padT:20,padB:44,W:0,H:0};[depthState.viewMin,depthState.viewMax]=clampDepthView(depthState.viewMin,depthState.viewMax);return;}depthState.fullMin=dataMin;depthState.fullMax=dataMax;const span=(depthState.viewMax>depthState.viewMin)?(depthState.viewMax-depthState.viewMin):(dataMax-dataMin);let vMin=cur-span/2,vMax=cur+span/2;[vMin,vMax]=clampDepthView(vMin,vMax);depthState.viewMin=vMin;depthState.viewMax=vMax;}
function drawDepth(){const c=document.getElementById('depthChart');const v=syncDepthCanvas(c),x=v.x,W=v.W,H=v.H;x.clearRect(0,0,W,H);x.fillStyle='#fff';x.fillRect(0,0,W,H);const m=orderbook&&orderbook.merged;const bids=(m&&m.bids)||[];const asks=(m&&m.asks)||[];const cur=Number((m&&m.mid)||0)||Number((dashboard&&dashboard.current_price)||0);if(!(cur>0)||(!bids.length&&!asks.length)){x.fillStyle='#6b7280';x.font='14px sans-serif';x.fillText('暂无盘口数据',20,24);return;}const all=bids.concat(asks).map(v=>({p:Number(v.price||0),n:Number(v.qty||0)*Number(v.price||0)})).filter(v=>v.p>0&&v.n>0);if(!all.length){x.fillStyle='#6b7280';x.font='14px sans-serif';x.fillText('暂无盘口数据',20,24);return;}initDepthView(all.map(v=>v.p),cur);const padL=64,padR=24,padT=20,padB=44,pw=W-padL-padR,ph=H-padT-padB,by=padT+ph;depthState.padL=padL;depthState.padR=padR;depthState.padT=padT;depthState.padB=padB;depthState.W=W;depthState.H=H;const minP=depthState.viewMin,maxP=depthState.viewMax,span=Math.max(1e-6,maxP-minP);const bidNow=levelMap(bids),askNow=levelMap(asks);const visible=all.filter(v=>v.p>=minP&&v.p<=maxP);const ghostVals=[];for(const [k,vv] of Object.entries(depthMemory.bidGhost)){const p=Number(k);if(p>=minP&&p<=maxP&&vv>0)ghostVals.push(vv);}for(const [k,vv] of Object.entries(depthMemory.askGhost)){const p=Number(k);if(p>=minP&&p<=maxP&&vv>0)ghostVals.push(vv);}const maxVals=[];for(const [k,o] of Object.entries(depthMemory.bidMax)){const p=Number(k);if(p>=minP&&p<=maxP&&o&&o.v>0)maxVals.push(o.v);}for(const [k,o] of Object.entries(depthMemory.askMax)){const p=Number(k);if(p>=minP&&p<=maxP&&o&&o.v>0)maxVals.push(o.v);}const maxN=Math.max(1,...(visible.length?visible.map(v=>v.n):all.map(v=>v.n)),...(ghostVals.length?ghostVals:[1]),...(maxVals.length?maxVals:[1]));const sx=v=>padL+((v-minP)/span)*pw,sy=v=>by-(v/maxN)*ph;x.strokeStyle='#e5e7eb';x.lineWidth=1;x.font='12px sans-serif';for(let i=0;i<=4;i++){const y=padT+ph*(i/4),val=maxN*(1-i/4);x.beginPath();x.moveTo(padL,y);x.lineTo(W-padR,y);x.stroke();x.fillStyle='#475569';x.fillText(fmtAmount(val),6,y+4);}const minLabelGap=12;const approxLabelW=Math.max(36,x.measureText(fmtPrice(minP)).width,x.measureText(fmtPrice(maxP)).width);const maxTicks=Math.max(2,Math.floor(pw/(approxLabelW+minLabelGap)));const tickCount=Math.max(2,Math.min(10,maxTicks));for(let i=0;i<=tickCount;i++){const p=minP+span*(i/tickCount),px=sx(p);x.strokeStyle='#e5e7eb';x.beginPath();x.moveTo(px,by);x.lineTo(px,by+4);x.stroke();const label=fmtPrice(p);const lw=x.measureText(label).width;const tx=Math.max(padL,Math.min(px-lw/2,W-padR-lw));x.fillStyle='#64748b';x.fillText(label,tx,by+18);}if(cur>=minP&&cur<=maxP){const cp=sx(cur);x.strokeStyle='#dc2626';x.setLineDash([6,4]);x.beginPath();x.moveTo(cp,padT);x.lineTo(cp,by);x.stroke();x.setLineDash([]);x.fillStyle='#111827';x.fillText('当前价 '+fmtPrice(cur),Math.max(padL,Math.min(cp-34,W-150)),padT-4);}const barCount=Math.max(1,visible.length);const barW=Math.max(1,Math.min(12,pw/Math.max(50,barCount)));for(const [k,n] of Object.entries(depthMemory.bidGhost)){const p=Number(k);if(!(p>=minP&&p<=maxP&&n>0&&p<=cur))continue;const px=sx(p),y=sy(n);x.fillStyle='rgba(22,163,74,0.18)';x.fillRect(px-barW/2,y,barW,by-y);}for(const [k,n] of Object.entries(depthMemory.askGhost)){const p=Number(k);if(!(p>=minP&&p<=maxP&&n>0&&p>=cur))continue;const px=sx(p),y=sy(n);x.fillStyle='rgba(220,38,38,0.16)';x.fillRect(px-barW/2,y,barW,by-y);}for(const [k,o] of Object.entries(depthMemory.bidMax)){const p=Number(k),n=Number(o&&o.v||0);if(!(p>=minP&&p<=maxP&&n>0&&p<=cur))continue;const px=sx(p),y=sy(n);x.strokeStyle='rgba(22,163,74,0.85)';x.lineWidth=1.3;x.beginPath();x.moveTo(px-barW/2,y);x.lineTo(px+barW/2,y);x.stroke();}for(const [k,o] of Object.entries(depthMemory.askMax)){const p=Number(k),n=Number(o&&o.v||0);if(!(p>=minP&&p<=maxP&&n>0&&p>=cur))continue;const px=sx(p),y=sy(n);x.strokeStyle='rgba(220,38,38,0.85)';x.lineWidth=1.3;x.beginPath();x.moveTo(px-barW/2,y);x.lineTo(px+barW/2,y);x.stroke();}x.lineWidth=1;
const exColors={binance:'rgba(139,92,246,0.75)',okx:'rgba(234,179,8,0.72)',bybit:'rgba(103,232,249,0.72)'};
if(currentMode==='weighted'&&m&&m.per_exchange){const order=['binance','okx','bybit'];for(const side of ['bids','asks']){for(const ex of order){const ls=((m.per_exchange[ex]||{})[side])||[];for(const lv of ls){const p=Number(lv.price||0),n=Number(lv.qty||0)*p;if(!(p>=minP&&p<=maxP&&n>0&&((side==='bids'&&p<=cur)||(side==='asks'&&p>=cur))))continue;const px=sx(p),y=sy(n);x.fillStyle=exColors[ex];x.fillRect(px-barW/2,y,barW,by-y);}}}}
else {for(const b of bids){const p=Number(b.price||0),n=Number(b.qty||0)*p;if(!(p>=minP&&p<=maxP&&n>0&&p<=cur))continue;const px=sx(p),y=sy(n);x.fillStyle='rgba(22,163,74,0.75)';x.fillRect(px-barW/2,y,barW,by-y);}for(const a of asks){const p=Number(a.price||0),n=Number(a.qty||0)*p;if(!(p>=minP&&p<=maxP&&n>0&&p>=cur))continue;const px=sx(p),y=sy(n);x.fillStyle='rgba(220,38,38,0.72)';x.fillRect(px-barW/2,y,barW,by-y);}}
x.fillStyle='#16a34a';x.fillText('Bid 深度',padL,14);x.fillStyle='#dc2626';x.fillText('Ask 深度',padL+100,14);x.fillStyle='#64748b';x.fillText('半衰期 '+Math.round(depthMemory.halfLifeMs/1000)+'s  滚动窗口 '+Math.round(depthMemory.rollingWindowMs/60000)+' 分钟',padL+220,14);x.fillText('X 轴价格 / Y 轴挂单金额',W-190,14);}
function bindDepthInteraction(){const c=document.getElementById('depthChart');if(!c||c.dataset.bound==='1')return;c.dataset.bound='1';c.addEventListener('wheel',e=>{if(!depthState||!(depthState.viewMax>depthState.viewMin))return;e.preventDefault();const rect=c.getBoundingClientRect();const xPos=e.clientX-rect.left;const s=depthState,pw=s.W-s.padL-s.padR;if(pw<=0)return;const ratio=Math.max(0,Math.min(1,(xPos-s.padL)/pw));const focus=s.viewMin+(s.viewMax-s.viewMin)*ratio;const factor=e.deltaY<0?0.88:1.14;let vMin=focus-(focus-s.viewMin)*factor,vMax=vMin+(s.viewMax-s.viewMin)*factor;[vMin,vMax]=clampDepthView(vMin,vMax);s.viewMin=vMin;s.viewMax=vMax;drawDepth();},{passive:false});c.addEventListener('mousedown',e=>{if(e.button!==0||!depthState)return;isDraggingDepth=true;depthLastX=e.clientX;});window.addEventListener('mousemove',e=>{if(!isDraggingDepth||!depthState)return;const s=depthState,pw=s.W-s.padL-s.padR;if(pw<=0)return;const dx=e.clientX-depthLastX;depthLastX=e.clientX;const dp=(dx/pw)*(s.viewMax-s.viewMin);let vMin=s.viewMin-dp,vMax=s.viewMax-dp;[vMin,vMax]=clampDepthView(vMin,vMax);s.viewMin=vMin;s.viewMax=vMax;drawDepth();});window.addEventListener('mouseup',()=>{isDraggingDepth=false;});window.addEventListener('mouseleave',()=>{isDraggingDepth=false;});window.addEventListener('resize',()=>drawDepth());}
function buildShares(states){const order=['binance','okx','bybit'];const out={};let total=0;for(const ex of order){const s=(states||[]).find(v=>(v.exchange||'').toLowerCase()===ex);const oi=Number((s&&s.oi_value_usd)||0);out[ex]=oi;total+=oi;}if(total<=0){for(const ex of order)out[ex]=1/order.length;}else{for(const ex of order)out[ex]/=total;}return out;}
function exchangeWsHealthy(ex){const ob=orderbook&&orderbook[ex];if(!ob)return false;const now=Date.now();const last=Math.max(Number(ob.last_ws_event||0),Number(ob.last_snapshot||0),Number(ob.updated_ts||0));if(!(last>0))return false;return (now-last)<=20000;}
function renderWsStatus(){const el=document.getElementById('wsStatus');if(!el)return;const show=(name,ex)=>name+' '+(exchangeWsHealthy(ex)?'已连接':'未连接');el.textContent=[show('Binance','binance'),show('OKX','okx'),show('Bybit','bybit')].join('  ');}
function renderMeta(){if(!dashboard)return;const t=new Date(dashboard.generated_at).toLocaleString();document.getElementById('meta').textContent='覆盖 Binance / Bybit / OKX | 更新时间 '+t+' | 当前模式 '+currentMode;const s=buildShares(dashboard.states);document.getElementById('weights').textContent=currentMode==='weighted'?('OI 权重 Binance '+(s.binance*100).toFixed(1)+'% | Bybit '+(s.bybit*100).toFixed(1)+'% | OKX '+(s.okx*100).toFixed(1)+'%'):'合并模式下直接聚合多交易所盘口';}
async function load(){const [r1,r2,r3]=await Promise.all([fetch('/api/dashboard'),fetch('/api/orderbook?limit=60&mode='+encodeURIComponent(currentMode)),fetch('/api/price-events')]);dashboard=await r1.json();orderbook=await r2.json();const evts=await r3.json().catch(()=>[]);updateDepthMemory();setPersistedEvents(evts);renderActive();renderMeta();renderWsStatus();renderMergedPanel();drawDepth();renderEventTables();}
async function openUpgradeModal(){const m=document.getElementById('upgradeModal'),logEl=document.getElementById('upgradeLog'),foot=document.getElementById('upgradeFoot');if(!m||!logEl||!foot)return;m.classList.add('show');logEl.textContent='';foot.textContent='正在触发升级...';const r=await fetch('/api/upgrade/pull',{method:'POST'});const d=await r.json().catch(()=>({error:'response parse failed',output:''}));if(d.error){logEl.textContent=String(d.output||'');foot.textContent='触发失败: '+d.error;return;}foot.textContent='已触发，正在执行...';let stable=0;for(let i=0;i<180;i++){await new Promise(res=>setTimeout(res,1000));const pr=await fetch('/api/upgrade/progress').then(x=>x.json()).catch(()=>null);if(!pr)continue;logEl.textContent=String(pr.log||'');logEl.scrollTop=logEl.scrollHeight;if(pr.done){foot.textContent=(String(pr.exit_code||'')==='0')?'升级完成并已重启':'升级完成，退出码 '+String(pr.exit_code||'?');return;}if(!pr.running)stable++;else stable=0;if(stable>=3){foot.textContent='升级进程已结束（状态未知），请检查日志';return;}}foot.textContent='升级仍在进行，请稍后再看';}
function closeUpgradeModal(){const m=document.getElementById('upgradeModal');if(m)m.classList.remove('show');}
async function doUpgrade(event){if(event)event.preventDefault();openUpgradeModal();return false;}
document.querySelectorAll('button[data-mode]').forEach(b=>b.onclick=()=>setMode((b.dataset.mode||'weighted')));
loadModeFromStorage();
loadTuneFromStorage();
syncTuneUI();
const hs=document.getElementById('halfLifeSel'),rw=document.getElementById('rollWinSel');
if(hs)hs.onchange=applyTuneFromUI;
if(rw)rw.onchange=applyTuneFromUI;
bindDepthInteraction();
setInterval(load,5000);load();(async()=>{try{const r=await fetch('/api/version');const v=await r.json();const el=document.getElementById('globalFooter');if(el)el.textContent='Code by Yuhao@jiansutech.com - '+(v.commit_time||'-')+' - '+(v.commit_id||'-')+' - '+(v.branch||'-');}catch(_){const el=document.getElementById('globalFooter');if(el)el.textContent='Code by Yuhao@jiansutech.com - - - -';}})();
</script><div id="upgradeModal" class="upgrade-modal"><div class="upgrade-card"><div class="upgrade-head"><div class="upgrade-title">升级过程</div><button class="upgrade-close" onclick="closeUpgradeModal()">关闭</button></div><pre id="upgradeLog" class="upgrade-log"></pre><div id="upgradeFoot" class="upgrade-foot">等待开始...</div></div></div><div id="globalFooter" class="footer">Code by Yuhao@jiansutech.com - loading - loading - loading</div></body></html>`

const channelHTML = `<!doctype html>
<html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>&#28040;&#24687;&#36890;&#36947;</title>
<style>body{margin:0;background:#f5f7fb;color:#1f2937;font-family:Inter,system-ui,Segoe UI,Arial,sans-serif}.nav{height:56px;background:#0b1220;border-bottom:1px solid #243145;display:flex;align-items:center;justify-content:space-between;padding:0 20px;position:sticky;top:0;z-index:10}.nav-left,.nav-right{display:flex;align-items:center;gap:20px}.brand{font-size:18px;font-weight:700;color:#eef3f9}.menu a{color:#d6deea;text-decoration:none;font-size:16px;margin-right:18px}.menu a.active{color:#fff;font-weight:700}.upgrade{color:#fff;font-weight:700;text-decoration:none}.wrap{max-width:900px;margin:0 auto;padding:22px}.panel{border:1px solid #dce3ec;background:#fff;margin:14px 0;padding:16px;border-radius:10px;box-shadow:0 1px 2px rgba(15,23,42,.04)}.small{font-size:12px;color:#6b7280}button.primary{background:#22c55e;color:#fff;border:0;padding:10px 16px;border-radius:8px;cursor:pointer}button.secondary{background:#fff;color:#111827;border:1px solid #cbd5e1;padding:10px 16px;border-radius:8px;cursor:pointer}input{width:100%;box-sizing:border-box;padding:10px;border:1px solid #cbd5e1;border-radius:8px;background:#fff;color:#111827;margin-top:6px}.row{display:flex;gap:10px;flex-wrap:wrap;align-items:center}.upgrade-modal{position:fixed;inset:0;background:rgba(2,6,23,.55);display:none;align-items:center;justify-content:center;z-index:9999}.upgrade-modal.show{display:flex}.upgrade-card{width:min(880px,92vw);max-height:82vh;background:#0b1220;color:#e2e8f0;border:1px solid #334155;border-radius:10px;box-shadow:0 10px 30px rgba(2,6,23,.45);overflow:hidden}.upgrade-head{display:flex;align-items:center;justify-content:space-between;padding:10px 12px;border-bottom:1px solid #334155}.upgrade-title{font-size:14px;font-weight:700}.upgrade-close{background:transparent;border:1px solid #475569;color:#e2e8f0;border-radius:6px;padding:4px 8px;cursor:pointer}.upgrade-log{margin:0;padding:12px;white-space:pre-wrap;overflow:auto;max-height:62vh;font-family:ui-monospace,SFMono-Regular,Menlo,Consolas,monospace;font-size:12px;line-height:1.45}.upgrade-foot{padding:8px 12px;border-top:1px solid #334155;font-size:12px;color:#94a3b8}.footer{margin:18px auto 0 auto;max-width:1200px;padding:10px 12px;font-size:12px;color:#64748b;text-align:center}</style></head>
<body><div class="nav"><div class="nav-left"><div class="brand">ETH Liquidation Map</div><div class="menu"><a href="/">清算热区</a><a href="/config">模型配置</a><a href="/monitor">雷区监控</a><a href="/map">盘口汇总</a><a href="/liquidations">强平清算</a><a href="/bubbles">气泡图</a><a href="/channel" class="active">消息通道</a></div></div><div class="nav-right"><a href="#" class="upgrade" onclick="return doUpgrade(event)">升级</a></div></div>
<div class="wrap"><div class="panel"><h2 style="margin-top:0">Telegram 消息通道</h2><div class="small">配置 Telegram 机器人与频道，系统将按设定间隔发送通知。</div><div style="margin-top:14px"><label>Telegram Bot Token</label><input id="token" autocomplete="off" placeholder="123456:ABC..."></div><div style="margin-top:14px"><label>Telegram Channel / Chat ID</label><input id="channel" autocomplete="off" placeholder="@mychannel 或 -100123456789"></div><div style="margin-top:14px"><label>通知间隔（分钟）</label><input id="notify-interval" type="number" min="1" step="1" placeholder="15"></div><div style="margin-top:16px" class="row"><button class="primary" onclick="save()">保存</button><button class="secondary" onclick="testTelegram()">测试发送</button><span id="msg" class="small" style="margin-left:10px"></span></div></div></div>
<script>
let rawToken={{printf "%q" .TelegramBotToken}},rawChannel={{printf "%q" .TelegramChannel}},rawInterval={{.NotifyIntervalMin}},tokenDirty=false,channelDirty=false;
function maskSensitive(v){v=(v||'').trim();if(!v)return '';if(v.length<=8)return v;return v.slice(0,4)+'*'.repeat(v.length-8)+v.slice(-4);}function syncInputs(){const t=document.getElementById('token'),c=document.getElementById('channel'),n=document.getElementById('notify-interval');if(!tokenDirty)t.value=rawToken?maskSensitive(rawToken):'';if(!channelDirty)c.value=rawChannel?maskSensitive(rawChannel):'';n.value=rawInterval||15;}function currentValue(i,r,d){const v=(i.value||'').trim();if(!d&&r&&v===maskSensitive(r))return r;return v;}document.getElementById('token').addEventListener('input',()=>{tokenDirty=true});document.getElementById('channel').addEventListener('input',()=>{channelDirty=true});syncInputs();
async function loadFooter(){try{const r=await fetch('/api/version');const v=await r.json();const el=document.getElementById('globalFooter');if(el)el.textContent='Code by Yuhao@jiansutech.com - '+(v.commit_time||'-')+' - '+(v.commit_id||'-')+' - '+(v.branch||'-');}catch(_){const el=document.getElementById('globalFooter');if(el)el.textContent='Code by Yuhao@jiansutech.com - - - -';}}async function openUpgradeModal(){const m=document.getElementById('upgradeModal'),logEl=document.getElementById('upgradeLog'),foot=document.getElementById('upgradeFoot');if(!m||!logEl||!foot)return;m.classList.add('show');logEl.textContent='';foot.textContent='正在触发升级...';const r=await fetch('/api/upgrade/pull',{method:'POST'});const d=await r.json().catch(()=>({error:'response parse failed',output:''}));if(d.error){logEl.textContent=String(d.output||'');foot.textContent='触发失败: '+d.error;return;}foot.textContent='已触发，正在执行...';let stable=0;for(let i=0;i<180;i++){await new Promise(res=>setTimeout(res,1000));const pr=await fetch('/api/upgrade/progress').then(x=>x.json()).catch(()=>null);if(!pr)continue;logEl.textContent=String(pr.log||'');logEl.scrollTop=logEl.scrollHeight;if(pr.done){foot.textContent=(String(pr.exit_code||'')==='0')?'升级完成并已重启':'升级完成，退出码 '+String(pr.exit_code||'?');return;}if(!pr.running)stable++;else stable=0;if(stable>=3){foot.textContent='升级进程已结束（状态未知），请检查日志';return;}}foot.textContent='升级仍在进行，请稍后再看';}
function closeUpgradeModal(){const m=document.getElementById('upgradeModal');if(m)m.classList.remove('show');}
async function doUpgrade(event){if(event)event.preventDefault();openUpgradeModal();return false;}
async function save(){const n=document.getElementById('notify-interval');const iv=Math.max(1,Number((n&&n.value)||rawInterval||15)|0);const body={telegram_bot_token:currentValue(document.getElementById('token'),rawToken,tokenDirty),telegram_channel:currentValue(document.getElementById('channel'),rawChannel,channelDirty),notify_interval_min:iv};const r=await fetch('/api/settings',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify(body)});if(r.ok){rawToken=body.telegram_bot_token;rawChannel=body.telegram_channel;rawInterval=iv;tokenDirty=false;channelDirty=false;syncInputs();document.getElementById('msg').textContent='保存成功';}else{document.getElementById('msg').textContent='保存失败';}}
async function testTelegram(){const msg=document.getElementById('msg');msg.textContent='正在发送测试消息...';const r=await fetch('/api/channel/test',{method:'POST'});msg.textContent=r.ok?'测试发送成功':('测试发送失败: '+await r.text());}
</script><div id="upgradeModal" class="upgrade-modal"><div class="upgrade-card"><div class="upgrade-head"><div class="upgrade-title">升级过程</div><button class="upgrade-close" onclick="closeUpgradeModal()">关闭</button></div><pre id="upgradeLog" class="upgrade-log"></pre><div id="upgradeFoot" class="upgrade-foot">等待开始...</div></div></div><div id="globalFooter" class="footer">Code by Yuhao@jiansutech.com - loading - loading - loading</div></body></html>`

const configHTMLLegacy = `<!doctype html>
<html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>模型配置</title>
<style>body{margin:0;background:#f5f7fb;color:#1f2937;font-family:Inter,system-ui,Segoe UI,Arial,sans-serif}.nav{height:56px;background:#0b1220;border-bottom:1px solid #243145;display:flex;align-items:center;justify-content:space-between;padding:0 20px;position:sticky;top:0;z-index:10}.nav-left,.nav-right{display:flex;align-items:center;gap:20px}.brand{font-size:18px;font-weight:700;color:#eef3f9}.menu a{color:#d6deea;text-decoration:none;font-size:16px;margin-right:18px}.menu a.active{color:#fff;font-weight:700}.upgrade{color:#fff;font-weight:700;text-decoration:none}.wrap{max-width:900px;margin:0 auto;padding:22px}.panel{border:1px solid #dce3ec;background:#fff;margin:14px 0;padding:16px;border-radius:10px;box-shadow:0 1px 2px rgba(15,23,42,.04)}label{display:block;margin-top:12px;font-size:13px;color:#334155}input{width:100%;box-sizing:border-box;padding:10px;border:1px solid #cbd5e1;border-radius:8px;background:#fff;color:#111827;margin-top:6px}.row{display:flex;gap:10px;align-items:center;flex-wrap:wrap}.btn{background:#22c55e;color:#fff;border:0;padding:10px 16px;border-radius:8px;cursor:pointer}.small{font-size:12px;color:#64748b}.upgrade-modal{position:fixed;inset:0;background:rgba(2,6,23,.55);display:none;align-items:center;justify-content:center;z-index:9999}.upgrade-modal.show{display:flex}.upgrade-card{width:min(880px,92vw);max-height:82vh;background:#0b1220;color:#e2e8f0;border:1px solid #334155;border-radius:10px;box-shadow:0 10px 30px rgba(2,6,23,.45);overflow:hidden}.upgrade-head{display:flex;align-items:center;justify-content:space-between;padding:10px 12px;border-bottom:1px solid #334155}.upgrade-title{font-size:14px;font-weight:700}.upgrade-close{background:transparent;border:1px solid #475569;color:#e2e8f0;border-radius:6px;padding:4px 8px;cursor:pointer}.upgrade-log{margin:0;padding:12px;white-space:pre-wrap;overflow:auto;max-height:62vh;font-family:ui-monospace,SFMono-Regular,Menlo,Consolas,monospace;font-size:12px;line-height:1.45}.upgrade-foot{padding:8px 12px;border-top:1px solid #334155;font-size:12px;color:#94a3b8}.footer{margin:18px auto 0 auto;max-width:1200px;padding:10px 12px;font-size:12px;color:#64748b;text-align:center}</style></head>
<body><div class="nav"><div class="nav-left"><div class="brand">ETH Liquidation Map</div><div class="menu"><a href="/">清算热区</a><a href="/monitor">雷区监控</a><a href="/config" class="active">模型配置</a><a href="/map">盘口汇总</a><a href="/liquidations">强平清算</a><a href="/bubbles">气泡图</a><a href="/channel">消息通道</a></div></div><div class="nav-right"><a href="#" class="upgrade" onclick="return doUpgrade(event)">升级</a></div></div>
<div class="wrap"><div class="panel"><h2 style="margin-top:0">模型配置</h2><div class="small">修改模型参数以调整清算热区与强平清算计算。</div>
<label>lookback_min<input id="lookback_min" type="number" min="60" max="1440" step="1"></label>
<label>bucket_min<input id="bucket_min" type="number" min="1" max="30" step="1"></label>
<label>price_step<input id="price_step" type="number" min="1" max="50" step="0.1"></label>
<label>price_range<input id="price_range" type="number" min="100" max="1000" step="1"></label>
<div class="row" style="margin-top:16px"><button class="btn" onclick="save()">保存</button><span id="msg" class="small"></span></div>
</div></div>
<script>
async function openUpgradeModal(){const m=document.getElementById('upgradeModal'),logEl=document.getElementById('upgradeLog'),foot=document.getElementById('upgradeFoot');if(!m||!logEl||!foot)return;m.classList.add('show');logEl.textContent='';foot.textContent='正在触发升级...';const r=await fetch('/api/upgrade/pull',{method:'POST'});const d=await r.json().catch(()=>({error:'response parse failed',output:''}));if(d.error){logEl.textContent=String(d.output||'');foot.textContent='触发失败: '+d.error;return;}foot.textContent='已触发，正在执行...';let stable=0;for(let i=0;i<180;i++){await new Promise(res=>setTimeout(res,1000));const pr=await fetch('/api/upgrade/progress').then(x=>x.json()).catch(()=>null);if(!pr)continue;logEl.textContent=String(pr.log||'');logEl.scrollTop=logEl.scrollHeight;if(pr.done){foot.textContent=(String(pr.exit_code||'')==='0')?'升级完成并已重启':'升级完成，退出码 '+String(pr.exit_code||'?');return;}if(!pr.running)stable++;else stable=0;if(stable>=3){foot.textContent='升级进程已结束（状态未知），请检查日志';return;}}foot.textContent='升级仍在进行，请稍后再看';}
function closeUpgradeModal(){const m=document.getElementById('upgradeModal');if(m)m.classList.remove('show');}
async function doUpgrade(event){if(event)event.preventDefault();openUpgradeModal();return false;}
function val(id){return Number((document.getElementById(id).value||'').trim())}
async function load(){const r=await fetch('/api/model-config');const d=await r.json();document.getElementById('lookback_min').value=d.lookback_min;document.getElementById('bucket_min').value=d.bucket_min;document.getElementById('price_step').value=d.price_step;document.getElementById('price_range').value=d.price_range;}
async function save(){const body={lookback_min:val('lookback_min'),bucket_min:val('bucket_min'),price_step:val('price_step'),price_range:val('price_range')};const r=await fetch('/api/model-config',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify(body)});document.getElementById('msg').textContent=r.ok?'保存成功':'保存失败';if(r.ok)load();}
(async()=>{try{const r=await fetch('/api/version');const v=await r.json();const el=document.getElementById('globalFooter');if(el)el.textContent='Code by Yuhao@jiansutech.com - '+(v.commit_time||'-')+' - '+(v.commit_id||'-')+' - '+(v.branch||'-');}catch(_){const el=document.getElementById('globalFooter');if(el)el.textContent='Code by Yuhao@jiansutech.com - - - -';}})();load();
</script><div id="upgradeModal" class="upgrade-modal"><div class="upgrade-card"><div class="upgrade-head"><div class="upgrade-title">升级过程</div><button class="upgrade-close" onclick="closeUpgradeModal()">关闭</button></div><pre id="upgradeLog" class="upgrade-log"></pre><div id="upgradeFoot" class="upgrade-foot">等待开始...</div></div></div><div id="globalFooter" class="footer">Code by Yuhao@jiansutech.com - loading - loading - loading</div></body></html>`

const liquidationsHTML = `<!doctype html>
<html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>强平清算</title>
<style>body{margin:0;background:#f5f7fb;color:#1f2937;font-family:Inter,system-ui,Segoe UI,Arial,sans-serif}.nav{height:56px;background:#0b1220;border-bottom:1px solid #243145;display:flex;align-items:center;justify-content:space-between;padding:0 20px;position:sticky;top:0;z-index:10}.nav-left,.nav-right{display:flex;align-items:center;gap:20px}.brand{font-size:18px;font-weight:700;color:#eef3f9}.menu a{color:#d6deea;text-decoration:none;font-size:16px;margin-right:18px}.menu a.active{color:#fff;font-weight:700}.upgrade{color:#fff;font-weight:700;text-decoration:none}.wrap{max-width:1200px;margin:0 auto;padding:22px}.panel{border:1px solid #dce3ec;background:#fff;margin:14px 0;padding:16px;border-radius:10px;box-shadow:0 1px 2px rgba(15,23,42,.04)}table{width:100%;border-collapse:collapse}th,td{border-bottom:1px solid #e5e7eb;padding:8px 10px;text-align:center;font-size:13px}.row{display:flex;gap:8px;align-items:center}.btn{background:#fff;color:#111827;border:1px solid #cbd5e1;padding:8px 12px;border-radius:8px;cursor:pointer}.btn.primary{background:#22c55e;color:#fff;border-color:#22c55e}.small{font-size:12px;color:#64748b}.upgrade-modal{position:fixed;inset:0;background:rgba(2,6,23,.55);display:none;align-items:center;justify-content:center;z-index:9999}.upgrade-modal.show{display:flex}.upgrade-card{width:min(880px,92vw);max-height:82vh;background:#0b1220;color:#e2e8f0;border:1px solid #334155;border-radius:10px;box-shadow:0 10px 30px rgba(2,6,23,.45);overflow:hidden}.upgrade-head{display:flex;align-items:center;justify-content:space-between;padding:10px 12px;border-bottom:1px solid #334155}.upgrade-title{font-size:14px;font-weight:700}.upgrade-close{background:transparent;border:1px solid #475569;color:#e2e8f0;border-radius:6px;padding:4px 8px;cursor:pointer}.upgrade-log{margin:0;padding:12px;white-space:pre-wrap;overflow:auto;max-height:62vh;font-family:ui-monospace,SFMono-Regular,Menlo,Consolas,monospace;font-size:12px;line-height:1.45}.upgrade-foot{padding:8px 12px;border-top:1px solid #334155;font-size:12px;color:#94a3b8}.footer{margin:18px auto 0 auto;max-width:1200px;padding:10px 12px;font-size:12px;color:#64748b;text-align:center}</style></head>
<body><div class="nav"><div class="nav-left"><div class="brand">ETH Liquidation Map</div><div class="menu"><a href="/">清算热区</a><a href="/monitor">雷区监控</a><a href="/config">模型配置</a><a href="/map">盘口汇总</a><a href="/liquidations" class="active">强平清算</a><a href="/bubbles">气泡图</a><a href="/channel">消息通道</a></div></div><div class="nav-right"><a href="#" class="upgrade" onclick="return doUpgrade(event)">升级</a></div></div>
<div class="wrap"><div class="panel"><div class="row" style="justify-content:space-between"><h2 style="margin:0">ETH 强平清算（Binance / Bybit / OKX）</h2><div class="row"><button id="filterBtn" class="btn" onclick="toggleFilter()">过滤小单</button><button class="btn" onclick="prev()">上一页</button><button class="btn" onclick="next()">下一页</button><button class="btn primary" onclick="load()">刷新</button></div></div><div id="meta" class="small" style="margin:8px 0 10px 0"></div><div id="table"></div></div></div>
<script>
let page=1,pageSize=25,filterSmall=false;
function fmtPrice(n){return Number(n).toLocaleString('zh-CN',{minimumFractionDigits:1,maximumFractionDigits:1})}
function fmtQty(n){return Number(n).toLocaleString('zh-CN',{maximumFractionDigits:4})}
function fmtAmt(n){n=Number(n);if(!isFinite(n))return '-';return n.toLocaleString('zh-CN',{minimumFractionDigits:2,maximumFractionDigits:2});}
function fmtTime(ts){try{return new Date(Number(ts||0)).toLocaleString('zh-CN',{hour12:false});}catch(_){return '-';}}
function render(rows){if(!rows||!rows.length){document.getElementById('table').innerHTML='<div class="small">暂无数据</div>';return;}let h='<table><thead><tr><th>时间</th><th>交易所</th><th>方向</th><th>价格</th><th>数量</th><th>金额 (USD)</th></tr></thead><tbody>';for(const r of rows){h+='<tr><td>'+fmtTime(r.event_ts)+'</td><td>'+String(r.exchange||'').toUpperCase()+'</td><td>'+r.side+'</td><td>'+fmtPrice(r.price)+'</td><td>'+fmtQty(r.qty)+'</td><td>'+fmtAmt(r.notional_usd)+'</td></tr>';}h+='</tbody></table>';document.getElementById('table').innerHTML=h;}
function toggleFilter(){filterSmall=!filterSmall;const b=document.getElementById('filterBtn');if(b)b.textContent=filterSmall?'显示全部':'过滤小单';load();}
async function load(){const r=await fetch('/api/liquidations?page='+page+'&limit='+pageSize);const d=await r.json();let rows=(d.rows||[]);if(filterSmall)rows=rows.filter(x=>Number(x.qty||0)>=1);document.getElementById('meta').textContent='第 '+d.page+' 页 | 每页 '+d.page_size+' 条'+(filterSmall?' | 已过滤小于 1 ETH 的记录':'');render(rows);}
function prev(){if(page>1){page--;load();}}
function next(){page++;load();}
async function openUpgradeModal(){const m=document.getElementById('upgradeModal'),logEl=document.getElementById('upgradeLog'),foot=document.getElementById('upgradeFoot');if(!m||!logEl||!foot)return;m.classList.add('show');logEl.textContent='';foot.textContent='正在触发升级...';const r=await fetch('/api/upgrade/pull',{method:'POST'});const d=await r.json().catch(()=>({error:'response parse failed',output:''}));if(d.error){logEl.textContent=String(d.output||'');foot.textContent='触发失败: '+d.error;return;}foot.textContent='已触发，正在执行...';let stable=0;for(let i=0;i<180;i++){await new Promise(res=>setTimeout(res,1000));const pr=await fetch('/api/upgrade/progress').then(x=>x.json()).catch(()=>null);if(!pr)continue;logEl.textContent=String(pr.log||'');logEl.scrollTop=logEl.scrollHeight;if(pr.done){foot.textContent=(String(pr.exit_code||'')==='0')?'升级完成并已重启':'升级完成，退出码 '+String(pr.exit_code||'?');return;}if(!pr.running)stable++;else stable=0;if(stable>=3){foot.textContent='升级进程已结束（状态未知），请检查日志';return;}}foot.textContent='升级仍在进行，请稍后再看';}
function closeUpgradeModal(){const m=document.getElementById('upgradeModal');if(m)m.classList.remove('show');}
async function doUpgrade(event){if(event)event.preventDefault();openUpgradeModal();return false;}
setInterval(load,5000);load();(async()=>{try{const r=await fetch('/api/version');const v=await r.json();const el=document.getElementById('globalFooter');if(el)el.textContent='Code by Yuhao@jiansutech.com - '+(v.commit_time||'-')+' - '+(v.commit_id||'-')+' - '+(v.branch||'-');}catch(_){const el=document.getElementById('globalFooter');if(el)el.textContent='Code by Yuhao@jiansutech.com - - - -';}})();
</script><div id="upgradeModal" class="upgrade-modal"><div class="upgrade-card"><div class="upgrade-head"><div class="upgrade-title">升级过程</div><button class="upgrade-close" onclick="closeUpgradeModal()">关闭</button></div><pre id="upgradeLog" class="upgrade-log"></pre><div id="upgradeFoot" class="upgrade-foot">等待开始...</div></div></div><div id="globalFooter" class="footer">Code by Yuhao@jiansutech.com - loading - loading - loading</div></body></html>`

const bubblesHTML = `<!doctype html>
<html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>气泡图</title>
<style>body{margin:0;background:#f5f7fb;color:#1f2937;font-family:Inter,system-ui,Segoe UI,Arial,sans-serif}.nav{height:56px;background:#0b1220;border-bottom:1px solid #243145;display:flex;align-items:center;justify-content:space-between;padding:0 20px;position:sticky;top:0;z-index:10}.nav-left,.nav-right{display:flex;align-items:center;gap:20px}.brand{font-size:18px;font-weight:700;color:#eef3f9}.menu a{color:#d6deea;text-decoration:none;font-size:16px;margin-right:18px}.menu a.active{color:#fff;font-weight:700}.upgrade{color:#fff;font-weight:700;text-decoration:none}.wrap{max-width:1280px;margin:0 auto;padding:14px}.panel{border:1px solid #dce3ec;background:#fff;margin:10px 0;padding:12px;border-radius:10px}.row{display:flex;gap:8px;align-items:center;flex-wrap:wrap}.small{font-size:12px;color:#64748b}select,button{height:34px;border:1px solid #cbd5e1;border-radius:8px;background:#fff;padding:0 10px}button{cursor:pointer}.chart{width:100%;height:620px;border:1px solid #e5e7eb;border-radius:8px;background:#fff;display:block}.upgrade-modal{position:fixed;inset:0;background:rgba(2,6,23,.55);display:none;align-items:center;justify-content:center;z-index:9999}.upgrade-modal.show{display:flex}.upgrade-card{width:min(880px,92vw);max-height:82vh;background:#0b1220;color:#e2e8f0;border:1px solid #334155;border-radius:10px;box-shadow:0 10px 30px rgba(2,6,23,.45);overflow:hidden}.upgrade-head{display:flex;align-items:center;justify-content:space-between;padding:10px 12px;border-bottom:1px solid #334155}.upgrade-title{font-size:14px;font-weight:700}.upgrade-close{background:transparent;border:1px solid #475569;color:#e2e8f0;border-radius:6px;padding:4px 8px;cursor:pointer}.upgrade-log{margin:0;padding:12px;white-space:pre-wrap;overflow:auto;max-height:62vh;font-family:ui-monospace,SFMono-Regular,Menlo,Consolas,monospace;font-size:12px;line-height:1.45}.upgrade-foot{padding:8px 12px;border-top:1px solid #334155;font-size:12px;color:#94a3b8}.footer{margin:18px auto 0 auto;max-width:1200px;padding:10px 12px;font-size:12px;color:#64748b;text-align:center}</style></head>
<body><div class="nav"><div class="nav-left"><div class="brand">ETH Liquidation Map</div><div class="menu"><a href="/">清算热区</a><a href="/monitor">雷区监控</a><a href="/config">模型配置</a><a href="/map">盘口汇总</a><a href="/liquidations">强平清算</a><a href="/bubbles" class="active">气泡图</a><a href="/channel">消息通道</a></div></div><div class="nav-right"><a href="#" class="upgrade" onclick="return doUpgrade(event)">升级</a></div></div>
<div class="wrap"><div class="panel"><div class="row"><span>周期</span><select id="iv"><option value="1m">1M</option><option value="2m">2M</option><option value="5m">5M</option><option value="10m">10M</option><option value="15m" selected>15M</option><option value="30m">30M</option><option value="1h">1H</option><option value="4h">4H</option><option value="8h">8H</option><option value="12h">12H</option><option value="1d">1D</option><option value="3d">3D</option><option value="7d">7D</option></select><button onclick="load()">刷新</button><label class="small" style="display:inline-flex;align-items:center;gap:6px;margin-left:6px"><input id="hist" type="checkbox">历史</label><button id="moreBtn" style="display:none" onclick="loadMoreHistory()">向左加载</button><span id="meta" class="small"></span></div><canvas id="cv" class="chart" width="1600" height="620"></canvas><div class="small">气泡大小代表清算金额，颜色代表多空方向。滚轮缩放（默认以最右侧K线为锚点），按住鼠标左键左右拖动。</div></div></div>
<script>
let candles=[],events=[],viewStart=0,viewCount=120,drag=false,lastX=0;
function fit(){const c=document.getElementById('cv');const r=c.getBoundingClientRect(),dpr=window.devicePixelRatio||1;const w=Math.max(700,Math.floor(r.width)),h=Math.max(420,Math.floor(r.height));c.width=Math.floor(w*dpr);c.height=Math.floor(h*dpr);const x=c.getContext('2d');x.setTransform(dpr,0,0,dpr,0,0);return {c,x,w,h};}
function toNum(v){const n=Number(v);return isFinite(n)?n:0}
function mapInterval(v){if(v==='10m')return '5m';if(v==='7d')return '1w';return v;}
function parseRows(rows,interval){let cs=(rows||[]).map(r=>({t:toNum(r[0]),o:toNum(r[1]),h:toNum(r[2]),l:toNum(r[3]),c:toNum(r[4])}));if(interval==='2m'){const out=[];for(let i=0;i+1<cs.length;i+=2){const a=cs[i],b=cs[i+1];out.push({t:a.t,o:a.o,h:Math.max(a.h,b.h),l:Math.min(a.l,b.l),c:b.c});}cs=out;}if(interval==='10m'){const out=[];for(let i=0;i+1<cs.length;i+=2){const a=cs[i],b=cs[i+1];out.push({t:a.t,o:a.o,h:Math.max(a.h,b.h),l:Math.min(a.l,b.l),c:b.c});}cs=out;}if(interval==='7d'){const out=[];for(let i=0;i+6<cs.length;i+=7){const a=cs[i],g=cs.slice(i,i+7),z=g[g.length-1];out.push({t:a.t,o:a.o,h:Math.max(...g.map(v=>v.h)),l:Math.min(...g.map(v=>v.l)),c:z.c});}cs=out;}return cs;}
function draw(){const v=fit(),x=v.x,W=v.w,H=v.h,padL=70,padR=20,padT=18,padB=42,pw=W-padL-padR,ph=H-padT-padB,by=padT+ph;x.clearRect(0,0,W,H);x.fillStyle='#fff';x.fillRect(0,0,W,H);if(!candles.length){x.fillStyle='#64748b';x.fillText('暂无数据',16,24);return;}const s=Math.max(0,Math.min(candles.length-1,viewStart)),e=Math.max(s+10,Math.min(candles.length,s+viewCount));const cs=candles.slice(s,e);const minP=Math.min(...cs.map(v=>v.l)),maxP=Math.max(...cs.map(v=>v.h));const span=Math.max(1e-6,maxP-minP);const sx=i=>padL+(i/(cs.length-1))*pw,sy=p=>padT+((maxP-p)/span)*ph;x.strokeStyle='#e5e7eb';x.font='12px sans-serif';for(let i=0;i<=4;i++){const y=padT+ph*i/4,val=maxP-(span*i/4);x.beginPath();x.moveTo(padL,y);x.lineTo(W-padR,y);x.stroke();x.fillStyle='#64748b';x.fillText(val.toFixed(1),6,y+4);}const bodyW=Math.max(3,Math.min(12,pw/Math.max(20,cs.length)));for(let i=0;i<cs.length;i++){const c=cs[i],px=sx(i),yo=sy(c.o),yc=sy(c.c),yh=sy(c.h),yl=sy(c.l),up=c.c>=c.o;x.strokeStyle=up?'#16a34a':'#dc2626';x.beginPath();x.moveTo(px,yh);x.lineTo(px,yl);x.stroke();x.fillStyle=up?'rgba(22,163,74,0.75)':'rgba(220,38,38,0.75)';x.fillRect(px-bodyW/2,Math.min(yo,yc),bodyW,Math.max(1,Math.abs(yc-yo)));}
const t0=cs[0].t,t1=cs[cs.length-1].t;const vis=events.filter(e=>e.event_ts>=t0&&e.event_ts<=t1);const maxN=Math.max(1,...vis.map(e=>toNum(e.notional_usd)));for(const ev of vis){const tt=toNum(ev.event_ts);const ratio=(tt-t0)/Math.max(1,t1-t0);const px=padL+ratio*pw;const py=sy(toNum(ev.price));const r=3+18*Math.sqrt(Math.max(0,toNum(ev.notional_usd))/maxN);const side=String(ev.side||'').toLowerCase();const color=(side==='long')?'rgba(22,163,74,0.45)':'rgba(220,38,38,0.45)';const stroke=(side==='long')?'#16a34a':'#dc2626';x.beginPath();x.fillStyle=color;x.strokeStyle=stroke;x.arc(px,py,r,0,Math.PI*2);x.fill();x.stroke();}
x.fillStyle='#64748b';for(let i=0;i<=6;i++){const k=Math.floor((cs.length-1)*i/6),px=sx(k),t=new Date(cs[k].t).toLocaleString('zh-CN',{month:'2-digit',day:'2-digit',hour:'2-digit',minute:'2-digit',hour12:false});x.fillText(t,Math.max(padL,px-30),H-10);} }
function uniqByT(list){const out=[];const seen=new Set();for(const it of (list||[])){const t=toNum(it.t);if(!t||seen.has(t))continue;seen.add(t);out.push(it);}out.sort((a,b)=>a.t-b.t);return out;}
function mergeEvents(a,b){const out=[];const key=(e)=>String(toNum(e.event_ts))+'|'+String(e.exchange||'')+'|'+String(e.side||'')+'|'+String(toNum(e.price))+'|'+String(toNum(e.qty));const seen=new Set();for(const it of (a||[])){const k=key(it);if(seen.has(k))continue;seen.add(k);out.push(it);}for(const it of (b||[])){const k=key(it);if(seen.has(k))continue;seen.add(k);out.push(it);}out.sort((x,y)=>toNum(x.event_ts)-toNum(y.event_ts));return out;}
function setMoreBtnVisible(){const cb=document.getElementById('hist');const b=document.getElementById('moreBtn');if(!cb||!b)return;b.style.display=cb.checked?'inline-block':'none';}
async function load(){const iv=document.getElementById('iv').value;const kr=await fetch('/api/klines?interval='+encodeURIComponent(iv)+'&limit=500');const kd=await kr.json();candles=uniqByT(parseRows(kd.rows||[],iv));events=[];if(candles.length){const startTS=toNum(candles[0].t),endTS=toNum(candles[candles.length-1].t);const er=await fetch('/api/liquidations?limit=5000&page=1&start_ts='+encodeURIComponent(startTS)+'&end_ts='+encodeURIComponent(endTS));const ed=await er.json();events=ed.rows||[];}viewCount=Math.min(160,Math.max(50,Math.floor(candles.length*0.45)));viewStart=Math.max(0,candles.length-viewCount);setMoreBtnVisible();document.getElementById('meta').textContent='K线来源: '+(kd.source||iv)+' | 区间清算事件 '+events.length+' 条';draw();}
async function loadMoreHistory(){const cb=document.getElementById('hist');if(!cb||!cb.checked)return;if(!candles.length)return;const iv=document.getElementById('iv').value;const endTS=Math.max(0,toNum(candles[0].t)-1);const kr=await fetch('/api/klines?interval='+encodeURIComponent(iv)+'&limit=500&end_ts='+encodeURIComponent(endTS));const kd=await kr.json();const more=uniqByT(parseRows(kd.rows||[],iv));if(!more.length){document.getElementById('meta').textContent='没有更多历史K线';return;}const added=more.filter(x=>toNum(x.t)<toNum(candles[0].t));if(!added.length){document.getElementById('meta').textContent='没有更多历史K线';return;}candles=uniqByT(added.concat(candles));const startTS=toNum(added[0].t),endTS2=toNum(added[added.length-1].t);const er=await fetch('/api/liquidations?limit=5000&page=1&start_ts='+encodeURIComponent(startTS)+'&end_ts='+encodeURIComponent(endTS2));const ed=await er.json();events=mergeEvents(ed.rows||[],events);viewStart=Math.max(0,viewStart+added.length);document.getElementById('meta').textContent='K线来源: '+(kd.source||iv)+' | 区间清算事件 '+events.length+' 条';draw();}
const c=document.getElementById('cv');c.addEventListener('wheel',e=>{if(!candles.length)return;e.preventDefault();const right=Math.min(candles.length,viewStart+viewCount);const factor=e.deltaY<0?0.88:1.12;const nextCount=Math.max(30,Math.min(candles.length,Math.round(viewCount*factor)));viewCount=nextCount;viewStart=Math.max(0,Math.min(candles.length-viewCount,right-viewCount));draw();},{passive:false});c.addEventListener('mousedown',e=>{drag=true;lastX=e.clientX});window.addEventListener('mouseup',()=>drag=false);window.addEventListener('mousemove',e=>{if(!drag||!candles.length)return;const dx=e.clientX-lastX;lastX=e.clientX;const shift=Math.round(-dx/8);if(shift!==0){viewStart=Math.max(0,Math.min(candles.length-viewCount,viewStart+shift));draw();}});window.addEventListener('resize',()=>draw());document.getElementById('iv').addEventListener('change',load);document.getElementById('hist').addEventListener('change',setMoreBtnVisible);
async function openUpgradeModal(){const m=document.getElementById('upgradeModal'),logEl=document.getElementById('upgradeLog'),foot=document.getElementById('upgradeFoot');if(!m||!logEl||!foot)return;m.classList.add('show');logEl.textContent='';foot.textContent='正在触发升级...';const r=await fetch('/api/upgrade/pull',{method:'POST'});const d=await r.json().catch(()=>({error:'response parse failed',output:''}));if(d.error){logEl.textContent=String(d.output||'');foot.textContent='触发失败: '+d.error;return;}foot.textContent='已触发，正在执行...';let stable=0;for(let i=0;i<180;i++){await new Promise(res=>setTimeout(res,1000));const pr=await fetch('/api/upgrade/progress').then(x=>x.json()).catch(()=>null);if(!pr)continue;logEl.textContent=String(pr.log||'');logEl.scrollTop=logEl.scrollHeight;if(pr.done){foot.textContent=(String(pr.exit_code||'')==='0')?'升级完成并已重启':'升级完成，退出码 '+String(pr.exit_code||'?');return;}if(!pr.running)stable++;else stable=0;if(stable>=3){foot.textContent='升级进程已结束（状态未知），请检查日志';return;}}foot.textContent='升级仍在进行，请稍后再看';}
function closeUpgradeModal(){const m=document.getElementById('upgradeModal');if(m)m.classList.remove('show');}
async function doUpgrade(event){if(event)event.preventDefault();openUpgradeModal();return false;}
load();
</script><div id="upgradeModal" class="upgrade-modal"><div class="upgrade-card"><div class="upgrade-head"><div class="upgrade-title">升级过程</div><button class="upgrade-close" onclick="closeUpgradeModal()">关闭</button></div><pre id="upgradeLog" class="upgrade-log"></pre><div id="upgradeFoot" class="upgrade-foot">等待开始...</div></div></div><div id="globalFooter" class="footer">Code by Yuhao@jiansutech.com - loading - loading - loading</div><script>(async()=>{try{const r=await fetch('/api/version');const v=await r.json();const el=document.getElementById('globalFooter');if(el)el.textContent='Code by Yuhao@jiansutech.com - '+(v.commit_time||'-')+' - '+(v.commit_id||'-')+' - '+(v.branch||'-');}catch(_){const el=document.getElementById('globalFooter');if(el)el.textContent='Code by Yuhao@jiansutech.com - - - -';}})();</script></body></html>`
const configHTML = `<!doctype html>
<html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>模型配置</title>
<style>body{margin:0;background:#f5f7fb;color:#1f2937;font-family:Inter,system-ui,Segoe UI,Arial,sans-serif}.nav{height:56px;background:#0b1220;border-bottom:1px solid #243145;display:flex;align-items:center;justify-content:space-between;padding:0 20px;position:sticky;top:0;z-index:10}.nav-left,.nav-right{display:flex;align-items:center;gap:20px}.brand{font-size:18px;font-weight:700;color:#eef3f9}.menu a{color:#d6deea;text-decoration:none;font-size:16px;margin-right:18px}.menu a.active{color:#fff;font-weight:700}.upgrade{color:#fff;font-weight:700;text-decoration:none}.wrap{max-width:980px;margin:0 auto;padding:22px}.panel{border:1px solid #dce3ec;background:#fff;margin:14px 0;padding:16px;border-radius:10px;box-shadow:0 1px 2px rgba(15,23,42,.04)}.small{font-size:12px;color:#6b7280}.row{display:flex;gap:10px;flex-wrap:wrap;align-items:center}.grid{display:grid;grid-template-columns:1fr 1fr;gap:12px}.field label{display:block;font-size:12px;color:#6b7280}.field input{width:100%;box-sizing:border-box;padding:10px;border:1px solid #cbd5e1;border-radius:8px;background:#fff;color:#111827;margin-top:6px}button.primary{background:#22c55e;color:#fff;border:0;padding:10px 16px;border-radius:8px;cursor:pointer}button.secondary{background:#fff;color:#111827;border:1px solid #cbd5e1;padding:10px 16px;border-radius:8px;cursor:pointer}.upgrade-modal{position:fixed;inset:0;background:rgba(2,6,23,.55);display:none;align-items:center;justify-content:center;z-index:9999}.upgrade-modal.show{display:flex}.upgrade-card{width:min(880px,92vw);max-height:82vh;background:#0b1220;color:#e2e8f0;border:1px solid #334155;border-radius:10px;box-shadow:0 10px 30px rgba(2,6,23,.45);overflow:hidden}.upgrade-head{display:flex;align-items:center;justify-content:space-between;padding:10px 12px;border-bottom:1px solid #334155}.upgrade-title{font-size:14px;font-weight:700}.upgrade-close{background:transparent;border:1px solid #475569;color:#e2e8f0;border-radius:6px;padding:4px 8px;cursor:pointer}.upgrade-log{margin:0;padding:12px;white-space:pre-wrap;overflow:auto;max-height:62vh;font-family:ui-monospace,SFMono-Regular,Menlo,Consolas,monospace;font-size:12px;line-height:1.45}.upgrade-foot{padding:8px 12px;border-top:1px solid #334155;font-size:12px;color:#94a3b8}.footer{margin:18px auto 0 auto;max-width:1200px;padding:10px 12px;font-size:12px;color:#64748b;text-align:center}</style></head>
<body><div class="nav"><div class="nav-left"><div class="brand">ETH Liquidation Map</div><div class="menu"><a href="/">清算热区</a><a href="/config" class="active">模型配置</a><a href="/monitor">雷区监控</a><a href="/map">盘口汇总</a><a href="/liquidations">强平清算</a><a href="/bubbles">气泡图</a><a href="/channel">消息通道</a></div></div><div class="nav-right"><a href="#" class="upgrade" onclick="return doUpgrade(event)">升级</a></div></div>
<div class="wrap"><div class="panel"><h2 style="margin-top:0">清算地图模型参数</h2><div class="small">修改后立即影响 OI 增量模型的计算与展示。</div>
<div class="grid" style="margin-top:14px">
  <div class="field"><label>回看窗口（分钟）</label><input id="lookback" type="number" min="60" max="1440" step="1"></div>
  <div class="field"><label>时间桶（分钟）</label><input id="bucket" type="number" min="1" max="30" step="1"></div>
  <div class="field"><label>价格步长</label><input id="step" type="number" min="1" max="50" step="0.5"></div>
  <div class="field"><label>价格范围（±）</label><input id="range" type="number" min="100" max="1000" step="10"></div>
  <div class="field"><label>杠杆档位（逗号分隔）</label><input id="levs" type="text" placeholder="20,50,100"></div>
  <div class="field"><label>杠杆权重（逗号分隔）</label><input id="weights" type="text" placeholder="0.30,0.40,0.30"></div>
  <div class="field"><label>维护保证金率</label><input id="mm" type="number" step="0.0001"></div>
  <div class="field"><label>资金费率缩放系数</label><input id="funding" type="number" step="100"></div>
  <div class="field"><label>时间衰减系数 k</label><input id="decay" type="number" step="0.1"></div>
  <div class="field"><label>邻近价扩散比例</label><input id="neighbor" type="number" step="0.01"></div>
</div>
<div class="row" style="margin-top:14px"><button class="primary" onclick="save()">保存</button><button class="secondary" onclick="reloadCfg()">重载</button><span id="msg" class="small" style="margin-left:10px"></span></div></div></div>
<script>
function bind(cfg){
  document.getElementById('lookback').value=cfg.LookbackMin||360;
  document.getElementById('bucket').value=cfg.BucketMin||5;
  document.getElementById('step').value=cfg.PriceStep||5;
  document.getElementById('range').value=cfg.PriceRange||400;
  document.getElementById('levs').value=cfg.LeverageCSV||'20,50,100';
  document.getElementById('weights').value=cfg.WeightCSV||'0.30,0.40,0.30';
  document.getElementById('mm').value=cfg.MaintMargin||0.005;
  document.getElementById('funding').value=cfg.FundingScale||7000;
  document.getElementById('decay').value=cfg.DecayK||2.2;
  document.getElementById('neighbor').value=cfg.NeighborShare||0.28;
}
async function reloadCfg(){
  const cfg=await fetch('/api/model-config').then(r=>r.json()).catch(()=>null);
  if(cfg) bind(cfg);
}
async function save(){
  const body={
    LookbackMin:Number(document.getElementById('lookback').value||360),
    BucketMin:Number(document.getElementById('bucket').value||5),
    PriceStep:Number(document.getElementById('step').value||5),
    PriceRange:Number(document.getElementById('range').value||400),
    LeverageCSV:String(document.getElementById('levs').value||'20,50,100'),
    WeightCSV:String(document.getElementById('weights').value||'0.30,0.40,0.30'),
    MaintMargin:Number(document.getElementById('mm').value||0.005),
    FundingScale:Number(document.getElementById('funding').value||7000),
    DecayK:Number(document.getElementById('decay').value||2.2),
    NeighborShare:Number(document.getElementById('neighbor').value||0.28)
  };
  const r=await fetch('/api/model-config',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify(body)});
  document.getElementById('msg').textContent=r.ok?'已保存':'保存失败';
}
async function loadFooter(){try{const r=await fetch('/api/version');const v=await r.json();const el=document.getElementById('globalFooter');if(el)el.textContent='Code by Yuhao@jiansutech.com - '+(v.commit_time||'-')+' - '+(v.commit_id||'-')+' - '+(v.branch||'-');}catch(_){const el=document.getElementById('globalFooter');if(el)el.textContent='Code by Yuhao@jiansutech.com - - - -';}}
async function openUpgradeModal(){const m=document.getElementById('upgradeModal'),logEl=document.getElementById('upgradeLog'),foot=document.getElementById('upgradeFoot');if(!m||!logEl||!foot)return;m.classList.add('show');logEl.textContent='';foot.textContent='正在触发升级...';const r=await fetch('/api/upgrade/pull',{method:'POST'});const d=await r.json().catch(()=>({error:'response parse failed',output:''}));if(d.error){logEl.textContent=String(d.output||'');foot.textContent='触发失败: '+d.error;return;}foot.textContent='已触发，正在执行...';let stable=0;for(let i=0;i<180;i++){await new Promise(res=>setTimeout(res,1000));const pr=await fetch('/api/upgrade/progress').then(x=>x.json()).catch(()=>null);if(!pr)continue;logEl.textContent=String(pr.log||'');logEl.scrollTop=logEl.scrollHeight;if(pr.done){foot.textContent=(String(pr.exit_code||'')==='0')?'升级完成并已重启':'升级完成，退出码 '+String(pr.exit_code||'?');return;}if(!pr.running)stable++;else stable=0;if(stable>=3){foot.textContent='升级进程已结束（状态未知），请检查日志';return;}}foot.textContent='升级仍在进行，请稍后再看';}
function closeUpgradeModal(){const m=document.getElementById('upgradeModal');if(m)m.classList.remove('show');}
async function doUpgrade(event){if(event)event.preventDefault();openUpgradeModal();return false;}
bind({LookbackMin:{{.LookbackMin}},BucketMin:{{.BucketMin}},PriceStep:{{.PriceStep}},PriceRange:{{.PriceRange}},LeverageCSV:{{printf "%q" .LeverageCSV}},WeightCSV:{{printf "%q" .WeightCSV}},MaintMargin:{{.MaintMargin}},FundingScale:{{.FundingScale}},DecayK:{{.DecayK}},NeighborShare:{{.NeighborShare}}});
loadFooter();
</script><div id="upgradeModal" class="upgrade-modal"><div class="upgrade-card"><div class="upgrade-head"><div class="upgrade-title">升级过程</div><button class="upgrade-close" onclick="closeUpgradeModal()">关闭</button></div><pre id="upgradeLog" class="upgrade-log"></pre><div id="upgradeFoot" class="upgrade-foot">等待开始...</div></div></div><div id="globalFooter" class="footer">Code by Yuhao@jiansutech.com - loading - loading - loading</div></body></html>`
