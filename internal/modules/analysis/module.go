package analysis

import (
	"net/http"

	"multipleexchangeliquidationmap/internal/appctx"
)

func Mount(mux *http.ServeMux, deps *appctx.Dependencies) {
	mux.HandleFunc("/analysis", handlePage)
	mux.HandleFunc("/api/analysis", deps.Core.HandleAnalysisAPI)
}
