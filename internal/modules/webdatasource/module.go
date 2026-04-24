package webdatasource

import (
	"net/http"

	"multipleexchangeliquidationmap/internal/appctx"
)

func Mount(mux *http.ServeMux, deps *appctx.Dependencies) {
	mux.HandleFunc("/webdatasource", handlePage)
	mux.HandleFunc("/api/webdatasource/status", deps.Core.HandleWebDataSourceStatus)
	mux.HandleFunc("/api/webdatasource/init", deps.Core.HandleWebDataSourceInit)
	mux.HandleFunc("/api/webdatasource/run", deps.Core.HandleWebDataSourceRun)
	mux.HandleFunc("/api/webdatasource/map", deps.Core.HandleWebDataSourceMap)
	mux.HandleFunc("/api/webdatasource/runs", deps.Core.HandleWebDataSourceRuns)
	mux.HandleFunc("/api/webdatasource/settings", deps.Core.HandleWebDataSourceSettings)
}
