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
	home.Mount(mux, liqmap.NewHomeModuleServices(core))
	config.Mount(mux, liqmap.NewConfigModuleServices(core))
	monitor.Mount(mux)
	bookmap.Mount(mux, liqmap.NewBookmapModuleServices(core))
	liquidations.Mount(mux, liqmap.NewLiquidationsModuleServices(core))
	bubbles.Mount(mux, liqmap.NewBubblesModuleServices(core))
	webdatasource.Mount(mux, liqmap.NewWebDataSourceModuleServices(core))
	channel.Mount(mux, liqmap.NewChannelModuleServices(core))
	analysis.Mount(mux, liqmap.NewAnalysisModuleServices(core))
	system.Mount(mux, debug)
	return mux
}
