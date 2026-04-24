package app

import (
	"net/http"

	liqmap "multipleexchangeliquidationmap"
	"multipleexchangeliquidationmap/internal/modules/analysis"
	"multipleexchangeliquidationmap/internal/modules/bookmap"
	"multipleexchangeliquidationmap/internal/modules/bubbles"
	"multipleexchangeliquidationmap/internal/modules/channel"
	"multipleexchangeliquidationmap/internal/modules/config"
	"multipleexchangeliquidationmap/internal/modules/home"
	"multipleexchangeliquidationmap/internal/modules/liquidations"
	"multipleexchangeliquidationmap/internal/modules/monitor"
	"multipleexchangeliquidationmap/internal/modules/system"
	"multipleexchangeliquidationmap/internal/modules/webdatasource"
)

func NewRouter(core *liqmap.App, debug bool) *http.ServeMux {
	mux := http.NewServeMux()
	home.Mount(mux, core)
	config.Mount(mux, core)
	monitor.Mount(mux)
	bookmap.Mount(mux, core)
	liquidations.Mount(mux, core)
	bubbles.Mount(mux, core)
	webdatasource.Mount(mux, core)
	channel.Mount(mux, core)
	analysis.Mount(mux, core)
	system.Mount(mux, debug)
	return mux
}
