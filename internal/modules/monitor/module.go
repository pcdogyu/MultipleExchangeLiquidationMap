package monitor

import (
	"net/http"
)

func Mount(mux *http.ServeMux) {
	svc := newService()
	mux.HandleFunc("/monitor", svc.handlePage)
}
