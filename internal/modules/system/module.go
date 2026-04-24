package system

import (
	"net/http"

	"multipleexchangeliquidationmap/internal/appctx"
	platformruntime "multipleexchangeliquidationmap/internal/platform/runtime"
)

func Mount(mux *http.ServeMux, deps *appctx.Dependencies) {
	rt := platformruntime.New(deps.Core)
	mux.HandleFunc("/api/upgrade/pull", rt.HandleUpgradePull)
	mux.HandleFunc("/api/upgrade/progress", rt.HandleUpgradeProgress)
	mux.HandleFunc("/api/version", rt.HandleVersion)
}
