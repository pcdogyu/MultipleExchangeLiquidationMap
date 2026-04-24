package bookmap

import (
	"net/http"

	liqmap "multipleexchangeliquidationmap"
	"multipleexchangeliquidationmap/internal/appctx"
)

func Mount(mux *http.ServeMux, deps *appctx.Dependencies) {
	svc := newService(liqmap.NewBookmapModuleServices(deps.Core))
	mux.HandleFunc("/map", svc.handlePage)
	mux.HandleFunc("/api/orderbook", svc.handleOrderBook)
	mux.HandleFunc("/api/price-events", svc.handlePriceEvents)
}
