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
	home.Mount(mux, set.Core.Home)
	config.Mount(mux, set.Admin.Config)
	monitor.Mount(mux)
	bookmap.Mount(mux, set.Market.Bookmap)
	liquidations.Mount(mux, set.Market.Liquidations)
	bubbles.Mount(mux, set.Market.Bubbles)
	webdatasource.Mount(mux, set.Admin.WebDataSource)
	channel.Mount(mux, set.Admin.Channel)
	analysis.Mount(mux, set.Core.Analysis)
	system.Mount(mux, debug)
	return mux
}
