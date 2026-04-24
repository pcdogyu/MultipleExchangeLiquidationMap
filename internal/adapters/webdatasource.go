package adapters

import (
	liqmap "multipleexchangeliquidationmap/internal/core"
	"multipleexchangeliquidationmap/internal/modules/webdatasource"
)

type webDataSourceModuleAdapter struct {
	app *liqmap.App
}

func NewWebDataSource(app *liqmap.App) webdatasource.Services {
	return &webDataSourceModuleAdapter{app: app}
}

func (s *webDataSourceModuleAdapter) WebDataSourceStatus() liqmap.WebDataSourceStatus {
	return s.app.WebDataSourceStatus()
}

func (s *webDataSourceModuleAdapter) TriggerWebDataSourceInit() (bool, error) {
	return s.app.TriggerWebDataSourceInit()
}

func (s *webDataSourceModuleAdapter) WebDataSourceInitLoginTimeoutSec() int {
	return s.app.WebDataSourceInitLoginTimeoutSec()
}

func (s *webDataSourceModuleAdapter) TriggerWebDataSourceRun(windowDays *int) (bool, error) {
	return s.app.TriggerWebDataSourceRun(windowDays)
}

func (s *webDataSourceModuleAdapter) ListRecentWebDataSourceRuns(limit int) []liqmap.WebDataSourceRunRow {
	return s.app.ListRecentWebDataSourceRuns(limit)
}

func (s *webDataSourceModuleAdapter) UpdateWebDataSourceSettings(enabled *bool, intervalMin, timeoutSec int, chromePath, profileDir string) liqmap.WebDataSourceStatus {
	return s.app.UpdateWebDataSourceSettings(enabled, intervalMin, timeoutSec, chromePath, profileDir)
}

func (s *webDataSourceModuleAdapter) WebDataSourceMap(window string) liqmap.WebDataSourceMapResponse {
	return s.app.WebDataSourceMap(window)
}
