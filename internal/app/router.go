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
	home.Mount(mux, liqmap.NewHomeModuleAdapter(core))
	config.Mount(mux, liqmap.NewConfigModuleAdapter(core))
	monitor.Mount(mux)
	bookmap.Mount(mux, adapters.NewBookmap(core))
	liquidations.Mount(mux, adapters.NewLiquidations(core))
	bubbles.Mount(mux, adapters.NewBubbles(core))
	webdatasource.Mount(mux, liqmap.NewWebDataSourceModuleAdapter(core))
	channel.Mount(mux, liqmap.NewChannelModuleAdapter(core))
	analysis.Mount(mux, adapters.NewAnalysis(core))
	system.Mount(mux, debug)
	return mux
}
