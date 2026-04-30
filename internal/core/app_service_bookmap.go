package liqmap

func (a *App) OrderBookView(exchange, mode string, limit int) (any, error) {
	return a.orderBookView(exchange, mode, limit)
}

func (a *App) ListPriceWallEvents(page, limit, minutes int, side, mode string) (any, error) {
	return a.listPriceWallEvents(page, limit, minutes, side, mode)
}

func (a *App) RecordPriceWallEvent(req PriceWallEvent) error {
	return a.recordPriceWallEvent(req)
}
