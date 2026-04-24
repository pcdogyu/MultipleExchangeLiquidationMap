package analysis

import "net/http"

func Mount(mux *http.ServeMux, core Services) {
	svc := newService(core)
	mux.HandleFunc("/analysis", svc.handlePage)
	mux.HandleFunc("/api/analysis", svc.handleAnalysis)
}
