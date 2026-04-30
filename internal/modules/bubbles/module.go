package bubbles

import "net/http"

func Mount(mux *http.ServeMux, core Services) {
	svc := newService(core)
	mux.HandleFunc("/bubbles", svc.handlePage)
	mux.HandleFunc("/api/klines", svc.handleKlines)
	mux.HandleFunc("/api/trade-signals", svc.handleTradeSignals)
	mux.HandleFunc("/api/okx/latest-close", svc.handleOKXLatestClose)
}
