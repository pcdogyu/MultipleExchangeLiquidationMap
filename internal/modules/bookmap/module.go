package bookmap

import (
	"net/http"

	"multipleexchangeliquidationmap/internal/appctx"
)

func Mount(mux *http.ServeMux, deps *appctx.Dependencies) {
	mux.HandleFunc("/map", handlePage)
	mux.HandleFunc("/api/orderbook", deps.Core.HandleOrderBook)
	mux.HandleFunc("/api/price-events", deps.Core.HandlePriceEvents)
}
