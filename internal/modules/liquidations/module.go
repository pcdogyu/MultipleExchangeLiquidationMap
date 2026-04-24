package liquidations

import (
	"net/http"

	liqmap "multipleexchangeliquidationmap"
	"multipleexchangeliquidationmap/internal/appctx"
)

func Mount(mux *http.ServeMux, deps *appctx.Dependencies) {
	svc := newService(liqmap.NewLiquidationsModuleServices(deps.Core))
	mux.HandleFunc("/liquidations", svc.handlePage)
	mux.HandleFunc("/api/liquidations", svc.handleLiquidations)
}
