package liqmap

import (
	"context"
	"strconv"
	"strings"
)

type WebDataSourceModuleAdapter struct {
	app *App
}

func NewWebDataSourceModuleAdapter(app *App) *WebDataSourceModuleAdapter {
	return &WebDataSourceModuleAdapter{app: app}
}

func (s *WebDataSourceModuleAdapter) WebDataSourceStatus() WebDataSourceStatus {
	return s.app.webds.loadStatus()
}

func (s *WebDataSourceModuleAdapter) TriggerWebDataSourceInit() (bool, error) {
	return s.app.webds.triggerInit(context.Background())
}

func (s *WebDataSourceModuleAdapter) WebDataSourceInitLoginTimeoutSec() int {
	return defaultWebDataSourceInitLoginSec
}

func (s *WebDataSourceModuleAdapter) TriggerWebDataSourceRun(windowDays *int) (bool, error) {
	return s.app.webds.triggerRun(context.Background(), windowDays)
}

func (s *WebDataSourceModuleAdapter) ListRecentWebDataSourceRuns(limit int) []WebDataSourceRunRow {
	return s.app.webds.loadRecentRuns(limit)
}

func (s *WebDataSourceModuleAdapter) UpdateWebDataSourceSettings(enabled *bool, intervalMin, timeoutSec int, chromePath, profileDir string) WebDataSourceStatus {
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

func (s *WebDataSourceModuleAdapter) WebDataSourceMap(window string) WebDataSourceMapResponse {
	return s.app.webds.loadLatestMap(window)
}
