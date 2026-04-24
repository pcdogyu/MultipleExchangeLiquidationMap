package home

import "net/http"

func Mount(mux *http.ServeMux, core Services) {
	svc := newService(core)
	mux.HandleFunc("/", svc.handlePage)
	mux.HandleFunc("/api/dashboard", svc.handleDashboard)
	mux.HandleFunc("/api/window", svc.handleWindow)
	mux.HandleFunc("/api/model/liquidation-map", svc.handleModelLiquidationMap)
	mux.HandleFunc("/api/coinglass/map", svc.handleCoinGlassMap)
}
