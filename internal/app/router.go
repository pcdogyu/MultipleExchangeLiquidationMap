package app

import (
	"net/http"

	liqmap "multipleexchangeliquidationmap"
	"multipleexchangeliquidationmap/internal/adapters"
	"multipleexchangeliquidationmap/internal/modules/monitor"
	"multipleexchangeliquidationmap/internal/modules/system"
)

func NewRouter(core *liqmap.App, debug bool) *http.ServeMux {
	mux := http.NewServeMux()
	set := adapters.NewSet(core)
	mountCoreModules(mux, set.Core)
	mountAdminModules(mux, set.Admin)
	monitor.Mount(mux)
	mountMarketModules(mux, set.Market)
	system.Mount(mux, debug)
	return mux
}
