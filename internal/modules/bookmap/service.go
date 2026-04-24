package bookmap

import liqmap "multipleexchangeliquidationmap"

type Services interface {
	OrderBookView(exchange, mode string, limit int) (any, error)
	ListPriceWallEvents(page, limit, minutes int, side, mode string) (any, error)
	RecordPriceWallEvent(req liqmap.PriceWallEvent) error
}

type service struct {
	core Services
}

func newService(core Services) *service {
	return &service{core: core}
}
