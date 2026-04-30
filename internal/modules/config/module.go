package config

import "net/http"

func Mount(mux *http.ServeMux, core Services) {
	svc := newService(core)
	mux.HandleFunc("/config", svc.handlePage)
	mux.HandleFunc("/api/model-config", svc.handleModelConfig)
	mux.HandleFunc("/api/model-fit", svc.handleModelFit)
}
