package liqmap

import (
	"database/sql"
	"net/http"
	"time"
)

func NewApp(db *sql.DB, debug bool) *App {
	app := &App{
		db: db,
		httpClient: &http.Client{
			Timeout: 12 * time.Second,
		},
		ob:         newOrderBookHub(),
		apiGuards:  map[string]*ExchangeAPIGuard{},
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
