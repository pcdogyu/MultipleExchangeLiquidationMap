package analysis

import (
	"net/http"

	"multipleexchangeliquidationmap/internal/appctx"
)

func Mount(mux *http.ServeMux, deps *appctx.Dependencies) {
	h := newHandlers(deps)
	mux.HandleFunc("/analysis", h.handlePage)
	mux.HandleFunc("/api/analysis", deps.Core.HandleAnalysisAPI)
}
