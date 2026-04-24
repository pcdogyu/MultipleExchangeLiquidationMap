package channel

import (
	"net/http"

	liqmap "multipleexchangeliquidationmap"
)

func Mount(mux *http.ServeMux, core *liqmap.App) {
	svc := newService(liqmap.NewChannelModuleServices(core))
	mux.HandleFunc("/channel", svc.handlePage)
	mux.HandleFunc("/api/settings", svc.handleSettings)
	mux.HandleFunc("/api/channel/test", svc.handleChannelTest)
	mux.HandleFunc("/api/channel/history", svc.handleChannelHistory)
	mux.HandleFunc("/api/channel/timeline", svc.handleChannelTimeline)
	mux.HandleFunc("/api/channel/schedule", svc.handleChannelSchedule)
}
