package liqmap

import (
	"context"
	"strconv"
	"strings"
)

func (a *App) WebDataSourceStatus() WebDataSourceStatus {
	return a.webds.loadStatus()
}

func (a *App) TriggerWebDataSourceInit() (bool, error) {
	return a.webds.triggerInit(context.Background())
}

func (a *App) WebDataSourceInitLoginTimeoutSec() int {
	return defaultWebDataSourceInitLoginSec
}

func (a *App) TriggerWebDataSourceRun(windowDays *int) (bool, error) {
	return a.webds.triggerRun(context.Background(), windowDays)
}

func (a *App) ListRecentWebDataSourceRuns(limit int) []WebDataSourceRunRow {
	return a.webds.loadRecentRuns(limit)
}

func (a *App) UpdateWebDataSourceSettings(enabled *bool, intervalMin, timeoutSec int, chromePath, profileDir string) WebDataSourceStatus {
	if enabled != nil {
		_ = a.webds.setSetting("enabled", strconv.FormatBool(*enabled))
	}
	if intervalMin > 0 {
		_ = a.webds.setSetting("interval_min", strconv.Itoa(intervalMin))
	}
	if timeoutSec > 0 {
		_ = a.webds.setSetting("timeout_sec", strconv.Itoa(timeoutSec))
	}
	_ = a.webds.setSetting("chrome_path", strings.TrimSpace(chromePath))
	if strings.TrimSpace(profileDir) != "" {
		_ = a.webds.setSetting("profile_dir", strings.TrimSpace(profileDir))
	}
	return a.webds.loadStatus()
}

func (a *App) WebDataSourceMap(window string) WebDataSourceMapResponse {
	return a.webds.loadLatestMap(window)
}
