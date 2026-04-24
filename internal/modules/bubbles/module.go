package bubbles

import (
	"net/http"

	liqmap "multipleexchangeliquidationmap"
	"multipleexchangeliquidationmap/internal/appctx"
)

func Mount(mux *http.ServeMux, deps *appctx.Dependencies) {
	svc := newService(liqmap.NewBubblesModuleServices(deps.Core))
	mux.HandleFunc("/bubbles", svc.handlePage)
	mux.HandleFunc("/api/klines", svc.handleKlines)
	mux.HandleFunc("/api/okx/latest-close", svc.handleOKXLatestClose)
}
