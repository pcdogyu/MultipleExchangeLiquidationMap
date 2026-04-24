package webdatasource

import (
	"net/http"

	liqmap "multipleexchangeliquidationmap"
)

func Mount(mux *http.ServeMux, core *liqmap.App) {
	svc := newService(liqmap.NewWebDataSourceModuleServices(core))
	mux.HandleFunc("/webdatasource", svc.handlePage)
	mux.HandleFunc("/api/webdatasource/status", svc.handleStatus)
	mux.HandleFunc("/api/webdatasource/init", svc.handleInit)
	mux.HandleFunc("/api/webdatasource/run", svc.handleRun)
	mux.HandleFunc("/api/webdatasource/map", svc.handleMap)
	mux.HandleFunc("/api/webdatasource/runs", svc.handleRuns)
	mux.HandleFunc("/api/webdatasource/settings", svc.handleSettings)
}
