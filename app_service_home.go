package liqmap

func (a *App) WindowDays() int {
	return a.window()
}

func (a *App) SetWindowDays(days int) error {
	if normalized, ok := normalizeWindowDays(days); ok {
		a.setWindow(normalized)
		return nil
	}
	return BadRequestError{Message: "invalid days"}
}

func (a *App) Dashboard(days int) (Dashboard, error) {
	return a.buildDashboard(days)
}

func (a *App) ModelLiquidationMap(windowDays, lookbackMin, bucketMin int, priceStep, priceRange float64) (map[string]any, error) {
	return a.modelLiquidationMap(windowDays, lookbackMin, bucketMin, priceStep, priceRange)
}

func (a *App) FetchCoinGlassMap(symbol, window string) ([]byte, error) {
	return a.fetchCoinGlassMap(symbol, window)
}
