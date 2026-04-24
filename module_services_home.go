package liqmap

type HomeModuleServices struct {
	app *App
}

func NewHomeModuleServices(app *App) *HomeModuleServices {
	return &HomeModuleServices{app: app}
}

func (s *HomeModuleServices) WindowDays() int {
	return s.app.window()
}

func (s *HomeModuleServices) SetWindowDays(days int) error {
	if normalized, ok := normalizeWindowDays(days); ok {
		s.app.setWindow(normalized)
		return nil
	}
	return BadRequestError{Message: "invalid days"}
}

func (s *HomeModuleServices) Dashboard(days int) (Dashboard, error) {
	return s.app.buildDashboard(days)
}

func (s *HomeModuleServices) ModelLiquidationMap(windowDays, lookbackMin, bucketMin int, priceStep, priceRange float64) (map[string]any, error) {
	return s.app.modelLiquidationMap(windowDays, lookbackMin, bucketMin, priceStep, priceRange)
}

func (s *HomeModuleServices) FetchCoinGlassMap(symbol, window string) ([]byte, error) {
	return s.app.fetchCoinGlassMap(symbol, window)
}
