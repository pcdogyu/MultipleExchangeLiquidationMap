package adapters

import liqmap "multipleexchangeliquidationmap"

type Set struct {
	Core   CoreSet
	Market MarketSet
	Admin  AdminSet
}

func NewSet(app *liqmap.App) Set {
	return Set{
		Core:   NewCore(app),
		Market: NewMarket(app),
		Admin:  NewAdmin(app),
	}
}
