package liqmap

import (
	"encoding/json"
	"log"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

func (m *WebDataSourceManager) cleanupStaleRunningRuns(reason string) {
	now := time.Now().UnixMilli()
	result, err := m.app.db.Exec(`UPDATE webdatasource_runs
		SET status='failed',
			error_message=?,
			finished_at=CASE WHEN finished_at IS NULL OR finished_at<=0 THEN ? ELSE finished_at END
		WHERE status='running' OR finished_at IS NULL OR finished_at<=0`, reason, now)
	if err == nil {
		if affected, _ := result.RowsAffected(); affected > 0 && m.app.debug {
			log.Printf("webdatasource cleanup stale runs: marked %d row(s) as failed", affected)
		}
	}
	if strings.EqualFold(strings.TrimSpace(m.getSetting("last_run_status")), "running") {
		_ = m.setSetting("last_run_finished_ts", strconv.FormatInt(now, 10))
		_ = m.setSetting("last_run_status", "failed")
		if strings.TrimSpace(m.getSetting("last_error")) == "" {
			_ = m.setSetting("last_error", reason)
		}
	}
}

func (m *WebDataSourceManager) getSetting(key string) string {
	var value string
	if err := m.app.db.QueryRow(`SELECT value FROM webdatasource_settings WHERE key=?`, key).Scan(&value); err != nil {
		return ""
	}
	return value
}

func (m *WebDataSourceManager) setSetting(key, value string) error {
	_, err := m.app.db.Exec(`INSERT INTO webdatasource_settings(key, value) VALUES(?, ?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, value)
	return err
}

func (m *WebDataSourceManager) getSettingInt(key string, fallback int) int {
	raw := strings.TrimSpace(m.getSetting(key))
	if raw == "" {
		return fallback
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		return fallback
	}
	return v
}

func (m *WebDataSourceManager) getSettingInt64(key string, fallback int64) int64 {
	raw := strings.TrimSpace(m.getSetting(key))
	if raw == "" {
		return fallback
	}
	v, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return fallback
	}
	return v
}

func (m *WebDataSourceManager) getSettingBool(key string, fallback bool) bool {
	raw := strings.TrimSpace(strings.ToLower(m.getSetting(key)))
	if raw == "" {
		return fallback
	}
	return raw == "1" || raw == "true" || raw == "yes" || raw == "on"
}

func (m *WebDataSourceManager) loadSettings() WebDataSourceSettings {
	wd, _ := os.Getwd()
	profileDir := filepath.Join(wd, "coinglass_profile")
	rawProfile := strings.TrimSpace(m.getSetting("profile_dir"))
	if rawProfile != "" {
		profileDir = rawProfile
	}
	intervalMin := m.getSettingInt("interval_min", defaultWebDataSourceIntervalMin)
	if intervalMin <= 0 {
		intervalMin = defaultWebDataSourceIntervalMin
	}
	timeoutSec := m.getSettingInt("timeout_sec", defaultWebDataSourceTimeoutSec)
	if timeoutSec <= 0 {
		timeoutSec = defaultWebDataSourceTimeoutSec
	}
	return WebDataSourceSettings{
		Enabled:            m.getSettingBool("enabled", true),
		IntervalMin:        intervalMin,
		TimeoutSec:         timeoutSec,
		ChromePath:         strings.TrimSpace(m.getSetting("chrome_path")),
		ProfileDir:         profileDir,
		LastError:          strings.TrimSpace(m.getSetting("last_error")),
		LastSuccessTS:      m.getSettingInt64("last_success_ts", 0),
		LastRunStartedTS:   m.getSettingInt64("last_run_started_ts", 0),
		LastRunFinishedTS:  m.getSettingInt64("last_run_finished_ts", 0),
		LastRunStatus:      strings.TrimSpace(m.getSetting("last_run_status")),
		LastRunRecordCount: m.getSettingInt("last_run_record_count", 0),
	}
}

func (m *WebDataSourceManager) loadRecentRuns(limit int) []WebDataSourceRunRow {
	rows, err := m.app.db.Query(`SELECT id, started_at, finished_at, status, window_days, error_message, records_count
		FROM webdatasource_runs ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := make([]WebDataSourceRunRow, 0, limit)
	for rows.Next() {
		var r WebDataSourceRunRow
		if err := rows.Scan(&r.ID, &r.StartedAt, &r.FinishedAt, &r.Status, &r.WindowDays, &r.ErrorMessage, &r.RecordsCount); err == nil {
			out = append(out, r)
		}
	}
	return out
}

func (m *WebDataSourceManager) loadStatus() WebDataSourceStatus {
	cfg := m.loadSettings()
	currentAction, stepLogs := m.snapshotStepLogs()
	m.mu.Lock()
	running := m.running
	m.mu.Unlock()
	runs := m.loadRecentRuns(12)
	var lastRun *WebDataSourceRunRow
	if len(runs) > 0 {
		lastRun = &runs[0]
	}
	return WebDataSourceStatus{
		Enabled:       cfg.Enabled,
		Running:       running,
		IntervalMin:   cfg.IntervalMin,
		TimeoutSec:    cfg.TimeoutSec,
		ChromePath:    cfg.ChromePath,
		ProfileDir:    cfg.ProfileDir,
		LastError:     cfg.LastError,
		LastSuccessTS: cfg.LastSuccessTS,
		NextRunTS:     m.nextAutoRunTS(cfg, time.Now()),
		CurrentAction: currentAction,
		StepLogs:      stepLogs,
		LastRun:       lastRun,
		RecentRuns:    runs,
	}
}

func (m *WebDataSourceManager) loadLatestMap(window string) WebDataSourceMapResponse {
	windowDays := map[string]int{"1d": 1, "7d": 7, "30d": 30}[window]
	if windowDays == 0 {
		windowDays = 30
		window = "30d"
	}
	var (
		snap       WebDataSourceSnapshotMeta
		payloadRaw string
	)
	err := m.app.db.QueryRow(`SELECT id, symbol, window_days, captured_at, range_low, range_high, payload_json
		FROM webdatasource_snapshots
		WHERE symbol='ETH' AND window_days=?
			AND EXISTS (SELECT 1 FROM webdatasource_points WHERE snapshot_id=webdatasource_snapshots.id LIMIT 1)
		ORDER BY captured_at DESC LIMIT 1`, windowDays).
		Scan(&snap.ID, &snap.Symbol, &snap.WindowDays, &snap.CapturedAt, &snap.RangeLow, &snap.RangeHigh, &payloadRaw)
	if err != nil {
		status := m.loadStatus()
		return WebDataSourceMapResponse{HasData: false, Window: window, LastError: status.LastError}
	}
	rows, err := m.app.db.Query(`SELECT exchange, side, price, liq_value FROM webdatasource_points WHERE snapshot_id=?`, snap.ID)
	if err != nil {
		return WebDataSourceMapResponse{HasData: false, Window: window, LastError: err.Error()}
	}
	defer rows.Close()
	points := make([]WebDataSourcePoint, 0, 512)
	longTotal, shortTotal := 0.0, 0.0
	topLongPrice, topLongValue := 0.0, 0.0
	topShortPrice, topShortValue := 0.0, 0.0
	topLongs := make([]WebDataSourceTopPoint, 0, 8)
	topShorts := make([]WebDataSourceTopPoint, 0, 8)
	exMap := map[string]float64{}
	groupMap := map[string]*WebDataSourceTopPoint{}
	for rows.Next() {
		var pt WebDataSourcePoint
		if err := rows.Scan(&pt.Exchange, &pt.Side, &pt.Price, &pt.LiqValue); err != nil {
			continue
		}
		points = append(points, pt)
		exMap[pt.Exchange] += pt.LiqValue
		side := strings.ToLower(strings.TrimSpace(pt.Side))
		if side != "short" {
			side = "long"
		}
		if side == "long" {
			longTotal += pt.LiqValue
		} else {
			shortTotal += pt.LiqValue
		}
		key := side + "|" + strconv.FormatFloat(pt.Price, 'f', 4, 64)
		group := groupMap[key]
		if group == nil {
			group = &WebDataSourceTopPoint{Side: side, Price: pt.Price}
			groupMap[key] = group
		}
		group.LiqValue += pt.LiqValue
	}
	for _, group := range groupMap {
		if group.Side == "long" {
			topLongs = append(topLongs, *group)
			if group.LiqValue > topLongValue {
				topLongValue, topLongPrice = group.LiqValue, group.Price
			}
		} else {
			topShorts = append(topShorts, *group)
			if group.LiqValue > topShortValue {
				topShortValue, topShortPrice = group.LiqValue, group.Price
			}
		}
	}
	sort.Slice(topLongs, func(i, j int) bool { return topLongs[i].LiqValue > topLongs[j].LiqValue })
	sort.Slice(topShorts, func(i, j int) bool { return topShorts[i].LiqValue > topShorts[j].LiqValue })
	if len(topLongs) > 3 {
		topLongs = topLongs[:3]
	}
	if len(topShorts) > 3 {
		topShorts = topShorts[:3]
	}
	total := longTotal + shortTotal
	contrib := make([]ExchangeContribution, 0, len(exMap))
	for ex, notional := range exMap {
		share := 0.0
		if total > 0 {
			share = notional / total
		}
		contrib = append(contrib, ExchangeContribution{Exchange: ex, NotionalUSD: notional, Share: share})
	}
	sort.Slice(contrib, func(i, j int) bool { return contrib[i].NotionalUSD > contrib[j].NotionalUSD })
	currentPrice := 0.0
	if strings.TrimSpace(payloadRaw) != "" {
		var payload map[string]any
		if err := json.Unmarshal([]byte(payloadRaw), &payload); err == nil {
			currentPrice = webDataSourcePayloadCurrentPrice(payload, snap.RangeLow, snap.RangeHigh)
		}
	}
	if currentPrice <= 0 && snap.RangeHigh > snap.RangeLow {
		currentPrice = math.Round(((snap.RangeHigh + snap.RangeLow) / 2 * 10)) / 10
	}
	return WebDataSourceMapResponse{
		HasData:       true,
		Window:        window,
		GeneratedAt:   snap.CapturedAt,
		CurrentPrice:  currentPrice,
		RangeLow:      snap.RangeLow,
		RangeHigh:     snap.RangeHigh,
		TopLongPrice:  topLongPrice,
		TopLongValue:  topLongValue,
		TopShortPrice: topShortPrice,
		TopShortValue: topShortValue,
		TopLongs:      topLongs,
		TopShorts:     topShorts,
		LongTotal:     longTotal,
		ShortTotal:    shortTotal,
		ByExchange:    contrib,
		Points:        points,
		Snapshot:      &snap,
	}
}
