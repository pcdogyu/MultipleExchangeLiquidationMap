package app

import (
	"net/http"

	"multipleexchangeliquidationmap/internal/adapters"
	liqmap "multipleexchangeliquidationmap/internal/core"
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
