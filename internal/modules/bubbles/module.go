package bubbles

import (
	"net/http"

	"multipleexchangeliquidationmap/internal/appctx"
)

func Mount(mux *http.ServeMux, deps *appctx.Dependencies) {
	h := newHandlers(deps)
	mux.HandleFunc("/bubbles", h.handlePage)
	mux.HandleFunc("/api/klines", deps.Core.HandleKlinesAPI)
	mux.HandleFunc("/api/okx/latest-close", deps.Core.HandleOKXLatestCloseAPI)
}
