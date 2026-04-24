package liqmap

type BookmapModuleAdapter struct {
	app *App
}

func NewBookmapModuleAdapter(app *App) *BookmapModuleAdapter {
	return &BookmapModuleAdapter{app: app}
}

func (s *BookmapModuleAdapter) OrderBookView(exchange, mode string, limit int) (any, error) {
	return s.app.orderBookView(exchange, mode, limit)
}

func (s *BookmapModuleAdapter) ListPriceWallEvents(page, limit, minutes int, side, mode string) (any, error) {
	return s.app.listPriceWallEvents(page, limit, minutes, side, mode)
}

func (s *BookmapModuleAdapter) RecordPriceWallEvent(req PriceWallEvent) error {
	return s.app.recordPriceWallEvent(req)
}
