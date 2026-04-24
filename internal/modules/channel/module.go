package channel

import (
	"net/http"

	"multipleexchangeliquidationmap/internal/appctx"
)

func Mount(mux *http.ServeMux, deps *appctx.Dependencies) {
	h := newHandlers(deps)
	mux.HandleFunc("/channel", h.handlePage)
	mux.HandleFunc("/api/settings", deps.Core.HandleSettings)
	mux.HandleFunc("/api/channel/test", deps.Core.HandleChannelTest)
	mux.HandleFunc("/api/channel/history", deps.Core.HandleChannelHistory)
	mux.HandleFunc("/api/channel/timeline", deps.Core.HandleChannelTimeline)
	mux.HandleFunc("/api/channel/schedule", deps.Core.HandleChannelSchedule)
}
