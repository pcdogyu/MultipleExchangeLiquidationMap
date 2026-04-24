package analysis

import (
	"net/http"

	"multipleexchangeliquidationmap/internal/appctx"
)

func Mount(mux *http.ServeMux, deps *appctx.Dependencies) {
	svc := newService(deps)
	mux.HandleFunc("/analysis", svc.handlePage)
	mux.HandleFunc("/api/analysis", svc.handleAnalysis)
}
