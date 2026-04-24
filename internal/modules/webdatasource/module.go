package webdatasource

import (
	"net/http"

	"multipleexchangeliquidationmap/internal/appctx"
)

func Mount(mux *http.ServeMux, deps *appctx.Dependencies) {
	svc := newService(deps.Core)
	mux.HandleFunc("/webdatasource", svc.handlePage)
	mux.HandleFunc("/api/webdatasource/status", svc.handleStatus)
	mux.HandleFunc("/api/webdatasource/init", svc.handleInit)
	mux.HandleFunc("/api/webdatasource/run", svc.handleRun)
	mux.HandleFunc("/api/webdatasource/map", svc.handleMap)
	mux.HandleFunc("/api/webdatasource/runs", svc.handleRuns)
	mux.HandleFunc("/api/webdatasource/settings", svc.handleSettings)
}
