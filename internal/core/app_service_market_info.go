package liqmap

func (a *App) BinanceMarketInfo(symbol, period string, limit int) (MarketInfoResponse, error) {
	return a.MarketInfo(symbol, period, limit)
}
