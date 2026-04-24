package bubbles

import (
	"net/http"

	liqmap "multipleexchangeliquidationmap"
)

func Mount(mux *http.ServeMux, core *liqmap.App) {
	svc := newService(liqmap.NewBubblesModuleServices(core))
	mux.HandleFunc("/bubbles", svc.handlePage)
	mux.HandleFunc("/api/klines", svc.handleKlines)
	mux.HandleFunc("/api/okx/latest-close", svc.handleOKXLatestClose)
}
