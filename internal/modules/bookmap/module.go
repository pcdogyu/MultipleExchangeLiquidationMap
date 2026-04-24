package bookmap

import (
	"net/http"

	"multipleexchangeliquidationmap/internal/appctx"
)

func Mount(mux *http.ServeMux, deps *appctx.Dependencies) {
	svc := newService(deps)
	mux.HandleFunc("/map", svc.handlePage)
	mux.HandleFunc("/api/orderbook", svc.handleOrderBook)
	mux.HandleFunc("/api/price-events", svc.handlePriceEvents)
}
