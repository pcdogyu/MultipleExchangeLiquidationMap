package liqmap

type HomeModuleAdapter struct {
	app *App
}

func NewHomeModuleAdapter(app *App) *HomeModuleAdapter {
	return &HomeModuleAdapter{app: app}
}

func (s *HomeModuleAdapter) WindowDays() int {
	return s.app.window()
}

func (s *HomeModuleAdapter) SetWindowDays(days int) error {
	if normalized, ok := normalizeWindowDays(days); ok {
		s.app.setWindow(normalized)
		return nil
	}
	return BadRequestError{Message: "invalid days"}
}

func (s *HomeModuleAdapter) Dashboard(days int) (Dashboard, error) {
	return s.app.buildDashboard(days)
}

func (s *HomeModuleAdapter) ModelLiquidationMap(windowDays, lookbackMin, bucketMin int, priceStep, priceRange float64) (map[string]any, error) {
	return s.app.modelLiquidationMap(windowDays, lookbackMin, bucketMin, priceStep, priceRange)
}

func (s *HomeModuleAdapter) FetchCoinGlassMap(symbol, window string) ([]byte, error) {
	return s.app.fetchCoinGlassMap(symbol, window)
}
