package liqmap

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
