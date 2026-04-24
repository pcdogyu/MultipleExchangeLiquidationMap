package liqmap

import (
	"context"
	"database/sql"
	"net/http"
	"time"
)

type ModelConfigPageData struct {
	ModelConfig
	PageTitle        string
	ActiveMenu       string
	ShowAnalysisInfo bool
}

type BadRequestError struct {
	Message string
}

func (e BadRequestError) Error() string {
	return e.Message
}

const (
	DefaultDBPath     = defaultDBPath
	DefaultServerAddr = defaultServerAddr
	DefaultSymbol     = defaultSymbol
)

func Getenv(key, fallback string) string {
	return getenv(key, fallback)
}

func SetupLogging(debug bool) (func(), error) {
	return setupLogging(debug)
}

func NewApp(db *sql.DB, debug bool) *App {
	app := &App{
		db: db,
		httpClient: &http.Client{
			Timeout: 12 * time.Second,
		},
		ob:         newOrderBookHub(),
		apiGuards:  map[string]*ExchangeAPIGuard{},
		windowDays: defaultWindowDays,
		debug:      debug,
	}
	app.webds = newWebDataSourceManager(app)
	return app
}

func (a *App) StartBackgroundJobs(ctx context.Context) {
	a.startCollector(ctx)
	a.startOrderBookSync(ctx)
	a.startLiquidationSync(ctx)
	a.startTelegramNotifier(ctx)
	a.startModelMapSnapshotter(ctx)
	if a.webds != nil {
		a.webds.start(ctx)
	}
}

func (a *App) HandleDashboard(w http.ResponseWriter, r *http.Request) { a.handleDashboard(w, r) }

func (a *App) HandleAnalysisAPI(w http.ResponseWriter, r *http.Request) { a.handleAnalysisAPI(w, r) }

func (a *App) HandleModelLiquidationMap(w http.ResponseWriter, r *http.Request) {
	a.handleModelLiquidationMap(w, r)
}

func (a *App) HandleLiquidationsAPI(w http.ResponseWriter, r *http.Request) {
	a.handleLiquidationsAPI(w, r)
}

func (a *App) HandleCoinGlassMap(w http.ResponseWriter, r *http.Request) { a.handleCoinGlassMap(w, r) }
func (a *App) HandleWindow(w http.ResponseWriter, r *http.Request)       { a.handleWindow(w, r) }

func (a *App) HandleWebDataSourceStatus(w http.ResponseWriter, r *http.Request) {
	a.handleWebDataSourceStatus(w, r)
}

func (a *App) HandleWebDataSourceInit(w http.ResponseWriter, r *http.Request) {
	a.handleWebDataSourceInit(w, r)
}

func (a *App) HandleWebDataSourceRun(w http.ResponseWriter, r *http.Request) {
	a.handleWebDataSourceRun(w, r)
}

func (a *App) HandleWebDataSourceMap(w http.ResponseWriter, r *http.Request) {
	a.handleWebDataSourceMap(w, r)
}

func (a *App) HandleWebDataSourceRuns(w http.ResponseWriter, r *http.Request) {
	a.handleWebDataSourceRuns(w, r)
}

func (a *App) HandleWebDataSourceSettings(w http.ResponseWriter, r *http.Request) {
	a.handleWebDataSourceSettings(w, r)
}

func (a *App) LoadSettings() ChannelSettings {
	return a.loadSettings()
}

func (a *App) SaveSettings(req ChannelSettings) error {
	return a.saveSettings(req)
}

func (a *App) TriggerChannelTestSend() (string, bool) {
	return a.triggerTelegramTestSend()
}

func (a *App) ListTelegramSendHistory(limit int) ([]TelegramSendHistoryRow, error) {
	return a.listTelegramSendHistory(limit)
}

func (a *App) ListChannelTimeline(hours int) ([]ChannelTimelineRow, error) {
	return a.listChannelTimeline(hours)
}

func (a *App) ListChannelPlannedPushes(hours int) []ChannelPlannedPushRow {
	return a.listChannelPlannedPushes(hours)
}

func (a *App) LoadModelConfig() ModelConfig {
	return a.loadModelConfig()
}

func (a *App) SaveModelConfig(req ModelConfig) error {
	return a.saveModelConfig(req)
}

func (a *App) RunModelFit(hours, minEvents int, exchange, mode string) (map[string]any, error) {
	return a.runModelFit(hours, minEvents, exchange, mode)
}

func (a *App) OrderBookView(exchange, mode string, limit int) (any, error) {
	return a.orderBookView(exchange, mode, limit)
}

func (a *App) ListPriceWallEvents(page, limit, minutes int, side, mode string) (any, error) {
	return a.listPriceWallEvents(page, limit, minutes, side, mode)
}

func (a *App) RecordPriceWallEvent(req PriceWallEvent) error {
	return a.recordPriceWallEvent(req)
}

func (a *App) FetchKlines(interval string, limit int, startTS, endTS int64) (map[string]any, error) {
	return a.fetchKlines(interval, limit, startTS, endTS)
}

func (a *App) LatestOKXClose() (map[string]any, error) {
	closePrice, ts, err := a.latestOKXClose()
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"exchange": "okx",
		"inst_id":  "ETH-USDT-SWAP",
		"interval": "1m",
		"close":    closePrice,
		"ts":       ts,
	}, nil
}

func AnalysisHTMLFallback() string { return analysisHTMLFallback }
func IndexHTML() string            { return indexHTML }
func MonitorHTML() string          { return monitorHTML }
func MapHTML() string              { return mapHTML }
func ChannelHTMLV2() string        { return channelHTMLV2 }
func LiquidationsHTML() string     { return liquidationsHTML }
func BubblesHTML() string          { return bubblesHTML }
func ConfigHTML() string           { return configHTML }
func WebDataSourceHTML() string    { return webDataSourceHTML }
