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

func (a *App) HandleModelConfig(w http.ResponseWriter, r *http.Request) { a.handleModelConfig(w, r) }
func (a *App) HandleModelFit(w http.ResponseWriter, r *http.Request)    { a.handleModelFit(w, r) }

func (a *App) HandleLiquidationsAPI(w http.ResponseWriter, r *http.Request) {
	a.handleLiquidationsAPI(w, r)
}

func (a *App) HandleKlinesAPI(w http.ResponseWriter, r *http.Request) { a.handleKlinesAPI(w, r) }

func (a *App) HandleOKXLatestCloseAPI(w http.ResponseWriter, r *http.Request) {
	a.handleOKXLatestCloseAPI(w, r)
}

func (a *App) HandleOrderBook(w http.ResponseWriter, r *http.Request)    { a.handleOrderBook(w, r) }
func (a *App) HandleCoinGlassMap(w http.ResponseWriter, r *http.Request) { a.handleCoinGlassMap(w, r) }
func (a *App) HandleWindow(w http.ResponseWriter, r *http.Request)       { a.handleWindow(w, r) }
func (a *App) HandleSettings(w http.ResponseWriter, r *http.Request)     { a.handleSettings(w, r) }
func (a *App) HandleChannelTest(w http.ResponseWriter, r *http.Request)  { a.handleChannelTest(w, r) }

func (a *App) HandleChannelHistory(w http.ResponseWriter, r *http.Request) {
	a.handleChannelHistory(w, r)
}

func (a *App) HandleChannelTimeline(w http.ResponseWriter, r *http.Request) {
	a.handleChannelTimeline(w, r)
}

func (a *App) HandleChannelSchedule(w http.ResponseWriter, r *http.Request) {
	a.handleChannelSchedule(w, r)
}

func (a *App) HandleUpgradePull(w http.ResponseWriter, r *http.Request) { a.handleUpgradePull(w, r) }

func (a *App) HandleUpgradeProgress(w http.ResponseWriter, r *http.Request) {
	a.handleUpgradeProgress(w, r)
}

func (a *App) HandleVersion(w http.ResponseWriter, r *http.Request)     { a.handleVersion(w, r) }
func (a *App) HandlePriceEvents(w http.ResponseWriter, r *http.Request) { a.handlePriceEvents(w, r) }

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

func (a *App) LoadModelConfig() ModelConfig {
	return a.loadModelConfig()
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
