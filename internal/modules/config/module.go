package config

import (
	"net/http"

	liqmap "multipleexchangeliquidationmap"
)

func Mount(mux *http.ServeMux, core *liqmap.App) {
	svc := newService(liqmap.NewConfigModuleServices(core))
	mux.HandleFunc("/config", svc.handlePage)
	mux.HandleFunc("/api/model-config", svc.handleModelConfig)
	mux.HandleFunc("/api/model-fit", svc.handleModelFit)
}
