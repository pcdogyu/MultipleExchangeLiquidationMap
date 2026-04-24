package home

import (
	"net/http"

	"multipleexchangeliquidationmap/internal/appctx"
)

func Mount(mux *http.ServeMux, deps *appctx.Dependencies) {
	h := newHandlers(deps)
	mux.HandleFunc("/", h.handlePage)
	mux.HandleFunc("/api/dashboard", deps.Core.HandleDashboard)
	mux.HandleFunc("/api/window", deps.Core.HandleWindow)
	mux.HandleFunc("/api/model/liquidation-map", deps.Core.HandleModelLiquidationMap)
	mux.HandleFunc("/api/coinglass/map", deps.Core.HandleCoinGlassMap)
}
