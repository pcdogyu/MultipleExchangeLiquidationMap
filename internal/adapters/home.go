package adapters

import (
	liqmap "multipleexchangeliquidationmap/internal/core"
	"multipleexchangeliquidationmap/internal/modules/home"
)

type homeModuleAdapter struct {
	app *liqmap.App
}

func NewHome(app *liqmap.App) home.Services {
	return &homeModuleAdapter{app: app}
}

func (s *homeModuleAdapter) WindowDays() int {
	return s.app.WindowDays()
}

func (s *homeModuleAdapter) SetWindowDays(days int) error {
	return s.app.SetWindowDays(days)
}

func (s *homeModuleAdapter) Dashboard(days int) (liqmap.Dashboard, error) {
	return s.app.Dashboard(days)
}

func (s *homeModuleAdapter) ModelLiquidationMap(windowDays, lookbackMin, bucketMin int, priceStep, priceRange float64) (map[string]any, error) {
	return s.app.ModelLiquidationMap(windowDays, lookbackMin, bucketMin, priceStep, priceRange)
}

func (s *homeModuleAdapter) FetchCoinGlassMap(symbol, window string) ([]byte, error) {
	return s.app.FetchCoinGlassMap(symbol, window)
}
