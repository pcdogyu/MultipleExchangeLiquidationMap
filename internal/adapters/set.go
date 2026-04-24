package adapters

import (
	liqmap "multipleexchangeliquidationmap"
	"multipleexchangeliquidationmap/internal/modules/analysis"
	"multipleexchangeliquidationmap/internal/modules/bookmap"
	"multipleexchangeliquidationmap/internal/modules/bubbles"
	"multipleexchangeliquidationmap/internal/modules/channel"
	"multipleexchangeliquidationmap/internal/modules/config"
	"multipleexchangeliquidationmap/internal/modules/home"
	"multipleexchangeliquidationmap/internal/modules/liquidations"
	"multipleexchangeliquidationmap/internal/modules/webdatasource"
)

type Set struct {
	Core   CoreSet
	Market MarketSet
	Admin  AdminSet
}

type CoreSet struct {
	Home     home.Services
	Analysis analysis.Services
}

type MarketSet struct {
	Bookmap      bookmap.Services
	Liquidations liquidations.Services
	Bubbles      bubbles.Services
}

type AdminSet struct {
	Config        config.Services
	WebDataSource webdatasource.Services
	Channel       channel.Services
}

func NewCore(app *liqmap.App) CoreSet {
	return CoreSet{
		Home:     NewHome(app),
		Analysis: NewAnalysis(app),
	}
}

func NewMarket(app *liqmap.App) MarketSet {
	return MarketSet{
		Bookmap:      NewBookmap(app),
		Liquidations: NewLiquidations(app),
		Bubbles:      NewBubbles(app),
	}
}

func NewAdmin(app *liqmap.App) AdminSet {
	return AdminSet{
		Config:        NewConfig(app),
		WebDataSource: NewWebDataSource(app),
		Channel:       NewChannel(app),
	}
}

func NewSet(app *liqmap.App) Set {
	return Set{
		Core:   NewCore(app),
		Market: NewMarket(app),
		Admin:  NewAdmin(app),
	}
}
