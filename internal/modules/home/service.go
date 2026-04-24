package home

import liqmap "multipleexchangeliquidationmap"

type homeCore interface {
	WindowDays() int
	SetWindowDays(days int) error
	Dashboard(days int) (liqmap.Dashboard, error)
	ModelLiquidationMap(windowDays, lookbackMin, bucketMin int, priceStep, priceRange float64) (map[string]any, error)
	FetchCoinGlassMap(symbol, window string) ([]byte, error)
}

type service struct {
	core homeCore
}

func newService(core homeCore) *service {
	return &service{core: core}
}
