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
	Home          home.Services
	Config        config.Services
	Bookmap       bookmap.Services
	Liquidations  liquidations.Services
	Bubbles       bubbles.Services
	WebDataSource webdatasource.Services
	Channel       channel.Services
	Analysis      analysis.Services
}

func NewSet(app *liqmap.App) Set {
	return Set{
		Home:          NewHome(app),
		Config:        NewConfig(app),
		Bookmap:       NewBookmap(app),
		Liquidations:  NewLiquidations(app),
		Bubbles:       NewBubbles(app),
		WebDataSource: NewWebDataSource(app),
		Channel:       NewChannel(app),
		Analysis:      NewAnalysis(app),
	}
}
