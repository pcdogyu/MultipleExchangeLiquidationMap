package liquidations

import (
	"net/http"

	liqmap "multipleexchangeliquidationmap"
)

func Mount(mux *http.ServeMux, core *liqmap.App) {
	svc := newService(liqmap.NewLiquidationsModuleServices(core))
	mux.HandleFunc("/liquidations", svc.handlePage)
	mux.HandleFunc("/api/liquidations", svc.handleLiquidations)
}
