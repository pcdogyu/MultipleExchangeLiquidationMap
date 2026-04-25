package liquidations

import liqmap "multipleexchangeliquidationmap/internal/core"

type Services interface {
	QueryLiquidations(opts liqmap.LiquidationListOptions) []liqmap.EventRow
	LiquidationPeriodSummary(opts liqmap.LiquidationListOptions) liqmap.LiquidationPeriodSummary
	LiquidationSymbols(limit int) []string
	LiquidationWSStatuses() []liqmap.LiquidationWSStatus
	RetryLiquidationExchange(exchange string) error
}

type service struct {
	core Services
}

func newService(core Services) *service {
	return &service{core: core}
}
