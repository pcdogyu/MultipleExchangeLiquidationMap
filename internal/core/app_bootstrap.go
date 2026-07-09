package liqmap

import (
	"database/sql"
	"net/http"
	"time"

	dbplatform "multipleexchangeliquidationmap/internal/platform/db"
)

func NewApp(rawDB any, debug bool) *App {
	db := normalizeAppDB(rawDB)
	app := &App{
		db: db,
		httpClient: &http.Client{
			Timeout: 12 * time.Second,
		},
		ob:                  newOrderBookHub(),
		apiGuards:           map[string]*ExchangeAPIGuard{},
		marketInfoRefreshes: map[string]time.Time{},
		retrySignals: map[string]chan struct{}{
			"binance": make(chan struct{}, 1),
			"bybit":   make(chan struct{}, 1),
			"okx":     make(chan struct{}, 1),
		},
		liqWS:      map[string]*liquidationWSState{},
		liqSymbols: map[string]struct{}{defaultSymbol: {}},
		windowDays: defaultWindowDays,
		debug:      debug,
	}
	app.webds = newWebDataSourceManager(app)
	return app
}

func normalizeAppDB(rawDB any) *dbplatform.DB {
	switch db := rawDB.(type) {
	case *dbplatform.DB:
		return db
	case *sql.DB:
		return dbplatform.WrapSQLite(db)
	default:
		return nil
	}
}
