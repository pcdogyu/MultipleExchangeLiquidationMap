package bookmap

import (
	"net/http"

	liqmap "multipleexchangeliquidationmap"
)

func Mount(mux *http.ServeMux, core *liqmap.App) {
	svc := newService(liqmap.NewBookmapModuleServices(core))
	mux.HandleFunc("/map", svc.handlePage)
	mux.HandleFunc("/api/orderbook", svc.handleOrderBook)
	mux.HandleFunc("/api/price-events", svc.handlePriceEvents)
}
