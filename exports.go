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

func AnalysisHTMLFallback() string { return analysisHTMLFallback }
func IndexHTML() string            { return indexHTML }
func MonitorHTML() string          { return monitorHTML }
func MapHTML() string              { return mapHTML }
func ChannelHTMLV2() string        { return channelHTMLV2 }
func LiquidationsHTML() string     { return liquidationsHTML }
func BubblesHTML() string          { return bubblesHTML }
func ConfigHTML() string           { return configHTML }
func WebDataSourceHTML() string    { return webDataSourceHTML }
