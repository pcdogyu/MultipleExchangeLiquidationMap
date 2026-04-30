package app

import (
	"net/http"

	"multipleexchangeliquidationmap/internal/adapters"
	"multipleexchangeliquidationmap/internal/modules/channel"
	"multipleexchangeliquidationmap/internal/modules/config"
	"multipleexchangeliquidationmap/internal/modules/webdatasource"
)

func mountAdminModules(mux *http.ServeMux, set adapters.AdminSet) {
	config.Mount(mux, set.Config)
	webdatasource.Mount(mux, set.WebDataSource)
	channel.Mount(mux, set.Channel)
}
