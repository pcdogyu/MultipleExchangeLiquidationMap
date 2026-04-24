package config

import (
	"net/http"

	"multipleexchangeliquidationmap/internal/appctx"
)

func Mount(mux *http.ServeMux, deps *appctx.Dependencies) {
	svc := newService(deps)
	mux.HandleFunc("/config", svc.handlePage)
	mux.HandleFunc("/api/model-config", svc.handleModelConfig)
	mux.HandleFunc("/api/model-fit", svc.handleModelFit)
}
