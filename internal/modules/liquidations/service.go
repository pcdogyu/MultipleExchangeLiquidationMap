package liquidations

import liqmap "multipleexchangeliquidationmap"

type Services interface {
	ListLiquidations(limit, offset int, startTS, endTS int64) []liqmap.EventRow
}

type service struct {
	core Services
}

func newService(core Services) *service {
	return &service{core: core}
}
