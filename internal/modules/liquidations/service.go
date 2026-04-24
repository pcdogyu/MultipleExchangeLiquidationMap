package liquidations

import liqmap "multipleexchangeliquidationmap/internal/core"

type Services interface {
	QueryLiquidations(opts liqmap.LiquidationListOptions) []liqmap.EventRow
	LiquidationSymbols(limit int) []string
	LiquidationWSStatuses() []liqmap.LiquidationWSStatus
}

type service struct {
	core Services
}

func newService(core Services) *service {
	return &service{core: core}
}
