package app

import (
	"net/http"

	"multipleexchangeliquidationmap/internal/appctx"
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

func NewRouter(deps *appctx.Dependencies) *http.ServeMux {
	mux := http.NewServeMux()
	home.Mount(mux, deps.Core)
	config.Mount(mux, deps.Core)
	monitor.Mount(mux)
	bookmap.Mount(mux, deps.Core)
	liquidations.Mount(mux, deps.Core)
	bubbles.Mount(mux, deps.Core)
	webdatasource.Mount(mux, deps.Core)
	channel.Mount(mux, deps.Core)
	analysis.Mount(mux, deps.Core)
	system.Mount(mux, deps.Debug)
	return mux
}
