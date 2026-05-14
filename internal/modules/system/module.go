package system

import (
	"net/http"

	platformruntime "multipleexchangeliquidationmap/internal/platform/runtime"
)

func Mount(mux *http.ServeMux, debug bool) {
	rt := platformruntime.New(debug)
	mux.HandleFunc("/api/upgrade/pull", rt.HandleUpgradePull)
	mux.HandleFunc("/api/upgrade/progress", rt.HandleUpgradeProgress)
	mux.HandleFunc("/api/version", rt.HandleVersion)
}
