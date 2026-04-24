package home

import (
	"net/http"

	"multipleexchangeliquidationmap/internal/appctx"
)

func Mount(mux *http.ServeMux, deps *appctx.Dependencies) {
	svc := newService(deps)
	mux.HandleFunc("/", svc.handlePage)
	mux.HandleFunc("/api/dashboard", svc.handleDashboard)
	mux.HandleFunc("/api/window", svc.handleWindow)
	mux.HandleFunc("/api/model/liquidation-map", svc.handleModelLiquidationMap)
	mux.HandleFunc("/api/coinglass/map", svc.handleCoinGlassMap)
}
