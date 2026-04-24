package adapters

import (
	liqmap "multipleexchangeliquidationmap"
	"multipleexchangeliquidationmap/internal/modules/bookmap"
)

type bookmapModuleAdapter struct {
	app *liqmap.App
}

func NewBookmap(app *liqmap.App) bookmap.Services {
	return &bookmapModuleAdapter{app: app}
}

func (s *bookmapModuleAdapter) OrderBookView(exchange, mode string, limit int) (any, error) {
	return s.app.OrderBookView(exchange, mode, limit)
}

func (s *bookmapModuleAdapter) ListPriceWallEvents(page, limit, minutes int, side, mode string) (any, error) {
	return s.app.ListPriceWallEvents(page, limit, minutes, side, mode)
}

func (s *bookmapModuleAdapter) RecordPriceWallEvent(req liqmap.PriceWallEvent) error {
	return s.app.RecordPriceWallEvent(req)
}
