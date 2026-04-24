package adapters

import (
	liqmap "multipleexchangeliquidationmap/internal/core"
	"multipleexchangeliquidationmap/internal/modules/bookmap"
	"multipleexchangeliquidationmap/internal/modules/bubbles"
	"multipleexchangeliquidationmap/internal/modules/liquidations"
)

type MarketSet struct {
	Bookmap      bookmap.Services
	Liquidations liquidations.Services
	Bubbles      bubbles.Services
}

func NewMarket(app *liqmap.App) MarketSet {
	return MarketSet{
		Bookmap:      NewBookmap(app),
		Liquidations: NewLiquidations(app),
		Bubbles:      NewBubbles(app),
	}
}
