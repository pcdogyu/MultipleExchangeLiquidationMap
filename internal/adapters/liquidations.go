package adapters

import (
	liqmap "multipleexchangeliquidationmap/internal/core"
	"multipleexchangeliquidationmap/internal/modules/liquidations"
)

type liquidationsModuleAdapter struct {
	app *liqmap.App
}

func NewLiquidations(app *liqmap.App) liquidations.Services {
	return &liquidationsModuleAdapter{app: app}
}

func (s *liquidationsModuleAdapter) QueryLiquidations(opts liqmap.LiquidationListOptions) []liqmap.EventRow {
	return s.app.QueryLiquidations(opts)
}

func (s *liquidationsModuleAdapter) LiquidationSymbols(limit int) []string {
	return s.app.LiquidationSymbols(limit)
}

func (s *liquidationsModuleAdapter) LiquidationWSStatuses() []liqmap.LiquidationWSStatus {
	return s.app.LiquidationWSStatuses()
}

func (s *liquidationsModuleAdapter) RetryLiquidationExchange(exchange string) error {
	return s.app.RetryLiquidationExchange(exchange)
}
