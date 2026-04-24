package config

import (
	"net/http"

	"multipleexchangeliquidationmap/internal/appctx"
)

func Mount(mux *http.ServeMux, deps *appctx.Dependencies) {
	h := newHandlers(deps)
	mux.HandleFunc("/config", h.handlePage)
	mux.HandleFunc("/api/model-config", deps.Core.HandleModelConfig)
	mux.HandleFunc("/api/model-fit", deps.Core.HandleModelFit)
}
