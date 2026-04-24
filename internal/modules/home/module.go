package home

import (
	"net/http"

	liqmap "multipleexchangeliquidationmap"
	"multipleexchangeliquidationmap/internal/appctx"
)

func Mount(mux *http.ServeMux, deps *appctx.Dependencies) {
	svc := newService(liqmap.NewHomeModuleServices(deps.Core))
	mux.HandleFunc("/", svc.handlePage)
	mux.HandleFunc("/api/dashboard", svc.handleDashboard)
	mux.HandleFunc("/api/window", svc.handleWindow)
	mux.HandleFunc("/api/model/liquidation-map", svc.handleModelLiquidationMap)
	mux.HandleFunc("/api/coinglass/map", svc.handleCoinGlassMap)
}
