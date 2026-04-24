package liqmap

import (
	"context"
	"strconv"
	"strings"
)

type WebDataSourceModuleServices struct {
	app *App
}

func NewWebDataSourceModuleServices(app *App) *WebDataSourceModuleServices {
	return &WebDataSourceModuleServices{app: app}
}

func (s *WebDataSourceModuleServices) WebDataSourceStatus() WebDataSourceStatus {
	return s.app.webds.loadStatus()
}

func (s *WebDataSourceModuleServices) TriggerWebDataSourceInit() (bool, error) {
	return s.app.webds.triggerInit(context.Background())
}

func (s *WebDataSourceModuleServices) WebDataSourceInitLoginTimeoutSec() int {
	return defaultWebDataSourceInitLoginSec
}

func (s *WebDataSourceModuleServices) TriggerWebDataSourceRun(windowDays *int) (bool, error) {
	return s.app.webds.triggerRun(context.Background(), windowDays)
}

func (s *WebDataSourceModuleServices) ListRecentWebDataSourceRuns(limit int) []WebDataSourceRunRow {
	return s.app.webds.loadRecentRuns(limit)
}

func (s *WebDataSourceModuleServices) UpdateWebDataSourceSettings(enabled *bool, intervalMin, timeoutSec int, chromePath, profileDir string) WebDataSourceStatus {
	if enabled != nil {
		_ = s.app.webds.setSetting("enabled", strconv.FormatBool(*enabled))
	}
	if intervalMin > 0 {
		_ = s.app.webds.setSetting("interval_min", strconv.Itoa(intervalMin))
	}
	if timeoutSec > 0 {
		_ = s.app.webds.setSetting("timeout_sec", strconv.Itoa(timeoutSec))
	}
	_ = s.app.webds.setSetting("chrome_path", strings.TrimSpace(chromePath))
	if strings.TrimSpace(profileDir) != "" {
		_ = s.app.webds.setSetting("profile_dir", strings.TrimSpace(profileDir))
	}
	return s.app.webds.loadStatus()
}

func (s *WebDataSourceModuleServices) WebDataSourceMap(window string) WebDataSourceMapResponse {
	return s.app.webds.loadLatestMap(window)
}
