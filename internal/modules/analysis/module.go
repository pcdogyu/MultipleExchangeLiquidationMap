package analysis

import "net/http"

func Mount(mux *http.ServeMux, core Services) {
	svc := newService(core)
	mux.HandleFunc("/analysis", svc.handlePage)
	mux.HandleFunc("/analysis-backtest", svc.handleBacktestPage)
	mux.HandleFunc("/api/analysis", svc.handleAnalysis)
	mux.HandleFunc("/api/analysis-backtest", svc.handleBacktest)
	mux.HandleFunc("/api/analysis-backtest/history", svc.handleBacktestHistory)
}
