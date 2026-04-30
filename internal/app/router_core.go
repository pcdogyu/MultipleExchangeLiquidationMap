package app

import (
	"net/http"

	"multipleexchangeliquidationmap/internal/adapters"
	"multipleexchangeliquidationmap/internal/modules/analysis"
	"multipleexchangeliquidationmap/internal/modules/home"
)

func mountCoreModules(mux *http.ServeMux, set adapters.CoreSet) {
	home.Mount(mux, set.Home)
	analysis.Mount(mux, set.Analysis)
}
