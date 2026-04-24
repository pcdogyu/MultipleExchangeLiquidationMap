package bubbles

import (
	"net/http"

	"multipleexchangeliquidationmap/internal/appctx"
)

func Mount(mux *http.ServeMux, deps *appctx.Dependencies) {
	svc := newService(deps.Core)
	mux.HandleFunc("/bubbles", svc.handlePage)
	mux.HandleFunc("/api/klines", svc.handleKlines)
	mux.HandleFunc("/api/okx/latest-close", svc.handleOKXLatestClose)
}
