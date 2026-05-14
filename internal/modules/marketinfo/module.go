package marketinfo

import "net/http"

func Mount(mux *http.ServeMux, core Services) {
	svc := newService(core)
	mux.HandleFunc("/market-info", svc.handlePage)
	mux.HandleFunc("/api/market-info", svc.handleMarketInfo)
}
