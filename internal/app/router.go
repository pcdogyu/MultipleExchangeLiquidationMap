package app

import (
	"net/http"

	liqmap "multipleexchangeliquidationmap"
	"multipleexchangeliquidationmap/internal/adapters"
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
	set := adapters.NewSet(core)
	home.Mount(mux, set.Home)
	config.Mount(mux, set.Config)
	monitor.Mount(mux)
	bookmap.Mount(mux, set.Bookmap)
	liquidations.Mount(mux, set.Liquidations)
	bubbles.Mount(mux, set.Bubbles)
	webdatasource.Mount(mux, set.WebDataSource)
	channel.Mount(mux, set.Channel)
	analysis.Mount(mux, set.Analysis)
	system.Mount(mux, debug)
	return mux
}
