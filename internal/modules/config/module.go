package config

import (
	"net/http"

	liqmap "multipleexchangeliquidationmap"
	"multipleexchangeliquidationmap/internal/appctx"
)

func Mount(mux *http.ServeMux, deps *appctx.Dependencies) {
	svc := newService(liqmap.NewConfigModuleServices(deps.Core))
	mux.HandleFunc("/config", svc.handlePage)
	mux.HandleFunc("/api/model-config", svc.handleModelConfig)
	mux.HandleFunc("/api/model-fit", svc.handleModelFit)
}
