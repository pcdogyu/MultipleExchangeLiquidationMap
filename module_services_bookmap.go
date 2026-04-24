package liqmap

type BookmapModuleServices struct {
	app *App
}

func NewBookmapModuleServices(app *App) *BookmapModuleServices {
	return &BookmapModuleServices{app: app}
}

func (s *BookmapModuleServices) OrderBookView(exchange, mode string, limit int) (any, error) {
	return s.app.orderBookView(exchange, mode, limit)
}

func (s *BookmapModuleServices) ListPriceWallEvents(page, limit, minutes int, side, mode string) (any, error) {
	return s.app.listPriceWallEvents(page, limit, minutes, side, mode)
}

func (s *BookmapModuleServices) RecordPriceWallEvent(req PriceWallEvent) error {
	return s.app.recordPriceWallEvent(req)
}
