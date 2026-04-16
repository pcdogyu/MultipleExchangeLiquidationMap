package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"log"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/chromedp"
)

const defaultWebDataSourceIntervalMin = 15

type WebDataSourceManager struct {
	app *App
	mu  sync.Mutex

	running bool
}

type WebDataSourceSettings struct {
	Enabled            bool   `json:"enabled"`
	IntervalMin        int    `json:"interval_min"`
	ChromePath         string `json:"chrome_path"`
	ProfileDir         string `json:"profile_dir"`
	LastError          string `json:"last_error,omitempty"`
	LastSuccessTS      int64  `json:"last_success_ts,omitempty"`
	LastRunStartedTS   int64  `json:"last_run_started_ts,omitempty"`
	LastRunFinishedTS  int64  `json:"last_run_finished_ts,omitempty"`
	LastRunStatus      string `json:"last_run_status,omitempty"`
	LastRunRecordCount int    `json:"last_run_record_count,omitempty"`
}

type WebDataSourceRunRow struct {
	ID           int64  `json:"id"`
	StartedAt    int64  `json:"started_at"`
	FinishedAt   int64  `json:"finished_at"`
	Status       string `json:"status"`
	WindowDays   int    `json:"window_days"`
	ErrorMessage string `json:"error_message,omitempty"`
	RecordsCount int    `json:"records_count"`
}

type WebDataSourceStatus struct {
	Enabled       bool                  `json:"enabled"`
	Running       bool                  `json:"running"`
	IntervalMin   int                   `json:"interval_min"`
	ChromePath    string                `json:"chrome_path"`
	ProfileDir    string                `json:"profile_dir"`
	LastError     string                `json:"last_error,omitempty"`
	LastSuccessTS int64                 `json:"last_success_ts,omitempty"`
	LastRun       *WebDataSourceRunRow  `json:"last_run,omitempty"`
	RecentRuns    []WebDataSourceRunRow `json:"recent_runs,omitempty"`
}

type WebDataSourcePoint struct {
	Exchange string  `json:"exchange"`
	Side     string  `json:"side"`
	Price    float64 `json:"price"`
	LiqValue float64 `json:"liq_value"`
}

type WebDataSourceTopPoint struct {
	Side     string  `json:"side"`
	Price    float64 `json:"price"`
	LiqValue float64 `json:"liq_value"`
}

type WebDataSourceSnapshotMeta struct {
	ID         int64   `json:"id"`
	Symbol     string  `json:"symbol"`
	WindowDays int     `json:"window_days"`
	CapturedAt int64   `json:"captured_at"`
	RangeLow   float64 `json:"range_low"`
	RangeHigh  float64 `json:"range_high"`
}

type WebDataSourceMapResponse struct {
	HasData       bool                       `json:"has_data"`
	Window        string                     `json:"window"`
	GeneratedAt   int64                      `json:"generated_at"`
	CurrentPrice  float64                    `json:"current_price"`
	RangeLow      float64                    `json:"range_low"`
	RangeHigh     float64                    `json:"range_high"`
	TopLongPrice  float64                    `json:"top_long_price"`
	TopLongValue  float64                    `json:"top_long_value"`
	TopShortPrice float64                    `json:"top_short_price"`
	TopShortValue float64                    `json:"top_short_value"`
	TopLongs      []WebDataSourceTopPoint    `json:"top_longs"`
	TopShorts     []WebDataSourceTopPoint    `json:"top_shorts"`
	LongTotal     float64                    `json:"long_total"`
	ShortTotal    float64                    `json:"short_total"`
	ByExchange    []ExchangeContribution     `json:"by_exchange"`
	Points        []WebDataSourcePoint       `json:"points"`
	LastError     string                     `json:"last_error,omitempty"`
	Snapshot      *WebDataSourceSnapshotMeta `json:"snapshot,omitempty"`
}

type capturedPayloadMeta struct {
	RangeLow  float64 `json:"range_low"`
	RangeHigh float64 `json:"range_high"`
	HookHits  int     `json:"hook_hits"`
}

func newWebDataSourceManager(app *App) *WebDataSourceManager {
	return &WebDataSourceManager{app: app}
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
	return WebDataSourceSettings{
		Enabled:            m.getSettingBool("enabled", true),
		IntervalMin:        intervalMin,
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

func (m *WebDataSourceManager) start(ctx context.Context) {
	go func() {
		timer := time.NewTimer(2 * time.Second)
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
				cfg := m.loadSettings()
				if cfg.Enabled {
					_, _ = m.triggerRun(ctx, nil)
				}
				next := time.Duration(cfg.IntervalMin) * time.Minute
				if next <= 0 {
					next = defaultWebDataSourceIntervalMin * time.Minute
				}
				timer.Reset(next)
			}
		}
	}()
}

func (m *WebDataSourceManager) triggerRun(parent context.Context, windowDays *int) (bool, error) {
	m.mu.Lock()
	if m.running {
		m.mu.Unlock()
		return false, errors.New("webdatasource run already in progress")
	}
	m.running = true
	m.mu.Unlock()

	go func() {
		defer func() {
			m.mu.Lock()
			m.running = false
			m.mu.Unlock()
		}()
		ctx, cancel := context.WithTimeout(parent, 4*time.Minute)
		defer cancel()
		if err := m.runOnce(ctx, windowDays); err != nil {
			log.Printf("webdatasource run failed: %v", err)
		}
	}()
	return true, nil
}

func (m *WebDataSourceManager) finishRunState(status, errMsg string, records int) {
	now := time.Now().UnixMilli()
	_ = m.setSetting("last_run_finished_ts", strconv.FormatInt(now, 10))
	_ = m.setSetting("last_run_status", status)
	_ = m.setSetting("last_run_record_count", strconv.Itoa(records))
	if errMsg != "" {
		_ = m.setSetting("last_error", errMsg)
		return
	}
	_ = m.setSetting("last_error", "")
	_ = m.setSetting("last_success_ts", strconv.FormatInt(now, 10))
}

func (m *WebDataSourceManager) insertRun(windowDays int, status, errMsg string, records int) (int64, error) {
	now := time.Now().UnixMilli()
	res, err := m.app.db.Exec(`INSERT INTO webdatasource_runs(started_at, finished_at, status, window_days, error_message, records_count, source_meta_json) VALUES(?, ?, ?, ?, ?, ?, '')`,
		now, 0, status, windowDays, errMsg, records)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (m *WebDataSourceManager) updateRun(id int64, status, errMsg string, records int) error {
	now := time.Now().UnixMilli()
	_, err := m.app.db.Exec(`UPDATE webdatasource_runs SET finished_at=?, status=?, error_message=?, records_count=? WHERE id=?`,
		now, status, errMsg, records, id)
	return err
}

func (m *WebDataSourceManager) insertSnapshot(windowDays int, rangeLow, rangeHigh float64, payload map[string]any) (int64, error) {
	now := time.Now().UnixMilli()
	raw, _ := json.Marshal(payload)
	res, err := m.app.db.Exec(`INSERT INTO webdatasource_snapshots(symbol, window_days, captured_at, range_low, range_high, payload_json) VALUES(?, ?, ?, ?, ?, ?)`,
		"ETH", windowDays, now, rangeLow, rangeHigh, string(raw))
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (m *WebDataSourceManager) insertPoints(snapshotID int64, windowDays int, points []WebDataSourcePoint) error {
	tx, err := m.app.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmt, err := tx.Prepare(`INSERT INTO webdatasource_points(snapshot_id, symbol, window_days, side, exchange, price, liq_value, captured_at) VALUES(?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	now := time.Now().UnixMilli()
	for _, pt := range points {
		if _, err := stmt.Exec(snapshotID, "ETH", windowDays, pt.Side, pt.Exchange, pt.Price, pt.LiqValue, now); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (m *WebDataSourceManager) runOnce(ctx context.Context, windowDays *int) error {
	started := time.Now().UnixMilli()
	_ = m.setSetting("last_run_started_ts", strconv.FormatInt(started, 10))
	_ = m.setSetting("last_run_status", "running")
	_ = m.setSetting("last_error", "")

	windows := []int{1, 7, 30}
	if windowDays != nil && (*windowDays == 1 || *windowDays == 7 || *windowDays == 30) {
		windows = []int{*windowDays}
	}
	cfg := m.loadSettings()
	chromePath := strings.TrimSpace(cfg.ChromePath)
	if chromePath == "" {
		chromePath = detectChromePath()
	}
	if chromePath == "" {
		err := errors.New("chrome/chromium executable not found")
		m.finishRunState("failed", err.Error(), 0)
		return err
	}

	totalRecords := 0
	for _, days := range windows {
		runID, err := m.insertRun(days, "running", "", 0)
		if err != nil {
			continue
		}
		payload, meta, err := m.captureWindow(ctx, chromePath, cfg.ProfileDir, days)
		if err != nil {
			_ = m.updateRun(runID, "failed", err.Error(), 0)
			m.finishRunState("failed", err.Error(), 0)
			return err
		}
		points, rangeLow, rangeHigh := normalizeWebDataSourcePayload(payload)
		if rangeLow == 0 && meta.RangeLow != 0 {
			rangeLow = meta.RangeLow
		}
		if rangeHigh == 0 && meta.RangeHigh != 0 {
			rangeHigh = meta.RangeHigh
		}
		snapshotID, err := m.insertSnapshot(days, rangeLow, rangeHigh, payload)
		if err != nil {
			_ = m.updateRun(runID, "failed", err.Error(), 0)
			m.finishRunState("failed", err.Error(), 0)
			return err
		}
		if err := m.insertPoints(snapshotID, days, points); err != nil {
			_ = m.updateRun(runID, "failed", err.Error(), 0)
			m.finishRunState("failed", err.Error(), 0)
			return err
		}
		totalRecords += len(points)
		metaJSON, _ := json.Marshal(meta)
		_, _ = m.app.db.Exec(`UPDATE webdatasource_runs SET source_meta_json=? WHERE id=?`, string(metaJSON), runID)
		_ = m.updateRun(runID, "success", "", len(points))
	}
	m.finishRunState("success", "", totalRecords)
	return nil
}

func detectChromePath() string {
	candidates := []string{
		os.Getenv("WEBDATASOURCE_CHROME_PATH"),
		`C:\Program Files\Google\Chrome\Application\chrome.exe`,
		`C:\Program Files (x86)\Google\Chrome\Application\chrome.exe`,
		`C:\Program Files\Chromium\Application\chrome.exe`,
		`C:\Program Files\Microsoft\Edge\Application\msedge.exe`,
		"/usr/bin/google-chrome",
		"/usr/bin/google-chrome-stable",
		"/usr/bin/chromium",
		"/usr/bin/chromium-browser",
	}
	for _, p := range candidates {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

func (m *WebDataSourceManager) captureWindow(ctx context.Context, chromePath, profileDir string, days int) (map[string]any, capturedPayloadMeta, error) {
	label := map[int]string{1: "1天", 7: "7天", 30: "30天"}[days]
	if label == "" {
		label = fmt.Sprintf("%d天", days)
	}
	if err := os.MkdirAll(profileDir, 0o755); err != nil {
		return nil, capturedPayloadMeta{}, err
	}
	opts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.ExecPath(chromePath),
		chromedp.UserAgent("Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36"),
		chromedp.UserDataDir(profileDir),
		chromedp.Flag("disable-blink-features", "AutomationControlled"),
		chromedp.Flag("headless", true),
		chromedp.WindowSize(1440, 900),
	)
	allocCtx, cancelAlloc := chromedp.NewExecAllocator(ctx, opts...)
	defer cancelAlloc()
	taskCtx, cancelTask := chromedp.NewContext(allocCtx)
	defer cancelTask()

	var ok bool
	if err := chromedp.Run(taskCtx,
		chromedp.Navigate("https://www.coinglass.com/zh/pro/futures/LiquidationMap"),
		chromedp.Sleep(8*time.Second),
		chromedp.Evaluate(webDataSourceHookJS, nil),
		chromedp.Evaluate(`(() => {
			const headings = Array.from(document.querySelectorAll('h1'));
			let target = null;
			for (const h of headings) {
				const t = (h.textContent || '').trim();
				if (t.includes('交易所清算地图') && !t.includes('Hyperliquid')) { target = h; break; }
			}
			if (!target) return false;
			let container = target;
			for (let i = 0; i < 12; i++) {
				container = container.parentElement;
				if (!container) break;
				const inputs = container.querySelectorAll('.MuiAutocomplete-input');
				const btns = container.querySelectorAll('.MuiSelect-button');
				if (inputs.length > 0 && btns.length > 0) {
					window._cgSection = {
						inputIdx: Array.from(document.querySelectorAll('.MuiAutocomplete-input')).indexOf(inputs[0]),
						btnIdx: Array.from(document.querySelectorAll('.MuiSelect-button')).indexOf(btns[btns.length - 1])
					};
					return true;
				}
			}
			return false;
		})()`, &ok),
	); err != nil {
		return nil, capturedPayloadMeta{}, err
	}
	if !ok {
		return nil, capturedPayloadMeta{}, errors.New("coinglass section not found")
	}

	var inputIdx, btnIdx int
	if err := chromedp.Run(taskCtx, chromedp.Evaluate(`window._cgSection.inputIdx`, &inputIdx), chromedp.Evaluate(`window._cgSection.btnIdx`, &btnIdx)); err != nil {
		return nil, capturedPayloadMeta{}, err
	}

	js := fmt.Sprintf(`(() => {
		const inputs = document.querySelectorAll('.MuiAutocomplete-input');
		const inp = inputs[%d];
		if (!inp) return false;
		inp.focus();
		inp.value = '';
		inp.dispatchEvent(new Event('input', {bubbles:true}));
		return true;
	})()`, inputIdx)
	if err := chromedp.Run(taskCtx, chromedp.Evaluate(js, &ok)); err != nil || !ok {
		return nil, capturedPayloadMeta{}, errors.New("coinglass symbol input not found")
	}
	if err := chromedp.Run(taskCtx, chromedp.SendKeys(".MuiAutocomplete-input", "ETH"), chromedp.Sleep(1500*time.Millisecond)); err != nil {
		return nil, capturedPayloadMeta{}, err
	}
	if err := chromedp.Run(taskCtx, chromedp.Evaluate(`(() => {
		const all = Array.from(document.querySelectorAll('[role="option"], .MuiOption-root, li[class*="Option"], li[class*="option"]'));
		const exact = all.find(el => el.textContent.trim() === 'ETH' && el.offsetParent !== null);
		if (exact) { exact.click(); return true; }
		return false;
	})()`, &ok)); err != nil {
		return nil, capturedPayloadMeta{}, err
	}

	js = fmt.Sprintf(`(() => {
		const btn = document.querySelectorAll('.MuiSelect-button')[%d];
		if (!btn) return false;
		btn.click();
		return true;
	})()`, btnIdx)
	if err := chromedp.Run(taskCtx, chromedp.Evaluate(js, &ok), chromedp.Sleep(1200*time.Millisecond)); err != nil || !ok {
		return nil, capturedPayloadMeta{}, errors.New("coinglass window selector not found")
	}

	js = fmt.Sprintf(`(() => {
		const all = Array.from(document.querySelectorAll('[role="option"], .MuiOption-root, li[class*="Option"], li[class*="option"]'));
		const exact = all.find(el => el.textContent.trim() === %q && el.offsetParent !== null);
		if (exact) { exact.click(); return true; }
		const fuzzy = all.find(el => (el.textContent || '').includes(%q) && el.offsetParent !== null);
		if (fuzzy) { fuzzy.click(); return true; }
		return false;
	})()`, label, label)
	if err := chromedp.Run(taskCtx, chromedp.Evaluate(js, &ok), chromedp.Sleep(2*time.Second)); err != nil || !ok {
		return nil, capturedPayloadMeta{}, errors.New("coinglass target window option not found")
	}

	if err := chromedp.Run(taskCtx, chromedp.Evaluate(`(() => {
		const btns = Array.from(document.querySelectorAll('button')).filter(b => b.querySelector('svg') && b.textContent.trim() === '' && b.offsetParent !== null);
		if (btns.length) { btns[btns.length - 1].click(); return true; }
		return false;
	})()`, &ok)); err != nil || !ok {
		return nil, capturedPayloadMeta{}, errors.New("coinglass refresh button not found")
	}

	deadline := time.Now().Add(40 * time.Second)
	for time.Now().Before(deadline) {
		var data map[string]any
		if err := chromedp.Run(taskCtx, chromedp.Evaluate(`window._liqData`, &data)); err == nil && len(data) > 0 {
			meta := capturedPayloadMeta{
				RangeLow:  toFloatFromAny(data["rangeLow"]),
				RangeHigh: toFloatFromAny(data["rangeHigh"]),
			}
			var logs []any
			_ = chromedp.Run(taskCtx, chromedp.Evaluate(`window._liqLog || []`, &logs))
			meta.HookHits = len(logs)
			return data, meta, nil
		}
		select {
		case <-ctx.Done():
			return nil, capturedPayloadMeta{}, ctx.Err()
		case <-time.After(1200 * time.Millisecond):
		}
	}
	return nil, capturedPayloadMeta{}, errors.New("timed out waiting for coinglass liqMapV2 payload")
}

func toFloatFromAny(v any) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case float32:
		return float64(x)
	case int:
		return float64(x)
	case int64:
		return float64(x)
	case json.Number:
		f, _ := x.Float64()
		return f
	case string:
		f, _ := strconv.ParseFloat(strings.TrimSpace(x), 64)
		return f
	default:
		return 0
	}
}

func firstPositive(m map[string]any, keys ...string) float64 {
	for _, key := range keys {
		if v := toFloatFromAny(m[key]); v > 0 {
			return v
		}
	}
	return 0
}

func parsePointArray(node any, exchange, side string) []WebDataSourcePoint {
	raw, ok := node.([]any)
	if !ok {
		return nil
	}
	out := make([]WebDataSourcePoint, 0, len(raw))
	for _, item := range raw {
		switch vv := item.(type) {
		case []any:
			if len(vv) < 2 {
				continue
			}
			price := toFloatFromAny(vv[0])
			value := toFloatFromAny(vv[1])
			if price > 0 && value > 0 {
				out = append(out, WebDataSourcePoint{Exchange: strings.ToUpper(exchange), Side: side, Price: price, LiqValue: value})
			}
		case map[string]any:
			price := firstPositive(vv, "price", "p", "x")
			value := firstPositive(vv, "value", "liqValue", "amount", "y", "notional")
			ex := exchange
			if s := strings.TrimSpace(fmt.Sprint(vv["exchange"])); s != "" && s != "<nil>" {
				ex = s
			}
			if price > 0 && value > 0 {
				out = append(out, WebDataSourcePoint{Exchange: strings.ToUpper(ex), Side: side, Price: price, LiqValue: value})
			}
		}
	}
	return out
}

func extractFallbackPoints(payload map[string]any) []WebDataSourcePoint {
	out := make([]WebDataSourcePoint, 0, 256)
	var walk func(any)
	walk = func(node any) {
		switch v := node.(type) {
		case map[string]any:
			price := firstPositive(v, "price", "p", "x")
			value := firstPositive(v, "value", "liqValue", "amount", "y", "notional")
			side := strings.ToLower(strings.TrimSpace(fmt.Sprint(v["side"])))
			ex := strings.ToUpper(strings.TrimSpace(fmt.Sprint(v["exchange"])))
			if price > 0 && value > 0 && (side == "long" || side == "short") {
				out = append(out, WebDataSourcePoint{Exchange: ex, Side: side, Price: price, LiqValue: value})
			}
			for _, child := range v {
				walk(child)
			}
		case []any:
			for _, item := range v {
				walk(item)
			}
		}
	}
	walk(payload)
	return out
}

func dedupePoints(points []WebDataSourcePoint) []WebDataSourcePoint {
	out := make([]WebDataSourcePoint, 0, len(points))
	seen := map[string]int{}
	for _, pt := range points {
		key := strings.ToUpper(pt.Exchange) + "|" + pt.Side + "|" + strconv.FormatFloat(math.Round(pt.Price*100)/100, 'f', 2, 64)
		if idx, ok := seen[key]; ok {
			out[idx].LiqValue += pt.LiqValue
			continue
		}
		seen[key] = len(out)
		out = append(out, pt)
	}
	return out
}

func normalizeWebDataSourcePayload(payload map[string]any) ([]WebDataSourcePoint, float64, float64) {
	points := make([]WebDataSourcePoint, 0, 512)
	rangeLow := toFloatFromAny(payload["rangeLow"])
	rangeHigh := toFloatFromAny(payload["rangeHigh"])
	var walk func(any, string)
	walk = func(node any, exchange string) {
		switch v := node.(type) {
		case map[string]any:
			ex := exchange
			for _, key := range []string{"exchange", "ex", "name"} {
				if s := strings.TrimSpace(fmt.Sprint(v[key])); s != "" && s != "<nil>" {
					ex = s
					break
				}
			}
			for _, pair := range []struct {
				Key  string
				Side string
			}{
				{"long", "long"}, {"short", "short"}, {"longs", "long"}, {"shorts", "short"},
				{"longData", "long"}, {"shortData", "short"}, {"longLiquidationData", "long"}, {"shortLiquidationData", "short"},
			} {
				if child, ok := v[pair.Key]; ok {
					points = append(points, parsePointArray(child, ex, pair.Side)...)
				}
			}
			for _, child := range v {
				walk(child, ex)
			}
		case []any:
			for _, item := range v {
				walk(item, exchange)
			}
		}
	}
	walk(payload, "")
	if len(points) == 0 {
		points = extractFallbackPoints(payload)
	}
	sort.Slice(points, func(i, j int) bool {
		if points[i].Price == points[j].Price {
			return points[i].LiqValue > points[j].LiqValue
		}
		return points[i].Price < points[j].Price
	})
	return dedupePoints(points), rangeLow, rangeHigh
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
		ChromePath:    cfg.ChromePath,
		ProfileDir:    cfg.ProfileDir,
		LastError:     cfg.LastError,
		LastSuccessTS: cfg.LastSuccessTS,
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
	var snap WebDataSourceSnapshotMeta
	err := m.app.db.QueryRow(`SELECT id, symbol, window_days, captured_at, range_low, range_high
		FROM webdatasource_snapshots WHERE symbol='ETH' AND window_days=? ORDER BY captured_at DESC LIMIT 1`, windowDays).
		Scan(&snap.ID, &snap.Symbol, &snap.WindowDays, &snap.CapturedAt, &snap.RangeLow, &snap.RangeHigh)
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
	for rows.Next() {
		var pt WebDataSourcePoint
		if err := rows.Scan(&pt.Exchange, &pt.Side, &pt.Price, &pt.LiqValue); err != nil {
			continue
		}
		points = append(points, pt)
		exMap[pt.Exchange] += pt.LiqValue
		if pt.Side == "long" {
			longTotal += pt.LiqValue
			topLongs = append(topLongs, WebDataSourceTopPoint{Side: "long", Price: pt.Price, LiqValue: pt.LiqValue})
			if pt.LiqValue > topLongValue {
				topLongValue, topLongPrice = pt.LiqValue, pt.Price
			}
		} else {
			shortTotal += pt.LiqValue
			topShorts = append(topShorts, WebDataSourceTopPoint{Side: "short", Price: pt.Price, LiqValue: pt.LiqValue})
			if pt.LiqValue > topShortValue {
				topShortValue, topShortPrice = pt.LiqValue, pt.Price
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
	if snap.RangeHigh > snap.RangeLow {
		currentPrice = (snap.RangeHigh + snap.RangeLow) / 2
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

func (a *App) handleWebDataSource(w http.ResponseWriter, r *http.Request) {
	if a.debug {
		log.Printf("%s %s", r.Method, r.URL.Path)
	}
	tpl := template.Must(template.New("webdatasource").Parse(webDataSourceHTML))
	_ = tpl.Execute(w, nil)
}

func (a *App) handleWebDataSourceStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	_ = json.NewEncoder(w).Encode(a.webds.loadStatus())
}

func (a *App) handleWebDataSourceRun(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		WindowDays int `json:"window_days"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	var days *int
	if req.WindowDays == 1 || req.WindowDays == 7 || req.WindowDays == 30 {
		days = &req.WindowDays
	}
	started, err := a.webds.triggerRun(r.Context(), days)
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"started": started})
}

func (a *App) handleWebDataSourceRuns(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"rows": a.webds.loadRecentRuns(20)})
}

func (a *App) handleWebDataSourceSettings(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Enabled     *bool  `json:"enabled"`
		IntervalMin int    `json:"interval_min"`
		ChromePath  string `json:"chrome_path"`
		ProfileDir  string `json:"profile_dir"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if req.Enabled != nil {
		_ = a.webds.setSetting("enabled", strconv.FormatBool(*req.Enabled))
	}
	if req.IntervalMin > 0 {
		_ = a.webds.setSetting("interval_min", strconv.Itoa(req.IntervalMin))
	}
	_ = a.webds.setSetting("chrome_path", strings.TrimSpace(req.ChromePath))
	if strings.TrimSpace(req.ProfileDir) != "" {
		_ = a.webds.setSetting("profile_dir", strings.TrimSpace(req.ProfileDir))
	}
	_ = json.NewEncoder(w).Encode(a.webds.loadStatus())
}

func (a *App) handleWebDataSourceMap(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	window := strings.TrimSpace(r.URL.Query().Get("window"))
	if window == "" {
		window = "30d"
	}
	_ = json.NewEncoder(w).Encode(a.webds.loadLatestMap(window))
}

const webDataSourceHookJS = `(() => {
	window._liqData = null;
	window._liqLog = [];
	const _orig = JSON.parse;
	JSON.parse = function(str) {
		let res;
		try { res = _orig.call(this, str); } catch(e) { throw e; }
		try {
			if (str && str.includes('liqMapV2')) {
				window._liqLog.push('hit liqMapV2, len=' + str.length);
				window._liqData = res;
			}
		} catch(e) {}
		return res;
	};
	return 'hook_injected';
})()`

const webDataSourceHTML = `<!doctype html>
<html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>页面数据源</title>
<style>:root{--bg:#f5f7fb;--text:#1f2937;--muted:#64748b;--nav-bg:#0b1220;--nav-border:#243145;--nav-text:#eef3f9;--link:#d6deea;--panel-bg:#fff;--panel-border:#dce3ec;--ctl-bg:#fff;--ctl-text:#111827;--ctl-border:#cbd5e1;--chart-border:#e5e7eb}[data-theme="dark"]{--bg:#000;--text:#e5e7eb;--muted:#94a3b8;--nav-bg:#000;--nav-border:#111827;--nav-text:#eef3f9;--link:#d6deea;--panel-bg:#000;--panel-border:#1f2937;--ctl-bg:#000;--ctl-text:#e5e7eb;--ctl-border:#334155;--chart-border:#1f2937}body{margin:0;background:var(--bg);color:var(--text);font-family:Inter,system-ui,Segoe UI,Arial,sans-serif}.nav{height:56px;background:var(--nav-bg);border-bottom:1px solid var(--nav-border);display:flex;align-items:center;justify-content:space-between;padding:0 20px;position:sticky;top:0;z-index:10}.nav-left,.nav-right{display:flex;align-items:center;gap:14px}.brand{font-size:18px;font-weight:700;color:var(--nav-text)}.menu a{color:var(--link);text-decoration:none;font-size:16px;margin-right:18px}.menu a.active{color:#fff;font-weight:700}.theme-toggle{display:inline-flex;align-items:center;gap:6px;font-size:13px}.theme-toggle button{height:30px;padding:0 10px;border-radius:999px;border:1px solid rgba(148,163,184,0.45);background:transparent;color:var(--nav-text);cursor:pointer}.theme-toggle button.label{cursor:default;opacity:.92}.theme-toggle button.active{background:rgba(255,255,255,0.12);border-color:rgba(255,255,255,0.18);color:#fff}.wrap{max-width:1260px;margin:0 auto;padding:18px}.grid{display:grid;grid-template-columns:360px 1fr;gap:16px}.panel{border:1px solid var(--panel-border);background:var(--panel-bg);padding:16px;border-radius:12px}.small{font-size:12px;color:var(--muted)}.field label{display:block;font-size:12px;color:var(--muted);margin-bottom:6px}.field input,.field select,button{height:36px;border:1px solid var(--ctl-border);border-radius:8px;background:var(--ctl-bg);color:var(--ctl-text);padding:0 10px}.field input{width:100%;box-sizing:border-box}button{cursor:pointer}.primary{background:#0f172a;color:#eef3f9;border-color:#0f172a}.row{display:flex;gap:8px;align-items:center;flex-wrap:wrap}.cards{display:grid;grid-template-columns:repeat(4,minmax(0,1fr));gap:10px;margin-bottom:14px}.card{border:1px solid var(--panel-border);border-radius:10px;padding:12px;background:var(--panel-bg)}.card .k{font-size:12px;color:var(--muted)}.card .v{margin-top:6px;font-size:22px;font-weight:700}.chart{width:100%;height:520px;border:1px solid var(--chart-border);border-radius:10px;display:block;background:var(--panel-bg)}.runs{max-height:260px;overflow:auto}.runs table{width:100%;border-collapse:collapse}.runs th,.runs td{padding:6px 8px;border-bottom:1px solid var(--panel-border);font-size:12px;text-align:left}.bar{height:10px;background:rgba(148,163,184,.18);border-radius:999px;overflow:hidden}.bar>span{display:block;height:100%;background:#2563eb}.tag{display:inline-block;padding:2px 8px;border-radius:999px;background:rgba(37,99,235,.12);color:#2563eb;font-size:12px}.footer{margin:18px auto 0 auto;max-width:1200px;padding:10px 12px;font-size:12px;color:var(--muted);text-align:center}@media (max-width:1100px){.grid{grid-template-columns:1fr}.cards{grid-template-columns:repeat(2,minmax(0,1fr))}}</style></head>
<body><div class="nav"><div class="nav-left"><div class="brand">ETH Liquidation Map</div><div class="menu"><a href="/">清算热区</a><a href="/config">模型配置</a><a href="/monitor">雷区监控</a><a href="/map">盘口汇总</a><a href="/liquidations">强平清算</a><a href="/bubbles">气泡图</a><a href="/webdatasource" class="active">页面数据源</a><a href="/channel">消息通道</a></div></div><div class="nav-right"><div class="theme-toggle"><button class="label" type="button">主题</button><button id="themeDark" onclick="setTheme('dark')">深色</button><button id="themeLight" onclick="setTheme('light')">浅色</button></div></div></div>
<div class="wrap"><div class="grid"><div class="panel"><h2 style="margin:0 0 8px 0">抓取任务</h2><div id="statusBox" class="small">加载中...</div><div class="field" style="margin-top:12px"><label>抓取间隔（分钟）</label><input id="intervalMin" type="number" min="1" step="1"></div><div class="field" style="margin-top:12px"><label>Chrome 路径</label><input id="chromePath" placeholder="留空自动探测"></div><div class="field" style="margin-top:12px"><label>Profile 目录</label><input id="profileDir"></div><div class="row" style="margin-top:14px"><button class="primary" onclick="saveSettings()">保存设置</button><button onclick="runNow()">立即抓取</button><select id="runWindow"><option value="">抓取全部窗口</option><option value="1">仅 1 天</option><option value="7">仅 7 天</option><option value="30">仅 30 天</option></select></div><div class="small" id="saveMsg" style="margin-top:8px"></div><div style="margin-top:16px"><div class="row" style="justify-content:space-between"><h3 style="margin:0">最近运行</h3><span class="tag" id="runState">-</span></div><div class="runs" style="margin-top:8px"><table><thead><tr><th>ID</th><th>窗口</th><th>状态</th><th>记录数</th><th>开始</th></tr></thead><tbody id="runsBody"></tbody></table></div></div></div><div><div class="cards"><div class="card"><div class="k">当前窗口</div><div class="v" id="cardWindow">-</div></div><div class="card"><div class="k">多单总强度</div><div class="v" id="cardLong">-</div></div><div class="card"><div class="k">空单总强度</div><div class="v" id="cardShort">-</div></div><div class="card"><div class="k">最新抓取</div><div class="v" id="cardTime" style="font-size:16px">-</div></div></div><div class="panel"><div class="row" style="justify-content:space-between;margin-bottom:10px"><div><strong>ETH 页面数据源清算地图</strong><div class="small" id="chartMeta">加载中...</div></div><div class="row"><select id="windowSel" onchange="loadMap()"><option value="30d">30D</option><option value="7d">7D</option><option value="1d">1D</option></select></div></div><canvas id="cv" class="chart" width="1000" height="520"></canvas><div style="margin-top:12px"><div class="small" style="margin-bottom:6px">交易所贡献</div><div id="exchangeBars"></div></div></div></div></div></div><div id="globalFooter" class="footer">Code by Yuhao@jiansutech.com - loading - loading - loading</div>
<script>
function setTheme(t){const theme=(t==='dark')?'dark':'light';document.documentElement.setAttribute('data-theme',theme);try{localStorage.setItem('theme',theme);}catch(_){}const bd=document.getElementById('themeDark'),bl=document.getElementById('themeLight');if(bd)bd.classList.toggle('active',theme==='dark');if(bl)bl.classList.toggle('active',theme==='light');}
function initTheme(){let t='light';try{t=localStorage.getItem('theme')||'light';}catch(_){}setTheme(t);}
function fmtAmt(n){n=Number(n||0);if(!isFinite(n))return '-';if(n>=1e9)return (n/1e9).toFixed(2)+'B';if(n>=1e6)return (n/1e6).toFixed(2)+'M';if(n>=1e3)return (n/1e3).toFixed(2)+'K';return n.toFixed(2);}
function fmtYi(n){n=Number(n||0);if(!isFinite(n))return '-';return (n/1e8).toFixed(2)+'亿';}
function fmtPrice(n){n=Number(n||0);if(!isFinite(n))return '-';return n.toLocaleString('zh-CN',{minimumFractionDigits:1,maximumFractionDigits:1});}
function fmtTime(ts){if(!ts)return '-';return new Date(ts).toLocaleString('zh-CN',{hour12:false});}
let currentMap=null;
let okxLatestClose=0;
async function loadStatus(){const d=await fetch('/api/webdatasource/status').then(r=>r.json()).catch(()=>null);if(!d)return;document.getElementById('intervalMin').value=d.interval_min||15;document.getElementById('chromePath').value=d.chrome_path||'';document.getElementById('profileDir').value=d.profile_dir||'';document.getElementById('statusBox').textContent='运行中: '+(d.running?'是':'否')+' | 默认间隔 '+(d.interval_min||15)+' 分钟 | 最近成功 '+fmtTime(d.last_success_ts)+' | 最近错误 '+(d.last_error||'-');document.getElementById('runState').textContent=d.running?'抓取中':'空闲';const body=document.getElementById('runsBody');body.innerHTML='';for(const it of (d.recent_runs||[])){const tr=document.createElement('tr');tr.innerHTML='<td>'+it.id+'</td><td>'+it.window_days+'天</td><td>'+it.status+'</td><td>'+it.records_count+'</td><td>'+fmtTime(it.started_at)+'</td>';body.appendChild(tr);}}
async function saveSettings(){const body={interval_min:Number(document.getElementById('intervalMin').value||15),chrome_path:document.getElementById('chromePath').value||'',profile_dir:document.getElementById('profileDir').value||''};const r=await fetch('/api/webdatasource/settings',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify(body)});document.getElementById('saveMsg').textContent=r.ok?'保存成功':'保存失败';loadStatus();}
async function runNow(){const raw=document.getElementById('runWindow').value;const body=raw?{window_days:Number(raw)}:{};const r=await fetch('/api/webdatasource/run',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify(body)});document.getElementById('saveMsg').textContent=r.ok?'已触发抓取':('触发失败: '+await r.text());loadStatus();}
async function loadOKXClose(){const d=await fetch('/api/okx/latest-close').then(r=>r.ok?r.json():null).catch(()=>null);okxLatestClose=Number(d&&d.close||0)||0;}
function draw(){const c=document.getElementById('cv'),x=c.getContext('2d');const rect=c.getBoundingClientRect(),dpr=window.devicePixelRatio||1;const W=Math.max(760,Math.floor(rect.width)),H=Math.max(420,Math.floor(rect.height));c.width=W*dpr;c.height=H*dpr;x.setTransform(dpr,0,0,dpr,0,0);x.clearRect(0,0,W,H);x.fillStyle='#fff';x.fillRect(0,0,W,H);if(!currentMap||!currentMap.has_data||!(currentMap.points||[]).length){x.fillStyle='#64748b';x.font='14px sans-serif';x.fillText('暂无抓取数据',20,24);return;}const pts=currentMap.points||[];const minP=Number(currentMap.range_low||Math.min(...pts.map(p=>Number(p.price||0))));const maxP=Number(currentMap.range_high||Math.max(...pts.map(p=>Number(p.price||0))));const span=Math.max(1e-6,maxP-minP),padL=70,padR=20,padT=20,padB=40,pw=W-padL-padR,ph=H-padT-padB,by=padT+ph;const sx=v=>padL+((v-minP)/span)*pw,sy=v=>by-(v/Math.max(1,...pts.map(p=>Number(p.liq_value||0))))*ph*0.8;const maxV=Math.max(1,...pts.map(p=>Number(p.liq_value||0)));x.strokeStyle='#e5e7eb';x.font='12px sans-serif';for(let i=0;i<=4;i++){const y=padT+ph*(i/4);const val=maxV*(1-i/4);x.beginPath();x.moveTo(padL,y);x.lineTo(W-padR,y);x.stroke();x.fillStyle='#64748b';x.fillText(fmtAmt(val),6,y+4);}for(const pt of pts){const px=sx(Number(pt.price||0)),py=sy(Number(pt.liq_value||0)),r=3+16*Math.sqrt(Math.max(0,Number(pt.liq_value||0))/maxV),isLong=String(pt.side||'')==='long';x.beginPath();x.fillStyle=isLong?'rgba(220,38,38,0.35)':'rgba(22,163,74,0.35)';x.strokeStyle=isLong?'rgba(220,38,38,0.85)':'rgba(22,163,74,0.85)';x.arc(px,py,r,0,Math.PI*2);x.fill();x.stroke();}x.fillStyle='#64748b';for(let i=0;i<=6;i++){const p=minP+span*(i/6),px=sx(p),label=fmtPrice(p),lw=x.measureText(label).width;x.fillText(label,Math.max(padL,Math.min(px-lw/2,W-padR-lw)),H-10);}const cp=Number(currentMap.current_price||0);if(cp>0){const cpx=sx(cp);x.strokeStyle='#111827';x.setLineDash([6,4]);x.beginPath();x.moveTo(cpx,padT);x.lineTo(cpx,by);x.stroke();x.setLineDash([]);}const close=Number(okxLatestClose||0)>0?Number(okxLatestClose):cp;const labels=[...(currentMap.top_longs||[]).map((p,i)=>Object.assign({rank:i,side:'long'},p)),...(currentMap.top_shorts||[]).map((p,i)=>Object.assign({rank:i,side:'short'},p))];x.font='bold 12px sans-serif';for(const pt of labels){const price=Number(pt.price||0),val=Number(pt.liq_value||0);if(!(price>=minP&&price<=maxP&&val>0))continue;const px=sx(price),py=sy(val);const pct=close>0?((price-close)/close*100):0;const lines=['$'+fmtPrice(price),fmtYi(val),(pct>=0?'+':'')+pct.toFixed(2)+'%'];const color=pt.side==='long'?'rgba(185,28,28,0.96)':'rgba(21,128,61,0.96)';const bg=pt.side==='long'?'rgba(254,226,226,0.92)':'rgba(220,252,231,0.92)';let tw=0;for(const s of lines)tw=Math.max(tw,x.measureText(s).width);const bw=tw+10,bh=48;let lx=px+8,ly=py-54;if(lx+bw>W-6)lx=px-bw-8;if(lx<6)lx=6;if(ly<6)ly=py+10;if(ly+bh>H-padB)ly=Math.max(6,H-padB-bh);x.fillStyle=bg;x.strokeStyle=color;x.lineWidth=1;x.fillRect(lx,ly,bw,bh);x.strokeRect(lx,ly,bw,bh);x.fillStyle=color;for(let i=0;i<lines.length;i++)x.fillText(lines[i],lx+5,ly+15+i*14);x.beginPath();x.arc(px,py,4,0,Math.PI*2);x.fill();}}
async function loadMap(){const window=document.getElementById('windowSel').value;await loadOKXClose();const d=await fetch('/api/webdatasource/map?window='+encodeURIComponent(window)).then(r=>r.json()).catch(()=>null);currentMap=d;if(!d||!d.has_data){document.getElementById('chartMeta').textContent='暂无成功抓取快照'+(d&&d.last_error?(' | 最近错误: '+d.last_error):'');document.getElementById('cardWindow').textContent=window.toUpperCase();document.getElementById('cardLong').textContent='-';document.getElementById('cardShort').textContent='-';document.getElementById('cardTime').textContent='-';document.getElementById('exchangeBars').innerHTML='';draw();return;}document.getElementById('chartMeta').textContent='窗口 '+window.toUpperCase()+' | 价格区间 '+fmtPrice(d.range_low)+' - '+fmtPrice(d.range_high)+' | 快照 '+fmtTime(d.generated_at)+' | OKX 1m close '+(okxLatestClose?fmtPrice(okxLatestClose):'不可用');document.getElementById('cardWindow').textContent=window.toUpperCase();document.getElementById('cardLong').textContent=fmtAmt(d.long_total);document.getElementById('cardShort').textContent=fmtAmt(d.short_total);document.getElementById('cardTime').textContent=fmtTime(d.generated_at);const bars=document.getElementById('exchangeBars');bars.innerHTML='';for(const it of (d.by_exchange||[])){const row=document.createElement('div');row.style.margin='8px 0';row.innerHTML='<div class="row" style="justify-content:space-between"><span>'+it.exchange+'</span><span>'+fmtAmt(it.notional_usd)+'</span></div><div class="bar"><span style="width:'+Math.max(2,Math.round((Number(it.share||0))*100))+'%"></span></div>';bars.appendChild(row);}draw();}
window.addEventListener('resize',draw);initTheme();loadStatus();loadMap();(async()=>{try{const r=await fetch('/api/version');const v=await r.json();const el=document.getElementById('globalFooter');if(el)el.textContent='Code by Yuhao@jiansutech.com - '+(v.commit_time||'-')+' - '+(v.commit_id||'-')+' - '+(v.branch||'-');}catch(_){}})();setInterval(loadStatus,5000);
</script></body></html>`
