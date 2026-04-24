package liqmap

type BubblesModuleAdapter struct {
	app *App
}

func NewBubblesModuleAdapter(app *App) *BubblesModuleAdapter {
	return &BubblesModuleAdapter{app: app}
}

func (s *BubblesModuleAdapter) FetchKlines(interval string, limit int, startTS, endTS int64) (map[string]any, error) {
	return s.app.fetchKlines(interval, limit, startTS, endTS)
}

func (s *BubblesModuleAdapter) LatestOKXClose() (map[string]any, error) {
	closePrice, ts, err := s.app.latestOKXClose()
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
