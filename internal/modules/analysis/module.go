package analysis

import "net/http"

func Mount(mux *http.ServeMux, core Services) {
	svc := newService(core)
	mux.HandleFunc("/analysis", svc.handlePage)
	mux.HandleFunc("/analysis-backtest", svc.handleBacktestPage)
	mux.HandleFunc("/analysis-backtest-liquidation", svc.handleBacktestLiquidationPage)
	mux.HandleFunc("/analysis-backtest-2fa", svc.handleBacktest2FAPage)
	mux.HandleFunc("/api/analysis", svc.handleAnalysis)
	mux.HandleFunc("/api/analysis-backtest", svc.handleBacktest)
	mux.HandleFunc("/api/analysis-backtest-2fa", svc.handleBacktest2FA)
	mux.HandleFunc("/api/analysis-backtest/history", svc.handleBacktestHistory)
}
