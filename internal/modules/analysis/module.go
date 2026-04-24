package analysis

import (
	"net/http"

	liqmap "multipleexchangeliquidationmap"
)

func Mount(mux *http.ServeMux, core *liqmap.App) {
	svc := newService(liqmap.NewAnalysisModuleServices(core))
	mux.HandleFunc("/analysis", svc.handlePage)
	mux.HandleFunc("/api/analysis", svc.handleAnalysis)
}
