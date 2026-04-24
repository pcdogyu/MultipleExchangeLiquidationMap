package liquidations

import (
	"net/http"

	"multipleexchangeliquidationmap/internal/appctx"
)

func Mount(mux *http.ServeMux, deps *appctx.Dependencies) {
	mux.HandleFunc("/liquidations", handlePage)
	mux.HandleFunc("/api/liquidations", deps.Core.HandleLiquidationsAPI)
}
