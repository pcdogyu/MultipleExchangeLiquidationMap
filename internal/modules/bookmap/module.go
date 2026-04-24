package bookmap

import "net/http"

func Mount(mux *http.ServeMux, core Services) {
	svc := newService(core)
	mux.HandleFunc("/map", svc.handlePage)
	mux.HandleFunc("/api/orderbook", svc.handleOrderBook)
	mux.HandleFunc("/api/price-events", svc.handlePriceEvents)
}
