package webdatasource

import (
	"net/http"

	"multipleexchangeliquidationmap/internal/appctx"
)

func Mount(mux *http.ServeMux, deps *appctx.Dependencies) {
	h := newHandlers(deps)
	mux.HandleFunc("/webdatasource", h.handlePage)
	mux.HandleFunc("/api/webdatasource/status", deps.Core.HandleWebDataSourceStatus)
	mux.HandleFunc("/api/webdatasource/init", deps.Core.HandleWebDataSourceInit)
	mux.HandleFunc("/api/webdatasource/run", deps.Core.HandleWebDataSourceRun)
	mux.HandleFunc("/api/webdatasource/map", deps.Core.HandleWebDataSourceMap)
	mux.HandleFunc("/api/webdatasource/runs", deps.Core.HandleWebDataSourceRuns)
	mux.HandleFunc("/api/webdatasource/settings", deps.Core.HandleWebDataSourceSettings)
}
