package liquidations

import "net/http"

func Mount(mux *http.ServeMux, core Services) {
	svc := newService(core)
	mux.HandleFunc("/liquidations", svc.handlePage)
	mux.HandleFunc("/api/liquidations", svc.handleLiquidations)
}
