package adapters

import (
	liqmap "multipleexchangeliquidationmap"
	"multipleexchangeliquidationmap/internal/modules/channel"
	"multipleexchangeliquidationmap/internal/modules/config"
	"multipleexchangeliquidationmap/internal/modules/webdatasource"
)

type AdminSet struct {
	Config        config.Services
	WebDataSource webdatasource.Services
	Channel       channel.Services
}

func NewAdmin(app *liqmap.App) AdminSet {
	return AdminSet{
		Config:        NewConfig(app),
		WebDataSource: NewWebDataSource(app),
		Channel:       NewChannel(app),
	}
}
