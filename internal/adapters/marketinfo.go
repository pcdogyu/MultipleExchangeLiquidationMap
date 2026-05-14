package adapters

import (
	liqmap "multipleexchangeliquidationmap/internal/core"
	"multipleexchangeliquidationmap/internal/modules/marketinfo"
)

type marketInfoModuleAdapter struct {
	app *liqmap.App
}

func NewMarketInfo(app *liqmap.App) marketinfo.Services {
	return &marketInfoModuleAdapter{app: app}
}

func (s *marketInfoModuleAdapter) BinanceMarketInfo(symbol, period string, limit int) (liqmap.MarketInfoResponse, error) {
	return s.app.BinanceMarketInfo(symbol, period, limit)
}
