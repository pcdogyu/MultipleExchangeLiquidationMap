package liquidations

import (
	"net/http"

	"multipleexchangeliquidationmap/internal/appctx"
)

func Mount(mux *http.ServeMux, deps *appctx.Dependencies) {
	h := newHandlers(deps)
	mux.HandleFunc("/liquidations", h.handlePage)
	mux.HandleFunc("/api/liquidations", deps.Core.HandleLiquidationsAPI)
}
