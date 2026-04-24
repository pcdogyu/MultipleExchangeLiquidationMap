package analysis

import (
	"net/http"

	liqmap "multipleexchangeliquidationmap"
	"multipleexchangeliquidationmap/internal/appctx"
)

func Mount(mux *http.ServeMux, deps *appctx.Dependencies) {
	svc := newService(liqmap.NewAnalysisModuleServices(deps.Core))
	mux.HandleFunc("/analysis", svc.handlePage)
	mux.HandleFunc("/api/analysis", svc.handleAnalysis)
}
