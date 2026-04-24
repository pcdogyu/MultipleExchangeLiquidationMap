package adapters

import (
	liqmap "multipleexchangeliquidationmap"
	"multipleexchangeliquidationmap/internal/modules/liquidations"
)

type liquidationsModuleAdapter struct {
	app *liqmap.App
}

func NewLiquidations(app *liqmap.App) liquidations.Services {
	return &liquidationsModuleAdapter{app: app}
}

func (s *liquidationsModuleAdapter) ListLiquidations(limit, offset int, startTS, endTS int64) []liqmap.EventRow {
	return s.app.ListLiquidations(limit, offset, startTS, endTS)
}
