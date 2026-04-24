package liquidations

import (
	"net/http"

	"multipleexchangeliquidationmap/internal/appctx"
)

func Mount(mux *http.ServeMux, deps *appctx.Dependencies) {
	svc := newService(deps.Core)
	mux.HandleFunc("/liquidations", svc.handlePage)
	mux.HandleFunc("/api/liquidations", svc.handleLiquidations)
}
