package bookmap

import liqmap "multipleexchangeliquidationmap"

type bookmapCore interface {
	OrderBookView(exchange, mode string, limit int) (any, error)
	ListPriceWallEvents(page, limit, minutes int, side, mode string) (any, error)
	RecordPriceWallEvent(req liqmap.PriceWallEvent) error
}

type service struct {
	core bookmapCore
}

func newService(core bookmapCore) *service {
	return &service{core: core}
}
