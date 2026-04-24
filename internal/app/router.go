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
	home.Mount(mux, deps)
	config.Mount(mux, deps)
	monitor.Mount(mux, deps)
	bookmap.Mount(mux, deps)
	liquidations.Mount(mux, deps)
	bubbles.Mount(mux, deps)
	webdatasource.Mount(mux, deps)
	channel.Mount(mux, deps)
	analysis.Mount(mux, deps)
	system.Mount(mux, deps)
	return mux
}
