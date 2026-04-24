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

func (a *App) FetchKlines(interval string, limit int, startTS, endTS int64) (map[string]any, error) {
	return a.fetchKlines(interval, limit, startTS, endTS)
}

func (a *App) LatestOKXClose() (map[string]any, error) {
	closePrice, ts, err := a.latestOKXClose()
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"exchange": "okx",
		"inst_id":  "ETH-USDT-SWAP",
		"interval": "1m",
		"close":    closePrice,
		"ts":       ts,
	}, nil
}

func (a *App) ListLiquidations(limit, offset int, startTS, endTS int64) []EventRow {
	return a.loadLiquidations(defaultSymbol, limit, offset, startTS, endTS)
}

func (a *App) AnalysisSnapshot() (AnalysisSnapshot, error) {
	return a.buildAnalysisSnapshot()
}
