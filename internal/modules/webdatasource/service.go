package webdatasource

import liqmap "multipleexchangeliquidationmap"

type webDataSourceCore interface {
	WebDataSourceStatus() liqmap.WebDataSourceStatus
	TriggerWebDataSourceInit() (bool, error)
	WebDataSourceInitLoginTimeoutSec() int
	TriggerWebDataSourceRun(windowDays *int) (bool, error)
	ListRecentWebDataSourceRuns(limit int) []liqmap.WebDataSourceRunRow
	UpdateWebDataSourceSettings(enabled *bool, intervalMin, timeoutSec int, chromePath, profileDir string) liqmap.WebDataSourceStatus
	WebDataSourceMap(window string) liqmap.WebDataSourceMapResponse
}

type service struct {
	core webDataSourceCore
}

func newService(core webDataSourceCore) *service {
	return &service{core: core}
}
