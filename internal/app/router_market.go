package app

import (
	"net/http"

	"multipleexchangeliquidationmap/internal/adapters"
	"multipleexchangeliquidationmap/internal/modules/bookmap"
	"multipleexchangeliquidationmap/internal/modules/bubbles"
	"multipleexchangeliquidationmap/internal/modules/liquidations"
)

func mountMarketModules(mux *http.ServeMux, set adapters.MarketSet) {
	bookmap.Mount(mux, set.Bookmap)
	liquidations.Mount(mux, set.Liquidations)
	bubbles.Mount(mux, set.Bubbles)
}
