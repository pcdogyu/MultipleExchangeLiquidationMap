package bookmap

import (
	"net/http"

	"multipleexchangeliquidationmap/internal/appctx"
)

func Mount(mux *http.ServeMux, deps *appctx.Dependencies) {
	h := newHandlers(deps)
	mux.HandleFunc("/map", h.handlePage)
	mux.HandleFunc("/api/orderbook", deps.Core.HandleOrderBook)
	mux.HandleFunc("/api/price-events", deps.Core.HandlePriceEvents)
}
