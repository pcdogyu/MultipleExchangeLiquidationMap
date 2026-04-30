package home

import liqmap "multipleexchangeliquidationmap/internal/core"

type Services interface {
	WindowDays() int
	SetWindowDays(days int) error
	Dashboard(days int) (liqmap.Dashboard, error)
	ModelLiquidationMap(windowDays, lookbackMin, bucketMin int, priceStep, priceRange float64) (map[string]any, error)
	FetchCoinGlassMap(symbol, window string) ([]byte, error)
}

type service struct {
	core Services
}

func newService(core Services) *service {
	return &service{core: core}
}
