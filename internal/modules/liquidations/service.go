package liquidations

import liqmap "multipleexchangeliquidationmap"

type liquidationsCore interface {
	ListLiquidations(limit, offset int, startTS, endTS int64) []liqmap.EventRow
}

type service struct {
	core liquidationsCore
}

func newService(core liquidationsCore) *service {
	return &service{core: core}
}
