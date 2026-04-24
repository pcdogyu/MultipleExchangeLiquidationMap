package liqmap

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/cdproto/browser"
	"github.com/chromedp/cdproto/input"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
)

const (
	defaultWebDataSourceIntervalMin  = 15
	defaultWebDataSourceTimeoutSec   = 60
	defaultWebDataSourceInitLoginSec = 90
	defaultWebDataSourceInitTimeout  = (defaultWebDataSourceInitLoginSec + 15) * time.Second
	defaultWebDataSourceMaxAttempts  = 3
	webDataSourcePayloadDir          = "playload"
)

type WebDataSourceManager struct {
	app *App
	mu  sync.Mutex

	running       bool
	currentAction string
	stepLogs      []WebDataSourceStepLog
	runStartedAt  time.Time
	stepStartedAt time.Time
}

type WebDataSourceSettings struct {
	Enabled            bool   `json:"enabled"`
	IntervalMin        int    `json:"interval_min"`
	TimeoutSec         int    `json:"timeout_sec"`
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
	Enabled       bool                   `json:"enabled"`
	Running       bool                   `json:"running"`
	IntervalMin   int                    `json:"interval_min"`
	TimeoutSec    int                    `json:"timeout_sec"`
	ChromePath    string                 `json:"chrome_path"`
	ProfileDir    string                 `json:"profile_dir"`
	LastError     string                 `json:"last_error,omitempty"`
	LastSuccessTS int64                  `json:"last_success_ts,omitempty"`
	NextRunTS     int64                  `json:"next_run_ts,omitempty"`
	CurrentAction string                 `json:"current_action,omitempty"`
	StepLogs      []WebDataSourceStepLog `json:"step_logs,omitempty"`
	LastRun       *WebDataSourceRunRow   `json:"last_run,omitempty"`
	RecentRuns    []WebDataSourceRunRow  `json:"recent_runs,omitempty"`
}

type WebDataSourceStepLog struct {
	TS     int64  `json:"ts"`
	Step   string `json:"step"`
	Result string `json:"result"`
	Detail string `json:"detail,omitempty"`
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
	Source    string  `json:"source,omitempty"`
}

type webDataSourceSession struct {
	taskCtx context.Context
	cancel  context.CancelFunc
	closeOnce sync.Once
}

type webDataSourceProgress struct {
	action    string
	startedAt time.Time
	stepSince time.Time
	manager   *WebDataSourceManager
}

type webMouseTarget struct {
	X     float64 `json:"x"`
	Y     float64 `json:"y"`
	Text  string  `json:"text"`
	Value string  `json:"value"`
}

func newWebDataSourceManager(app *App) *WebDataSourceManager {
	m := &WebDataSourceManager{app: app}
	m.cleanupStaleRunningRuns("run interrupted before completion")
	return m
}

func (m *WebDataSourceManager) start(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				cfg := m.loadSettings()
				if !cfg.Enabled {
					continue
				}
				if !m.shouldAutoRun(cfg, time.Now()) {
					continue
				}
				_, _ = m.triggerRun(ctx, nil)
			}
		}
	}()
}

func (m *WebDataSourceManager) saveCapturedPayload(windowDays, attempt int, payload map[string]any, meta capturedPayloadMeta) error {
	if len(payload) == 0 {
		return nil
	}
	if err := os.MkdirAll(webDataSourcePayloadDir, 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return err
	}
	source := strings.TrimSpace(strings.ToLower(meta.Source))
	if source == "" {
		source = "unknown"
	}
	name := fmt.Sprintf("%s_%dd_attempt%d_%s_payload.json", time.Now().Format("20060102_150405"), windowDays, attempt, source)
	return os.WriteFile(filepath.Join(webDataSourcePayloadDir, name), raw, 0o644)
}

func (m *WebDataSourceManager) shouldAutoRun(cfg WebDataSourceSettings, now time.Time) bool {
	m.mu.Lock()
	running := m.running
	m.mu.Unlock()
	if running {
		return false
	}

	lastTS := cfg.LastRunFinishedTS
	if cfg.LastSuccessTS > lastTS {
		lastTS = cfg.LastSuccessTS
	}
	if captureTS, ok := m.app.nextWebDataSourceCaptureTS(now); ok {
		return now.UnixMilli() >= captureTS && lastTS < captureTS
	}

	intervalMin := cfg.IntervalMin
	if intervalMin <= 0 {
		intervalMin = defaultWebDataSourceIntervalMin
	}
	interval := time.Duration(intervalMin) * time.Minute

	if lastTS <= 0 {
		return true
	}
	lastRunAt := time.UnixMilli(lastTS)
	return !lastRunAt.Add(interval).After(now)
}

func (m *WebDataSourceManager) nextAutoRunTS(cfg WebDataSourceSettings, now time.Time) int64 {
	if captureTS, ok := m.app.nextWebDataSourceCaptureTS(now); ok {
		return captureTS
	}

	intervalMin := cfg.IntervalMin
	if intervalMin <= 0 {
		intervalMin = defaultWebDataSourceIntervalMin
	}
	interval := time.Duration(intervalMin) * time.Minute

	lastTS := cfg.LastRunFinishedTS
	if cfg.LastSuccessTS > lastTS {
		lastTS = cfg.LastSuccessTS
	}
	if lastTS <= 0 {
		return now.UnixMilli()
	}
	return time.UnixMilli(lastTS).Add(interval).UnixMilli()
}

func (m *WebDataSourceManager) triggerRun(parent context.Context, windowDays *int) (bool, error) {
	m.mu.Lock()
	if m.running {
		m.mu.Unlock()
		return false, errors.New("webdatasource run already in progress")
	}
	m.cleanupStaleRunningRuns("run interrupted before completion")
	m.running = true
	m.mu.Unlock()

	go func() {
		defer func() {
			m.mu.Lock()
			m.running = false
			m.mu.Unlock()
		}()
		cfg := m.loadSettings()
		timeout := time.Duration(cfg.TimeoutSec) * time.Second
		if timeout <= 0 {
			timeout = defaultWebDataSourceTimeoutSec * time.Second
		}
		ctx, cancel := context.WithTimeout(parent, timeout)
		defer cancel()
		if err := m.runOnce(ctx, windowDays); err != nil {
			log.Printf("webdatasource run failed: %v", err)
		}
	}()
	return true, nil
}

func (m *WebDataSourceManager) triggerInit(parent context.Context) (bool, error) {
	m.mu.Lock()
	if m.running {
		m.mu.Unlock()
		return false, errors.New("webdatasource run already in progress")
	}
	m.cleanupStaleRunningRuns("run interrupted before completion")
	m.running = true
	m.mu.Unlock()

	go func() {
		defer func() {
			m.mu.Lock()
			m.running = false
			m.mu.Unlock()
		}()
		ctx, cancel := context.WithTimeout(parent, defaultWebDataSourceInitTimeout)
		defer cancel()
		if err := m.runInitSession(ctx); err != nil {
			log.Printf("webdatasource init failed: %v", err)
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
	m.resetStepLogs()

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
		m.appendStepLog("Detect Chrome", "failed", err.Error())
		m.finishRunState("failed", err.Error(), 0)
		return err
	}

	progress := &webDataSourceProgress{
		action:    "Start web datasource capture",
		startedAt: time.Now(),
		stepSince: time.Now(),
		manager:   m,
	}
	m.syncProgressClock(progress.startedAt, progress.stepSince, progress.action)
	m.appendStepLog("Start capture task", "info", fmt.Sprintf("windows=%v", windows))
	stopProgressLogger := m.startProgressLogger(ctx, progress)
	defer stopProgressLogger()

	progress.setAction("Launch Chrome and open Coinglass")
	session, err := m.newCaptureSessionV4(ctx, chromePath, cfg.ProfileDir, progress)
	if err != nil {
		m.appendStepLog("Init Coinglass capture session", "failed", err.Error())
		m.finishRunState("failed", err.Error(), 0)
		return err
	}
	defer session.close()

	totalRecords := 0
	for _, days := range windows {
		runID, err := m.insertRun(days, "running", "", 0)
		if err != nil {
			continue
		}
		var recordsThisRun int
		var meta capturedPayloadMeta
		var attemptErr error
		for attempt := 1; attempt <= defaultWebDataSourceMaxAttempts; attempt++ {
			progress.setAction(fmt.Sprintf("Switch and capture %d day window (attempt %d/%d)", days, attempt, defaultWebDataSourceMaxAttempts))
			m.appendStepLog("Prepare capture window", "info", fmt.Sprintf("%d day | attempt %d/%d", days, attempt, defaultWebDataSourceMaxAttempts))
			payload, currentMeta, err := m.captureWindowV2(session, progress, days)
			if err == nil {
				points, rangeLow, rangeHigh := normalizeWebDataSourcePayload(payload)
				if rangeLow == 0 && currentMeta.RangeLow != 0 {
					rangeLow = currentMeta.RangeLow
				}
				if rangeHigh == 0 && currentMeta.RangeHigh != 0 {
					rangeHigh = currentMeta.RangeHigh
				}
				if len(points) == 0 {
					err = errors.New("coinglass payload parsed 0 points")
				} else {
					snapshotID, snapErr := m.insertSnapshot(days, rangeLow, rangeHigh, payload)
					if snapErr != nil {
						err = snapErr
					} else if pointErr := m.insertPoints(snapshotID, days, points); pointErr != nil {
						err = pointErr
					} else if payloadErr := m.saveCapturedPayload(days, attempt, payload, currentMeta); payloadErr != nil {
						err = payloadErr
					} else {
						meta = currentMeta
						recordsThisRun = len(points)
						totalRecords += len(points)
						metaJSON, _ := json.Marshal(meta)
						_, _ = m.app.db.Exec(`UPDATE webdatasource_runs SET source_meta_json=? WHERE id=?`, string(metaJSON), runID)
						_ = m.updateRun(runID, "success", "", len(points))
						m.appendStepLog("Save capture result", "success", fmt.Sprintf("%d day | %d points | attempt %d/%d", days, len(points), attempt, defaultWebDataSourceMaxAttempts))
						attemptErr = nil
						break
					}
				}
			}
			attemptErr = err
			m.appendStepLog("Capture window failed", "failed", fmt.Sprintf("%d day | attempt %d/%d | %s", days, attempt, defaultWebDataSourceMaxAttempts, err.Error()))
			if attempt < defaultWebDataSourceMaxAttempts {
				m.appendStepLog("Retry capture window", "info", fmt.Sprintf("%d day | wait 2s before retry", days))
				if sleepErr := sleepWithContext(ctx, 2*time.Second); sleepErr != nil {
					attemptErr = sleepErr
					break
				}
			}
		}
		if attemptErr != nil {
			_ = m.updateRun(runID, "failed", attemptErr.Error(), recordsThisRun)
			m.finishRunState("failed", attemptErr.Error(), 0)
			return attemptErr
		}
	}
	progress.setAction("Capture completed")
	m.appendStepLog("Capture finished", "success", fmt.Sprintf("records=%d", totalRecords))
	m.finishRunState("success", "", totalRecords)
	return nil
}

func (m *WebDataSourceManager) runInitSession(ctx context.Context) error {
	started := time.Now().UnixMilli()
	_ = m.setSetting("last_run_started_ts", strconv.FormatInt(started, 10))
	_ = m.setSetting("last_run_status", "running")
	_ = m.setSetting("last_error", "")
	m.resetStepLogs()

	cfg := m.loadSettings()
	chromePath := strings.TrimSpace(cfg.ChromePath)
	if chromePath == "" {
		chromePath = detectChromePath()
	}
	if chromePath == "" {
		err := errors.New("chrome/chromium executable not found")
		m.appendStepLog("Detect Chrome", "failed", err.Error())
		m.finishRunState("failed", err.Error(), 0)
		return err
	}
	if err := os.MkdirAll(cfg.ProfileDir, 0o755); err != nil {
		m.appendStepLog("Prepare Profile Dir", "failed", err.Error())
		m.finishRunState("failed", err.Error(), 0)
		return err
	}

	progress := &webDataSourceProgress{
		action:    "Initialize Coinglass session",
		startedAt: time.Now(),
		stepSince: time.Now(),
		manager:   m,
	}
	m.syncProgressClock(progress.startedAt, progress.stepSince, progress.action)
	m.appendStepLog("Start Init Session", "info", "Open Coinglass and keep local profile")
	stopProgressLogger := m.startProgressLogger(ctx, progress)
	defer stopProgressLogger()

	opts := webDataSourceChromeOptions(chromePath, cfg.ProfileDir, false)
	allocCtx, cancelAlloc := chromedp.NewExecAllocator(ctx, opts...)
	taskCtx, cancelTask := chromedp.NewContext(allocCtx)
	session := &webDataSourceSession{
		taskCtx: taskCtx,
		cancel: func() {
			cancelTask()
			cancelAlloc()
		},
	}
	defer session.close()

	if err := m.runLoggedStep(session.taskCtx, progress, "Open Coinglass Login Page", func() (string, error) {
		err := chromedp.Run(session.taskCtx, chromedp.ActionFunc(func(ctx context.Context) error {
			_, _, _, err := page.Navigate("https://www.coinglass.com/zh/pro/futures/LiquidationMap").Do(ctx)
			return err
		}))
		if err != nil {
			return "", err
		}
		return "Chrome opened, please complete login in the page", nil
	}); err != nil {
		m.finishRunState("failed", err.Error(), 0)
		return err
	}

	if err := m.runLoggedStep(session.taskCtx, progress, "Wait For Manual Login", func() (string, error) {
		m.appendStepLog("Login Tip", "info", fmt.Sprintf("Please finish Coinglass login within %d seconds; the saved profile will be reused later", defaultWebDataSourceInitLoginSec))
		deadline := time.Now().Add(time.Duration(defaultWebDataSourceInitLoginSec) * time.Second)
		if ctxDeadline, ok := session.taskCtx.Deadline(); ok && ctxDeadline.Before(deadline) {
			deadline = ctxDeadline
		}
		for {
			remaining := int(time.Until(deadline).Seconds())
			if remaining < 0 {
				remaining = 0
			}
			progress.setAction(fmt.Sprintf("Login to Coinglass in Chrome, %d seconds remaining", remaining))
			if time.Now().After(deadline) {
				break
			}
			sleepStep := 5 * time.Second
			if time.Until(deadline) < sleepStep {
				sleepStep = time.Until(deadline)
			}
			if sleepStep <= 0 {
				break
			}
			if err := sleepWithContext(session.taskCtx, sleepStep); err != nil {
				return "", err
			}
		}
		return "Login wait finished; profile session kept on disk", nil
	}); err != nil {
		m.finishRunState("failed", err.Error(), 0)
		return err
	}

	progress.setAction("Init Session Completed")
	m.appendStepLog("Init Session Completed", "success", "Coinglass login session saved to the configured Profile directory")
	m.finishRunState("success", "", 0)
	return nil
}

func (p *webDataSourceProgress) setAction(action string) {
	p.action = action
	p.stepSince = time.Now()
	if p.manager != nil {
		p.manager.setCurrentAction(action)
	}
}

func (m *WebDataSourceManager) setCurrentAction(action string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.currentAction = strings.TrimSpace(action)
	if m.currentAction != "" {
		m.stepStartedAt = time.Now()
	}
}

func (m *WebDataSourceManager) syncProgressClock(startedAt, stepStartedAt time.Time, action string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.runStartedAt = startedAt
	m.stepStartedAt = stepStartedAt
	m.currentAction = strings.TrimSpace(action)
}

func (m *WebDataSourceManager) resetStepLogs() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.currentAction = ""
	m.stepLogs = nil
	m.runStartedAt = time.Time{}
	m.stepStartedAt = time.Time{}
}

func (m *WebDataSourceManager) appendStepLog(step, result, detail string) {
	entry := WebDataSourceStepLog{
		TS:     time.Now().UnixMilli(),
		Step:   strings.TrimSpace(step),
		Result: strings.TrimSpace(result),
		Detail: strings.TrimSpace(detail),
	}
	m.mu.Lock()
	m.stepLogs = append(m.stepLogs, entry)
	if len(m.stepLogs) > 240 {
		m.stepLogs = append([]WebDataSourceStepLog(nil), m.stepLogs[len(m.stepLogs)-240:]...)
	}
	runStartedAt := m.runStartedAt
	stepStartedAt := m.stepStartedAt
	m.mu.Unlock()
	totalSec := 0
	if !runStartedAt.IsZero() {
		totalSec = int(time.Since(runStartedAt).Seconds())
	}
	stepSec := 0
	if !stepStartedAt.IsZero() {
		stepSec = int(time.Since(stepStartedAt).Seconds())
	}
	message := strings.TrimSpace(entry.Step)
	if strings.TrimSpace(entry.Detail) != "" {
		message += " | " + strings.TrimSpace(entry.Detail)
	}
	if message == "" {
		message = "-"
	}
	log.Printf("webdatasource progress: t+%ds | step+%ds | %s | %s", totalSec, stepSec, message, entry.Result)
}

func (m *WebDataSourceManager) snapshotStepLogs() (string, []WebDataSourceStepLog) {
	m.mu.Lock()
	defer m.mu.Unlock()
	logs := append([]WebDataSourceStepLog(nil), m.stepLogs...)
	return m.currentAction, logs
}

func sleepWithContext(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func webDataSourceChromeOptions(chromePath, profileDir string, startMinimized bool) []chromedp.ExecAllocatorOption {
	opts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.ExecPath(chromePath),
		chromedp.Flag("headless", false),
		chromedp.UserAgent("Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36"),
		chromedp.UserDataDir(profileDir),
		chromedp.Flag("disable-blink-features", "AutomationControlled"),
		chromedp.WindowSize(1440, 900),
	)
	if startMinimized {
		opts = append(opts,
			chromedp.Flag("start-minimized", true),
			chromedp.Flag("window-position", "-32000,-32000"),
		)
	}
	return opts
}

func minimizeBrowserWindow(ctx context.Context) error {
	return chromedp.Run(ctx, chromedp.ActionFunc(func(ctx context.Context) error {
		windowID, _, err := browser.GetWindowForTarget().Do(ctx)
		if err != nil {
			return err
		}
		return browser.SetWindowBounds(windowID, &browser.Bounds{WindowState: browser.WindowStateMinimized}).Do(ctx)
	}))
}

func dispatchMouseClick(ctx context.Context, x, y float64) error {
	if err := input.DispatchMouseEvent(input.MouseMoved, x, y).Do(ctx); err != nil {
		return err
	}
	if err := input.DispatchMouseEvent(input.MousePressed, x, y).WithButton(input.Left).WithClickCount(1).Do(ctx); err != nil {
		return err
	}
	return input.DispatchMouseEvent(input.MouseReleased, x, y).WithButton(input.Left).WithClickCount(1).Do(ctx)
}

func webDataSourceFindTargetPanelJS() string {
	return `(() => {
		const isRefreshButton = btn => {
			if (!btn || btn.offsetParent === null) return false;
			const svg = btn.querySelector('svg');
			if (!svg) return false;
			const polylines = Array.from(svg.querySelectorAll('polyline')).map(node => String(node.getAttribute('points') || '').trim());
			return polylines.includes('1 4 1 10 7 10') && polylines.includes('23 20 23 14 17 14') && !!svg.querySelector('path');
		};
		const isPeriodRoot = root => {
			if (!root) return false;
			const btn = root.querySelector('button[role="combobox"]');
			const hidden = root.querySelector('input[aria-hidden="true"]');
			if (!btn || !hidden) return false;
			const value = String(hidden.value || hidden.getAttribute('value') || '').trim();
			return ['1', '7', '30'].includes(value);
		};
		const inputs = Array.from(document.querySelectorAll('input.MuiAutocomplete-input[role="combobox"]'))
			.filter(input => ['BTC', 'ETH'].includes(String(input.value || '').trim()));
		for (const input of inputs) {
			let container = input.parentElement;
			for (let depth = 0; depth < 12 && container; depth++) {
				const periodRoots = Array.from(container.querySelectorAll('div.MuiSelect-root')).filter(isPeriodRoot);
				const chartRoot = container.querySelector('.echarts-for-react');
				const hasRefresh = Array.from(container.querySelectorAll('button')).some(isRefreshButton);
				if (periodRoots.length && chartRoot && hasRefresh) {
					return container;
				}
				container = container.parentElement;
			}
		}
		return null;
	})()`
}

func webDataSourceFindPeriodRootJS(findTargetPanelJS string) string {
	return `(() => {
		const panel = ` + findTargetPanelJS + `;
		if (!panel) return null;
		return Array.from(panel.querySelectorAll('div.MuiSelect-root')).find(root => {
			const btn = root.querySelector('button[role="combobox"]');
			const hidden = root.querySelector('input[aria-hidden="true"]');
			return !!btn && !!hidden && ['1', '7', '30'].includes(String(hidden.value || hidden.getAttribute('value') || '').trim());
		}) || null;
	})()`
}

func webDataSourceExtractChartPayloadJS(findTargetPanelJS string) string {
	return `(() => {
		const panel = ` + findTargetPanelJS + `;
		if (!panel || !window.echarts) return null;
		const chartRoot = panel.querySelector('.echarts-for-react');
		if (!chartRoot) return null;
		const targets = [
			chartRoot,
			chartRoot.querySelector('div'),
			chartRoot.querySelector('canvas')
		].filter(Boolean);
		let inst = null;
		for (const target of targets) {
			try {
				inst = window.echarts.getInstanceByDom(target);
			} catch (_) {}
			if (inst) break;
		}
		if (!inst || typeof inst.getOption !== 'function') return null;
		const option = inst.getOption();
		if (!option || !Array.isArray(option.series)) return null;
		const toNumber = value => {
			if (typeof value === 'number' && Number.isFinite(value)) return value;
			if (typeof value === 'string') {
				const cleaned = value.replace(/,/g, '').trim();
				const num = Number(cleaned);
				return Number.isFinite(num) ? num : 0;
			}
			return 0;
		};
		const axisArrays = [];
		const pushAxis = axis => {
			if (!axis) return;
			const list = Array.isArray(axis) ? axis : [axis];
			for (const item of list) {
				if (item && Array.isArray(item.data) && item.data.length) {
					axisArrays.push(item.data);
				}
			}
		};
		pushAxis(option.xAxis);
		const axisData = axisArrays.find(arr => Array.isArray(arr) && arr.length) || [];
		const inferExchange = name => {
			const labels = ['Binance', 'OKX', 'Bybit', 'Bitget', 'Gate', 'MEXC', 'HTX', 'KuCoin', 'Deribit', 'BitMEX', 'Hyperliquid'];
			for (const label of labels) {
				if (String(name || '').toLowerCase().includes(label.toLowerCase())) {
					return label;
				}
			}
			return String(name || 'UNKNOWN').trim() || 'UNKNOWN';
		};
		const extractPoint = (item, idx) => {
			if (Array.isArray(item)) {
				const price = toNumber(item[0]);
				const value = toNumber(item[item.length - 1]);
				return {price, value};
			}
			if (item && typeof item === 'object') {
				if (Array.isArray(item.value)) {
					return {price: toNumber(item.value[0]), value: toNumber(item.value[item.value.length - 1])};
				}
				const price = toNumber(item.price ?? item.x ?? axisData[idx]);
				const value = toNumber(item.value ?? item.y ?? item.amount ?? item.notional);
				return {price, value};
			}
			return {price: toNumber(axisData[idx]), value: toNumber(item)};
		};
		const payload = {
			source: 'echarts',
			rangeLow: 0,
			rangeHigh: 0,
			long: [],
			short: [],
			seriesMeta: []
		};
		for (const series of option.series) {
			if (!series || !Array.isArray(series.data) || !series.data.length) continue;
			const name = String(series.name || '').trim();
			const lower = name.toLowerCase();
			const aggregate = lower.includes('cumulative') || lower.includes('total') || name.includes('累计') || name.includes('总');
			let explicitSide = '';
			if (lower.includes('long') || name.includes('多')) explicitSide = 'long';
			if (lower.includes('short') || name.includes('空')) explicitSide = 'short';
			const exchange = inferExchange(name);
			let nonZero = 0;
			for (let idx = 0; idx < series.data.length; idx++) {
				const parsed = extractPoint(series.data[idx], idx);
				const price = parsed.price;
				const rawValue = parsed.value;
				if (!(price > 0) || !(Math.abs(rawValue) > 0)) continue;
				const side = explicitSide || (rawValue >= 0 ? 'long' : 'short');
				const point = {
					price,
					value: Math.abs(rawValue),
					exchange
				};
				if (!aggregate || option.series.length <= 2) {
					payload[side].push(point);
				}
				nonZero++;
			}
			payload.seriesMeta.push({
				name,
				exchange,
				side: explicitSide || 'signed',
				aggregate,
				dataLen: series.data.length,
				nonZero
			});
		}
		const prices = payload.long.concat(payload.short).map(point => Number(point.price || 0)).filter(price => price > 0);
		if (!prices.length) return null;
		payload.rangeLow = Math.min(...prices);
		payload.rangeHigh = Math.max(...prices);
		return payload;
	})()`
}

func (m *WebDataSourceManager) runLoggedStep(ctx context.Context, progress *webDataSourceProgress, step string, fn func() (string, error)) error {
	progress.setAction(step)
	m.appendStepLog(step, "info", "wait 1s")
	if err := sleepWithContext(ctx, 1*time.Second); err != nil {
		m.appendStepLog(step, "failed", err.Error())
		return err
	}
	detail, err := fn()
	if err != nil {
		m.appendStepLog(step, "failed", err.Error())
		return err
	}
	if strings.TrimSpace(detail) == "" {
		detail = "成功"
	}
	m.appendStepLog(step, "success", detail)
	return nil
}

func (m *WebDataSourceManager) startProgressLogger(ctx context.Context, progress *webDataSourceProgress) func() {
	done := make(chan struct{})
	go func() {
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-done:
				return
			case <-ticker.C:
				msg := strings.TrimSpace(progress.action)
				if msg == "" {
					msg = "执行抓取流程"
				}
				totalSec := int(time.Since(progress.startedAt).Seconds())
				stepSec := int(time.Since(progress.stepSince).Seconds())
				log.Printf("webdatasource progress: t+%ds | step+%ds | %s", totalSec, stepSec, msg)
			}
		}
	}()
	return func() {
		close(done)
	}
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

func (m *WebDataSourceManager) captureWindowLegacy(ctx context.Context, chromePath, profileDir string, days int) (map[string]any, capturedPayloadMeta, error) {
	label := map[int]string{1: "1天", 7: "7天", 30: "30天"}[days]
	if label == "" {
		label = fmt.Sprintf("%d天", days)
	}
	if err := os.MkdirAll(profileDir, 0o755); err != nil {
		return nil, capturedPayloadMeta{}, err
	}
	opts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.ExecPath(chromePath),
		chromedp.Flag("headless", false),
		chromedp.Flag("start-minimized", true),
		chromedp.UserAgent("Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36"),
		chromedp.UserDataDir(profileDir),
		chromedp.Flag("disable-blink-features", "AutomationControlled"),
		chromedp.WindowSize(1440, 900),
	)
	allocCtx, cancelAlloc := chromedp.NewExecAllocator(ctx, opts...)
	defer cancelAlloc()
	taskCtx, cancelTask := chromedp.NewContext(allocCtx)
	defer cancelTask()

	var ok bool
	if err := chromedp.Run(taskCtx,
		chromedp.ActionFunc(func(ctx context.Context) error {
			_, err := page.AddScriptToEvaluateOnNewDocument(webDataSourceHookJS).Do(ctx)
			return err
		}),
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
					const norm = s => String(s || '').replace(/\s+/g, '').toLowerCase();
					let winBtn = Array.from(btns).find(b => /^(1d|7d|30d|1天|7天|30天|1日|7日|30日)$/.test(norm(b.textContent)));
					if (!winBtn) winBtn = btns[btns.length - 1];
					window._cgSection = {
						inputIdx: Array.from(document.querySelectorAll('.MuiAutocomplete-input')).indexOf(inputs[0]),
						btnIdx: Array.from(document.querySelectorAll('.MuiSelect-button')).indexOf(winBtn)
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
		for (const type of ['pointerdown','mousedown','mouseup','click']) btn.dispatchEvent(new MouseEvent(type, {bubbles:true, cancelable:true, view:window}));
		return true;
	})()`, btnIdx)
	if err := chromedp.Run(taskCtx, chromedp.Evaluate(js, &ok), chromedp.Sleep(1200*time.Millisecond)); err != nil || !ok {
		return nil, capturedPayloadMeta{}, errors.New("coinglass window selector not found")
	}

	windowIdx := map[int]int{1: 0, 7: 1, 30: 2}[days]
	js = fmt.Sprintf(`(() => {
		const norm = s => String(s || '').replace(/\s+/g, '').toLowerCase();
		const want = norm(%q);
		const dayNum = String(%d);
		const wants = [want, dayNum + 'd', dayNum + '天', dayNum + '日', dayNum + 'day', dayNum + 'days'];
		const optionSel = '[role="option"], [role="menuitem"], .MuiOption-root, .MuiMenuItem-root, li[class*="Option"], li[class*="option"], li[class*="MenuItem"], [data-value]';
		const all = Array.from(document.querySelectorAll(optionSel)).filter(el => el.offsetParent !== null);
		const candidates = all.map((el, i) => ({el, i, text: norm(el.textContent), value: norm(el.getAttribute('data-value') || el.getAttribute('value') || '')}));
		let hit = candidates.find(x => wants.includes(x.text) || wants.includes(x.value));
		if (!hit) hit = candidates.find(x => wants.some(w => x.text.includes(w) || x.value.includes(w)));
		if (!hit) hit = candidates.find(x => x.text === dayNum || x.value === dayNum);
		if (!hit && candidates[%d]) hit = candidates[%d];
		if (hit) { for (const type of ['pointerdown','mousedown','mouseup','click']) hit.el.dispatchEvent(new MouseEvent(type, {bubbles:true, cancelable:true, view:window})); return true; }
		return false;
	})()`, label, days, windowIdx, windowIdx)
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

	deadline := time.Now().Add((defaultWebDataSourceTimeoutSec - 2) * time.Second)
	if ctxDeadline, ok := ctx.Deadline(); ok {
		deadline = ctxDeadline.Add(-2 * time.Second)
	}
	if minDeadline := time.Now().Add(5 * time.Second); deadline.Before(minDeadline) {
		deadline = minDeadline
	}
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

/*
func (m *WebDataSourceManager) newCaptureSession(ctx context.Context, chromePath, profileDir string, progress *webDataSourceProgress) (*webDataSourceSession, error) {
	if err := os.MkdirAll(profileDir, 0o755); err != nil {
		return nil, err
	}
	opts := webDataSourceChromeOptions(chromePath, profileDir, true)
	allocCtx, cancelAlloc := chromedp.NewExecAllocator(ctx, opts...)
	taskCtx, cancelTask := chromedp.NewContext(allocCtx)
	session := &webDataSourceSession{
		taskCtx: taskCtx,
		cancel: func() {
			cancelTask()
			cancelAlloc()
		},
	}

	var ok bool
	progress.setAction("打开 coinglass 页面")
	if err := chromedp.Run(session.taskCtx,
		chromedp.ActionFunc(func(ctx context.Context) error {
			_, err := page.AddScriptToEvaluateOnNewDocument(webDataSourceHookJS).Do(ctx)
			return err
		}),
		chromedp.ActionFunc(func(ctx context.Context) error {
			_, _, _, _, err := page.Navigate("https://www.coinglass.com/zh/pro/futures/LiquidationMap").Do(ctx)
			return err
		}),
	); err != nil {
		session.close()
		return nil, err
	}

	progress.setAction("等待页面首屏 3 秒")
	select {
	case <-session.taskCtx.Done():
		session.close()
		return nil, session.taskCtx.Err()
	case <-time.After(3 * time.Second):
	}

	progress.setAction("注入页面数据抓取钩子")
	if err := chromedp.Run(session.taskCtx, chromedp.Evaluate(webDataSourceHookJS, nil)); err != nil {
		session.close()
		return nil, err
	}

	progress.setAction("查找 BTC 搜索输入框控件")
	var searchInputInfo struct {
		Found       bool   `json:"found"`
		ID          string `json:"id"`
		Value       string `json:"value"`
		Placeholder string `json:"placeholder"`
		ClassName   string `json:"className"`
	}
	if err := chromedp.Run(session.taskCtx,
		chromedp.Evaluate(`(() => {
			const inputs = Array.from(document.querySelectorAll('input[role="combobox"]'));
			const hit = inputs.find(el => {
				const ph = (el.getAttribute('placeholder') || '').trim();
				const val = (el.value || '').trim();
				const cls = (el.className || '').toString();
				return ph === '搜索' && val === 'BTC' && cls.includes('MuiAutocomplete-input');
			});
			if (!hit) { /*
				return {found:false, id:'', value:'', placeholder:'', className:''};
			}
			return {
				found: true,
				id: hit.id || '',
				value: hit.value || '',
				placeholder: hit.getAttribute('placeholder') || '',
				className: hit.className || ''
			};
		})()`, &searchInputInfo),
	); err != nil {
		session.close()
		return nil, err
	}
	if searchInputInfo.Found {
		log.Printf("webdatasource 找到 BTC 搜索输入框: id=%q value=%q placeholder=%q class=%q", searchInputInfo.ID, searchInputInfo.Value, searchInputInfo.Placeholder, searchInputInfo.ClassName)
	} else {
		log.Printf("webdatasource 没有找到 BTC 搜索输入框")
	}

	progress.setAction("查找比特币交易所清算地图的币对下拉框和周期下拉框")
	var sectionInfo struct {
		TitleText string `json:"title_text"`
		SymbolBtnIdx int `json:"symbolBtnIdx"`
		BtnIdx    int    `json:"btnIdx"`
		CanvasIdx int    `json:"canvasIdx"`
	}
	if err := chromedp.Run(session.taskCtx,
		chromedp.Evaluate(`(() => {
			const title = Array.from(document.querySelectorAll('h1.MuiTypography-root, h1')).find(h => {
				const t = (h.textContent || '').trim();
				return (t.includes('交易所清算地图') || t.includes('Exchange Liquidation Map')) && !t.includes('Hyperliquid');
			});
			if (!title) return null;
			let container = title;
			for (let i = 0; i < 12; i++) {
				container = (i === 0 ? title : container.parentElement);
				if (!container) break;
				const btns = container.querySelectorAll('button.MuiSelect-button');
				const canvas = container.querySelector('canvas');
				if (btns.length >= 2 && canvas) {
					const data = {
						title_text: (title.textContent || '').trim(),
						symbolBtnIdx: Array.from(document.querySelectorAll('button.MuiSelect-button')).indexOf(btns[0]),
						btnIdx: Array.from(document.querySelectorAll('button.MuiSelect-button')).indexOf(btns[1]),
						canvasIdx: Array.from(document.querySelectorAll('canvas')).indexOf(canvas)
					};
					window._cgSection = data;
					return data;
				}
			}
			return null;
		})()`, &sectionInfo),
		); err != nil {
		session.close()
		return nil, err
	}
	if sectionInfo.TitleText == "" || sectionInfo.SymbolBtnIdx < 0 || sectionInfo.BtnIdx < 0 || sectionInfo.CanvasIdx < 0 {
		log.Printf("webdatasource 没有找到下拉框: 比特币交易所清算地图")
		session.close()
		return nil, errors.New("coinglass section not found")
	}
	log.Printf("webdatasource 找到下拉框: title=%q symbol_button_idx=%d period_button_idx=%d canvas_idx=%d", sectionInfo.TitleText, sectionInfo.SymbolBtnIdx, sectionInfo.BtnIdx, sectionInfo.CanvasIdx)

	if err := chromedp.Run(session.taskCtx,
		chromedp.Evaluate(`window._cgSection.symbolBtnIdx`, &session.symbolBtnIdx),
		chromedp.Evaluate(`window._cgSection.btnIdx`, &session.btnIdx),
		chromedp.Evaluate(`window._cgSection.canvasIdx`, &session.canvasIdx),
	); err != nil {
		session.close()
		return nil, err
	}

	progress.setAction("打开币对下拉框")
	log.Printf("webdatasource 准备点击币对下拉框: symbol_button_idx=%d", session.symbolBtnIdx)
	js := fmt.Sprintf(`(() => {
		const btns = document.querySelectorAll('button.MuiSelect-button');
		const btn = btns[%d];
		if (!btn) return false;
		for (const type of ['pointerdown','mousedown','mouseup','click']) btn.dispatchEvent(new MouseEvent(type, {bubbles:true, cancelable:true, view:window}));
		return true;
	})()`, session.symbolBtnIdx)
	if err := chromedp.Run(session.taskCtx, chromedp.Evaluate(js, &ok)); err != nil || !ok {
		session.close()
		return nil, errors.New("coinglass symbol selector not found")
	}
	if err := chromedp.Run(session.taskCtx, chromedp.Sleep(800*time.Millisecond)); err != nil {
		session.close()
		return nil, err
	}

	progress.setAction("选择第二个 ETH 币对选项")
	var pickedOption string
	if err := chromedp.Run(session.taskCtx, chromedp.Evaluate(`(() => {
		const all = Array.from(document.querySelectorAll('[role="option"], .MuiOption-root, li[class*="Option"], li[class*="option"]'));
		const visible = all.filter(el => el.offsetParent !== null);
		const exact = visible.filter(el => (el.textContent || '').trim() === 'ETH');
		if (exact.length >= 2) { exact[1].click(); return (exact[1].textContent || '').trim(); }
		if (exact.length === 1) { exact[0].click(); return (exact[0].textContent || '').trim(); }
		const fuzzy = visible.filter(el => (el.textContent || '').toUpperCase().includes('ETH'));
		if (fuzzy.length >= 2) { fuzzy[1].click(); return (fuzzy[1].textContent || '').trim(); }
		if (fuzzy.length === 1) { fuzzy[0].click(); return (fuzzy[0].textContent || '').trim(); }
		return '';
	})()`, &pickedOption)); err != nil || strings.TrimSpace(pickedOption) == "" {
		session.close()
		return nil, errors.New("coinglass ETH option not found")
	}
	log.Printf("webdatasource found symbol option: %q", pickedOption)

	progress.setAction("查找 EChart canvas")
	js = fmt.Sprintf(`(() => {
		const all = document.querySelectorAll('canvas');
		const cv = all[%d];
		return !!cv;
	})()`, session.canvasIdx)
	if err := chromedp.Run(session.taskCtx, chromedp.Evaluate(js, &ok)); err != nil || !ok {
		session.close()
		return nil, errors.New("coinglass chart canvas not found")
	}
	return session, nil
}

*/

func (m *WebDataSourceManager) newCaptureSession(ctx context.Context, chromePath, profileDir string, progress *webDataSourceProgress) (*webDataSourceSession, error) {
	if err := os.MkdirAll(profileDir, 0o755); err != nil {
		return nil, err
	}
	opts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.ExecPath(chromePath),
		chromedp.Flag("headless", false),
		chromedp.UserAgent("Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36"),
		chromedp.UserDataDir(profileDir),
		chromedp.Flag("disable-blink-features", "AutomationControlled"),
		chromedp.WindowSize(1440, 900),
	)
	allocCtx, cancelAlloc := chromedp.NewExecAllocator(ctx, opts...)
	taskCtx, cancelTask := chromedp.NewContext(allocCtx)
	session := &webDataSourceSession{
		taskCtx: taskCtx,
		cancel: func() {
			cancelTask()
			cancelAlloc()
		},
	}

	if err := m.runLoggedStep(session.taskCtx, progress, "访问 https://www.coinglass.com/zh/pro/futures/LiquidationMap", func() (string, error) {
		err := chromedp.Run(session.taskCtx,
			chromedp.ActionFunc(func(ctx context.Context) error {
				_, err := page.AddScriptToEvaluateOnNewDocument(webDataSourceHookJS).Do(ctx)
				return err
			}),
			chromedp.ActionFunc(func(ctx context.Context) error {
				_, _, _, err := page.Navigate("https://www.coinglass.com/zh/pro/futures/LiquidationMap").Do(ctx)
				return err
			}),
		)
		return "页面已打开", err
	}); err != nil {
		session.close()
		return nil, err
	}

	if err := m.runLoggedStep(session.taskCtx, progress, "注入页面数据抓取钩子", func() (string, error) {
		err := chromedp.Run(session.taskCtx, chromedp.Evaluate(webDataSourceHookJS, nil))
		return "hook 已注入", err
	}); err != nil {
		session.close()
		return nil, err
	}

	var sectionInfo struct {
		TitleText    string `json:"titleText"`
		InputValue   string `json:"inputValue"`
		Placeholder  string `json:"placeholder"`
		PeriodText   string `json:"periodText"`
		CanvasDomID  string `json:"canvasDomId"`
		CanvasWidth  int    `json:"canvasWidth"`
		CanvasHeight int    `json:"canvasHeight"`
	}
	if err := m.runLoggedStep(session.taskCtx, progress, "找到 h1 比特币交易所清算地图", func() (string, error) {
		if err := chromedp.Run(session.taskCtx, chromedp.Evaluate(`(() => {
			const title = Array.from(document.querySelectorAll('h1.MuiTypography-root, h1')).find(h => {
				const text = (h.textContent || '').trim();
				return text === '比特币交易所清算地图' || (text.includes('比特币交易所清算地图') && !text.includes('Hyperliquid'));
			});
			if (!title) return null;
			let container = title.parentElement;
			for (let i = 0; i < 12 && container; i++) {
				const input = container.querySelector('input.MuiAutocomplete-input[role="combobox"]');
				const periodBtn = container.querySelector('button.MuiSelect-button');
				const canvas = container.querySelector('canvas');
				if (input && periodBtn && canvas) {
					return {
						titleText: (title.textContent || '').trim(),
						inputValue: input.value || '',
						placeholder: input.getAttribute('placeholder') || '',
						periodText: (periodBtn.textContent || '').trim(),
						canvasDomId: canvas.getAttribute('data-zr-dom-id') || '',
						canvasWidth: Number(canvas.width || 0),
						canvasHeight: Number(canvas.height || 0)
					};
				}
				container = container.parentElement;
			}
			return null;
		})()`, &sectionInfo)); err != nil {
			return "", err
		}
		if strings.TrimSpace(sectionInfo.TitleText) == "" {
			return "", errors.New("coinglass h1 not found")
		}
		return fmt.Sprintf("title=%q", sectionInfo.TitleText), nil
	}); err != nil {
		session.close()
		return nil, err
	}

	var searchInputInfo struct {
		Found       bool   `json:"found"`
		ID          string `json:"id"`
		Value       string `json:"value"`
		Placeholder string `json:"placeholder"`
	}
	if err := m.runLoggedStep(session.taskCtx, progress, "找到 BTC 搜索输入框", func() (string, error) {
		if err := chromedp.Run(session.taskCtx, chromedp.Evaluate(`(() => {
			const title = Array.from(document.querySelectorAll('h1.MuiTypography-root, h1')).find(h => {
				const text = (h.textContent || '').trim();
				return text === '比特币交易所清算地图' || (text.includes('比特币交易所清算地图') && !text.includes('Hyperliquid'));
			});
			if (!title) return {found:false, id:'', value:'', placeholder:''};
			let container = title.parentElement;
			for (let i = 0; i < 12 && container; i++) {
				const input = container.querySelector('input.MuiAutocomplete-input[role="combobox"]');
				if (input) {
					return {
						found: (input.value || '').trim() === 'BTC',
						id: input.id || '',
						value: input.value || '',
						placeholder: input.getAttribute('placeholder') || ''
					};
				}
				container = container.parentElement;
			}
			return {found:false, id:'', value:'', placeholder:''};
		})()`, &searchInputInfo)); err != nil {
			return "", err
		}
		if !searchInputInfo.Found {
			return "", errors.New("coinglass BTC search input not found")
		}
		return fmt.Sprintf("id=%q value=%q placeholder=%q", searchInputInfo.ID, searchInputInfo.Value, searchInputInfo.Placeholder), nil
	}); err != nil {
		session.close()
		return nil, err
	}

	if err := m.runLoggedStep(session.taskCtx, progress, "打开 BTC 搜索下拉框", func() (string, error) {
		var ok bool
		if err := chromedp.Run(session.taskCtx, chromedp.Evaluate(`(() => {
			const title = Array.from(document.querySelectorAll('h1.MuiTypography-root, h1')).find(h => {
				const text = (h.textContent || '').trim();
				return text === '比特币交易所清算地图' || (text.includes('比特币交易所清算地图') && !text.includes('Hyperliquid'));
			});
			if (!title) return false;
			let container = title.parentElement;
			for (let i = 0; i < 12 && container; i++) {
				const input = container.querySelector('input.MuiAutocomplete-input[role="combobox"]');
				const root = input ? input.closest('.MuiAutocomplete-root') : null;
				const btn = root ? root.querySelector('button.MuiAutocomplete-popupIndicator, button[aria-label="Open"], button[title="Open"], button') : null;
				if (input && btn) {
					input.focus();
					for (const type of ['pointerdown','mousedown','mouseup','click']) {
						btn.dispatchEvent(new MouseEvent(type, {bubbles:true, cancelable:true, view:window}));
					}
					return true;
				}
				container = container.parentElement;
			}
			return false;
		})()`, &ok)); err != nil {
			return "", err
		}
		if !ok {
			return "", errors.New("coinglass symbol selector not found")
		}
		return "BTC 下拉框已打开", nil
	}); err != nil {
		session.close()
		return nil, err
	}

	if err := m.runLoggedStep(session.taskCtx, progress, "选择 ETH", func() (string, error) {
		var pickedOption string
		if err := chromedp.Run(session.taskCtx,
			chromedp.Evaluate(`(() => {
				const title = Array.from(document.querySelectorAll('h1.MuiTypography-root, h1')).find(h => {
					const text = (h.textContent || '').trim();
					return text === '比特币交易所清算地图' || (text.includes('比特币交易所清算地图') && !text.includes('Hyperliquid'));
				});
				if (!title) return false;
				let container = title.parentElement;
				for (let i = 0; i < 12 && container; i++) {
					const input = container.querySelector('input.MuiAutocomplete-input[role="combobox"]');
					if (input) {
						input.focus();
						return true;
					}
					container = container.parentElement;
				}
				return false;
			})()`, nil),
			chromedp.Sleep(500*time.Millisecond),
			chromedp.Evaluate(`(() => {
				const all = Array.from(document.querySelectorAll('[role="option"], [role="menuitem"], .MuiOption-root, .MuiMenuItem-root, li[class*="Option"], li[class*="option"]'));
				const visible = all.filter(el => el.offsetParent !== null);
				const norm = s => String(s || '').trim().toUpperCase();
				const btcIdx = visible.findIndex(el => norm(el.textContent) === 'BTC');
				if (btcIdx >= 0 && visible[btcIdx + 1] && norm(visible[btcIdx + 1].textContent) === 'ETH') {
					visible[btcIdx + 1].click();
					return (visible[btcIdx + 1].textContent || '').trim();
				}
				const exact = visible.find(el => norm(el.textContent) === 'ETH');
				if (exact) {
					exact.click();
					return (exact.textContent || '').trim();
				}
				return '';
			})()`, &pickedOption),
		); err != nil {
			return "", err
		}
		if strings.TrimSpace(pickedOption) == "" {
			return "", errors.New("coinglass ETH option not found")
		}
		if strings.ToUpper(strings.TrimSpace(pickedOption)) != "ETH" {
			return "", fmt.Errorf("coinglass selected unexpected symbol %q", pickedOption)
		}
		return fmt.Sprintf("宸查€夋嫨 %q", pickedOption), nil
		var finalValue string
		if err := chromedp.Run(session.taskCtx, chromedp.Evaluate(`(() => {
			const title = Array.from(document.querySelectorAll('h1.MuiTypography-root, h1')).find(h => {
				const text = (h.textContent || '').trim();
				return text === '比特币交易所清算地图' || (text.includes('比特币交易所清算地图') && !text.includes('Hyperliquid'));
			});
			if (!title) return '';
			let container = title.parentElement;
			for (let i = 0; i < 12 && container; i++) {
				const input = container.querySelector('input.MuiAutocomplete-input[role="combobox"]');
				if (input) return (input.value || '').trim();
				container = container.parentElement;
			}
			return '';
		})()`, &finalValue)); err != nil {
			return "", err
		}
		if finalValue != "ETH" {
			return "", fmt.Errorf("coinglass selected unexpected symbol %q", finalValue)
		}
		return fmt.Sprintf("已选择 %q", pickedOption), nil
	}); err != nil {
		session.close()
		return nil, err
	}

	if err := m.runLoggedStep(session.taskCtx, progress, "找到清算地图 canvas", func() (string, error) {
		var periodInfo struct {
			Text      string `json:"text"`
			ClassName string `json:"className"`
		}
		if err := chromedp.Run(session.taskCtx, chromedp.Evaluate(`(() => {
			const title = Array.from(document.querySelectorAll('h1.MuiTypography-root, h1')).find(h => {
				const text = (h.textContent || '').trim();
				return text === '比特币交易所清算地图' || (text.includes('比特币交易所清算地图') && !text.includes('Hyperliquid'));
			});
			if (!title) return null;
			let container = title.parentElement;
			for (let i = 0; i < 12 && container; i++) {
				const roots = Array.from(container.querySelectorAll('div.MuiSelect-root'));
				const periodRoot = roots.find(root => {
					const btn = root.querySelector('button.MuiSelect-button[role="combobox"]');
					return text === '1天' || text === '1D' || text === '1日';
				});
				if (periodBtn) {
					return {
						text: (periodBtn.textContent || '').trim(),
						className: periodBtn.className || ''
					};
				}
				container = container.parentElement;
			}
			return null;
		})()`, &periodInfo)); err != nil {
			return "", err
		}
		if strings.TrimSpace(periodInfo.Text) == "" {
			return "", errors.New("coinglass period button not found")
		}
		return fmt.Sprintf("text=%q class=%q", periodInfo.Text, periodInfo.ClassName), nil
	}); err != nil {
		session.close()
		return nil, err
	}

	return session, nil
}

func (s *webDataSourceSession) close() {
	if s != nil && s.cancel != nil {
		s.closeOnce.Do(s.cancel)
	}
}

func (m *WebDataSourceManager) newCaptureSessionV2(ctx context.Context, chromePath, profileDir string, progress *webDataSourceProgress) (*webDataSourceSession, error) {
	if err := os.MkdirAll(profileDir, 0o755); err != nil {
		return nil, err
	}
	opts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.ExecPath(chromePath),
		chromedp.Flag("headless", false),
		chromedp.UserAgent("Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36"),
		chromedp.UserDataDir(profileDir),
		chromedp.Flag("disable-blink-features", "AutomationControlled"),
		chromedp.WindowSize(1440, 900),
	)
	allocCtx, cancelAlloc := chromedp.NewExecAllocator(ctx, opts...)
	taskCtx, cancelTask := chromedp.NewContext(allocCtx)
	session := &webDataSourceSession{
		taskCtx: taskCtx,
		cancel: func() {
			cancelTask()
			cancelAlloc()
		},
	}

	findTargetTitleJS := `(() => {
		const isTargetText = text => {
			const value = String(text || '').replace(/\s+/g, '').trim();
			return value.includes('比特币交易所清算地图') && !value.includes('Hyperliquid');
		};
		const selectors = [
			'h1.MuiTypography-root',
			'h1',
			'[class*="MuiTypography-h1"]',
			'[class*="MuiTypography"]',
			'div',
			'span',
		];
		for (const selector of selectors) {
			const hit = Array.from(document.querySelectorAll(selector)).find(node => isTargetText(node.textContent || ''));
			if (hit) return hit;
		}
		return null;
	})()`

	if err := m.runLoggedStep(session.taskCtx, progress, "访问 https://www.coinglass.com/zh/pro/futures/LiquidationMap", func() (string, error) {
		err := chromedp.Run(session.taskCtx,
			chromedp.ActionFunc(func(ctx context.Context) error {
				_, err := page.AddScriptToEvaluateOnNewDocument(webDataSourceHookJS).Do(ctx)
				return err
			}),
			chromedp.ActionFunc(func(ctx context.Context) error {
				_, _, _, err := page.Navigate("https://www.coinglass.com/zh/pro/futures/LiquidationMap").Do(ctx)
				return err
			}),
		)
		return "页面已打开", err
	}); err != nil {
		session.close()
		return nil, err
	}

	if err := m.runLoggedStep(session.taskCtx, progress, "注入页面数据抓取钩子", func() (string, error) {
		err := chromedp.Run(session.taskCtx, chromedp.Evaluate(webDataSourceHookJS, nil))
		return "hook 已注入", err
	}); err != nil {
		session.close()
		return nil, err
	}

	if err := m.runLoggedStep(session.taskCtx, progress, "找到 h1 比特币交易所清算地图", func() (string, error) {
		var titleText string
		err := chromedp.Run(session.taskCtx, chromedp.Evaluate(`(() => {
			const title = `+findTargetTitleJS+`;
			return title ? String(title.textContent || '').trim() : '';
		})()`, &titleText))
		if err != nil {
			return "", err
		}
		if strings.TrimSpace(titleText) == "" {
			return "", errors.New("coinglass h1 not found")
		}
		return fmt.Sprintf("title=%q", titleText), nil
	}); err != nil {
		session.close()
		return nil, err
	}

	if err := m.runLoggedStep(session.taskCtx, progress, "找到 BTC 搜索输入框", func() (string, error) {
		var inputInfo struct {
			ID    string `json:"id"`
			Value string `json:"value"`
		}
		err := chromedp.Run(session.taskCtx, chromedp.Evaluate(`(() => {
			const panel = `+webDataSourceFindTargetPanelJS()+`;
			const input = panel ? panel.querySelector('input.MuiAutocomplete-input[role="combobox"]') : null;
			return {
				id: input ? (input.id || '') : '',
				value: input ? String(input.value || '').trim() : ''
			};
		})()`, &inputInfo))
		if err != nil {
			return "", err
		}
		if inputInfo.Value != "BTC" {
			return "", errors.New("coinglass BTC search input not found")
		}
		return fmt.Sprintf("id=%q value=%q", inputInfo.ID, inputInfo.Value), nil
	}); err != nil {
		session.close()
		return nil, err
	}

	if err := m.runLoggedStep(session.taskCtx, progress, "打开 BTC 搜索下拉框", func() (string, error) {
		var ok bool
		err := chromedp.Run(session.taskCtx, chromedp.Evaluate(`(() => {
			const title = `+findTargetTitleJS+`;
			if (!title) return false;
			let container = title.parentElement;
			for (let i = 0; i < 12 && container; i++) {
				const input = container.querySelector('input.MuiAutocomplete-input[role="combobox"]');
				const root = input ? input.closest('.MuiAutocomplete-root') : null;
				const btn = root ? root.querySelector('button.MuiAutocomplete-popupIndicator, button[aria-label="Open"], button[title="Open"], button') : null;
				if (input && btn) {
					input.focus();
					for (const type of ['pointerdown','mousedown','mouseup','click']) {
						btn.dispatchEvent(new MouseEvent(type, {bubbles:true, cancelable:true, view:window}));
					}
					return true;
				}
				container = container.parentElement;
			}
			return false;
		})()`, &ok))
		if err != nil {
			return "", err
		}
		if !ok {
			return "", errors.New("coinglass symbol selector not found")
		}
		return "BTC 下拉框已打开", nil
	}); err != nil {
		session.close()
		return nil, err
	}

	if err := m.runLoggedStep(session.taskCtx, progress, "选择 ETH", func() (string, error) {
		var picked struct {
			Text       string `json:"text"`
			InputValue string `json:"inputValue"`
		}
		err := chromedp.Run(session.taskCtx,
			chromedp.Evaluate(`(() => {
				const panel = `+webDataSourceFindTargetPanelJS()+`;
				const input = panel ? panel.querySelector('input.MuiAutocomplete-input[role="combobox"]') : null;
				if (!input) return false;
				input.focus();
				return true;
			})()`, nil),
			chromedp.Sleep(500*time.Millisecond),
			chromedp.Evaluate(`(() => {
				const all = Array.from(document.querySelectorAll('[role="option"], [role="menuitem"], .MuiOption-root, .MuiMenuItem-root, li[class*="Option"], li[class*="option"]'));
				const visible = all.filter(el => el.offsetParent !== null);
				const norm = s => String(s || '').trim().toUpperCase();
				const btcIdx = visible.findIndex(el => norm(el.textContent) === 'BTC');
				if (btcIdx >= 0 && visible[btcIdx + 1] && norm(visible[btcIdx + 1].textContent) === 'ETH') {
					visible[btcIdx + 1].click();
					return String(visible[btcIdx + 1].textContent || '').trim();
				}
				const exact = visible.find(el => norm(el.textContent) === 'ETH');
				if (exact) {
					exact.click();
					return String(exact.textContent || '').trim();
				}
				return '';
			})()`, &picked),
			chromedp.Sleep(500*time.Millisecond),
			chromedp.Evaluate(`(() => {
				const panel = `+webDataSourceFindTargetPanelJS()+`;
				const input = panel ? panel.querySelector('input.MuiAutocomplete-input[role="combobox"]') : null;
				return {
					text: '',
					inputValue: input ? String(input.value || '').trim() : ''
				};
			})()`, &picked),
		)
		if err != nil {
			return "", err
		}
		if strings.ToUpper(strings.TrimSpace(picked.InputValue)) != "ETH" {
			return "", fmt.Errorf("coinglass selected unexpected symbol %q", picked.InputValue)
		}
		if strings.TrimSpace(picked.Text) == "" {
			picked.Text = picked.InputValue
		}
		return fmt.Sprintf("selected %q", picked.Text), nil
	}); err != nil {
		session.close()
		return nil, err
	}

	if err := m.runLoggedStep(session.taskCtx, progress, "找到 1天 周期下拉框", func() (string, error) {
		var periodInfo struct {
			Text      string `json:"text"`
			ClassName string `json:"className"`
			Value     string `json:"value"`
		}
		err := chromedp.Run(session.taskCtx, chromedp.Evaluate(`(() => {
			const title = `+findTargetTitleJS+`;
			if (!title) return {text:'', className:'', value:''};
			let container = title.parentElement;
			for (let i = 0; i < 12 && container; i++) {
				const roots = Array.from(container.querySelectorAll('div.MuiSelect-root'));
				const hit = roots.find(root => {
					const btn = root.querySelector('button.MuiSelect-button[role="combobox"]');
					const hidden = root.querySelector('input[aria-hidden="true"]');
					if (!btn || !hidden) return false;
					const text = String(btn.textContent || '').trim();
					const value = String(hidden.value || hidden.getAttribute('value') || '').trim();
					return (text === '1天' || text === '1D' || text === '1日') && value === '1';
				});
				if (hit) {
					const btn = hit.querySelector('button.MuiSelect-button[role="combobox"]');
					const hidden = hit.querySelector('input[aria-hidden="true"]');
					return {
						text: btn ? String(btn.textContent || '').trim() : '',
						className: btn ? (btn.className || '') : '',
						value: hidden ? String(hidden.value || hidden.getAttribute('value') || '').trim() : ''
					};
				}
				container = container.parentElement;
			}
			return {text:'', className:'', value:''};
		})()`, &periodInfo))
		if err != nil {
			return "", err
		}
		if strings.TrimSpace(periodInfo.Text) == "" || periodInfo.Value != "1" {
			return "", errors.New("coinglass period selector not found")
		}
		return fmt.Sprintf("text=%q value=%q class=%q", periodInfo.Text, periodInfo.Value, periodInfo.ClassName), nil
	}); err != nil {
		session.close()
		return nil, err
	}

	return session, nil
}

func (m *WebDataSourceManager) newCaptureSessionV3(ctx context.Context, chromePath, profileDir string, progress *webDataSourceProgress) (*webDataSourceSession, error) {
	if err := os.MkdirAll(profileDir, 0o755); err != nil {
		return nil, err
	}
	opts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.ExecPath(chromePath),
		chromedp.Flag("headless", false),
		chromedp.UserAgent("Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36"),
		chromedp.UserDataDir(profileDir),
		chromedp.Flag("disable-blink-features", "AutomationControlled"),
		chromedp.WindowSize(1440, 900),
	)
	allocCtx, cancelAlloc := chromedp.NewExecAllocator(ctx, opts...)
	taskCtx, cancelTask := chromedp.NewContext(allocCtx)
	session := &webDataSourceSession{
		taskCtx: taskCtx,
		cancel: func() {
			cancelTask()
			cancelAlloc()
		},
	}

	findTargetTitleJS := `(() => {
		const headings = Array.from(document.querySelectorAll('h1.MuiTypography-root, h1'));
		return headings.find(h => {
			const text = String(h.textContent || '').trim();
			return text === '比特币交易所清算地图' || (text.includes('比特币交易所清算地图') && !text.includes('Hyperliquid'));
		}) || null;
	})()`

	if err := m.runLoggedStep(session.taskCtx, progress, "访问 https://www.coinglass.com/zh/pro/futures/LiquidationMap", func() (string, error) {
		err := chromedp.Run(session.taskCtx,
			chromedp.ActionFunc(func(ctx context.Context) error {
				_, err := page.AddScriptToEvaluateOnNewDocument(webDataSourceHookJS).Do(ctx)
				return err
			}),
			chromedp.ActionFunc(func(ctx context.Context) error {
				_, _, _, err := page.Navigate("https://www.coinglass.com/zh/pro/futures/LiquidationMap").Do(ctx)
				return err
			}),
		)
		return "页面已打开", err
	}); err != nil {
		session.close()
		return nil, err
	}

	if err := m.runLoggedStep(session.taskCtx, progress, "注入页面数据抓取钩子", func() (string, error) {
		err := chromedp.Run(session.taskCtx, chromedp.Evaluate(webDataSourceHookJS, nil))
		return "hook 已注入", err
	}); err != nil {
		session.close()
		return nil, err
	}

	if err := m.runLoggedStep(session.taskCtx, progress, "找到 h1 比特币交易所清算地图", func() (string, error) {
		var titleText string
		err := chromedp.Run(session.taskCtx, chromedp.Evaluate(`(() => {
			const title = `+findTargetTitleJS+`;
			return title ? String(title.textContent || '').trim() : '';
		})()`, &titleText))
		if err != nil {
			return "", err
		}
		if strings.TrimSpace(titleText) == "" {
			return "", errors.New("coinglass h1 not found")
		}
		return fmt.Sprintf("title=%q", titleText), nil
	}); err != nil {
		session.close()
		return nil, err
	}

	if err := m.runLoggedStep(session.taskCtx, progress, "找到 BTC 搜索输入框", func() (string, error) {
		var inputInfo struct {
			ID    string `json:"id"`
			Value string `json:"value"`
		}
		err := chromedp.Run(session.taskCtx, chromedp.Evaluate(`(() => {
			const title = `+findTargetTitleJS+`;
			if (!title) return {id:'', value:''};
			let container = title.parentElement;
			for (let i = 0; i < 12 && container; i++) {
				const input = container.querySelector('input.MuiAutocomplete-input[role="combobox"]');
				if (input) return {id: input.id || '', value: String(input.value || '').trim()};
				container = container.parentElement;
			}
			return {id:'', value:''};
		})()`, &inputInfo))
		if err != nil {
			return "", err
		}
		if inputInfo.Value != "BTC" {
			return "", errors.New("coinglass BTC search input not found")
		}
		return fmt.Sprintf("id=%q value=%q", inputInfo.ID, inputInfo.Value), nil
	}); err != nil {
		session.close()
		return nil, err
	}

	if err := m.runLoggedStep(session.taskCtx, progress, "打开 BTC 搜索下拉框", func() (string, error) {
		var ok bool
		err := chromedp.Run(session.taskCtx, chromedp.Evaluate(`(() => {
			const title = `+findTargetTitleJS+`;
			if (!title) return false;
			let container = title.parentElement;
			for (let i = 0; i < 12 && container; i++) {
				const input = container.querySelector('input.MuiAutocomplete-input[role="combobox"]');
				const root = input ? input.closest('.MuiAutocomplete-root') : null;
				const btn = root ? root.querySelector('button.MuiAutocomplete-popupIndicator, button[aria-label="Open"], button[title="Open"], button') : null;
				if (input && btn) {
					input.focus();
					for (const type of ['pointerdown','mousedown','mouseup','click']) {
						btn.dispatchEvent(new MouseEvent(type, {bubbles:true, cancelable:true, view:window}));
					}
					return true;
				}
				container = container.parentElement;
			}
			return false;
		})()`, &ok))
		if err != nil {
			return "", err
		}
		if !ok {
			return "", errors.New("coinglass symbol selector not found")
		}
		return "BTC 下拉框已打开", nil
	}); err != nil {
		session.close()
		return nil, err
	}

	if err := m.runLoggedStep(session.taskCtx, progress, "选择 ETH", func() (string, error) {
		var picked string
		err := chromedp.Run(session.taskCtx,
			chromedp.Evaluate(`(() => {
				const title = `+findTargetTitleJS+`;
				if (!title) return false;
				let container = title.parentElement;
				for (let i = 0; i < 12 && container; i++) {
					const input = container.querySelector('input.MuiAutocomplete-input[role="combobox"]');
					if (input) { input.focus(); return true; }
					container = container.parentElement;
				}
				return false;
			})()`, nil),
			chromedp.Sleep(500*time.Millisecond),
			chromedp.Evaluate(`(() => {
				const all = Array.from(document.querySelectorAll('[role="option"], [role="menuitem"], .MuiOption-root, .MuiMenuItem-root, li[class*="Option"], li[class*="option"]'));
				const visible = all.filter(el => el.offsetParent !== null);
				const norm = s => String(s || '').trim().toUpperCase();
				const btcIdx = visible.findIndex(el => norm(el.textContent) === 'BTC');
				if (btcIdx >= 0 && visible[btcIdx + 1] && norm(visible[btcIdx + 1].textContent) === 'ETH') {
					visible[btcIdx + 1].click();
					return String(visible[btcIdx + 1].textContent || '').trim();
				}
				const exact = visible.find(el => norm(el.textContent) === 'ETH');
				if (exact) {
					exact.click();
					return String(exact.textContent || '').trim();
				}
				return '';
			})()`, &picked),
		)
		if err != nil {
			return "", err
		}
		if strings.ToUpper(strings.TrimSpace(picked)) != "ETH" {
			return "", fmt.Errorf("coinglass selected unexpected symbol %q", picked)
		}
		return fmt.Sprintf("已选择 %q", picked), nil
	}); err != nil {
		session.close()
		return nil, err
	}

	if err := m.runLoggedStep(session.taskCtx, progress, "找到 1天 周期下拉框", func() (string, error) {
		var periodInfo struct {
			Text      string `json:"text"`
			ClassName string `json:"className"`
			Value     string `json:"value"`
		}
		err := chromedp.Run(session.taskCtx, chromedp.Evaluate(`(() => {
			const title = `+findTargetTitleJS+`;
			if (!title) return {text:'', className:'', value:''};
			let container = title.parentElement;
			for (let i = 0; i < 12 && container; i++) {
				const roots = Array.from(container.querySelectorAll('div.MuiSelect-root'));
				const hit = roots.find(root => {
					const btn = root.querySelector('button.MuiSelect-button[role="combobox"]');
					const hidden = root.querySelector('input[aria-hidden="true"]');
					if (!btn || !hidden) return false;
					const text = String(btn.textContent || '').trim();
					const value = String(hidden.value || hidden.getAttribute('value') || '').trim();
					return (text === '1天' || text === '1D' || text === '1日') && value === '1';
				});
				if (!hit) return {text:'', className:'', value:''};
				const btn = hit.querySelector('button.MuiSelect-button[role="combobox"]');
				const hidden = hit.querySelector('input[aria-hidden="true"]');
				return {
					text: btn ? String(btn.textContent || '').trim() : '',
					className: btn ? (btn.className || '') : '',
					value: hidden ? String(hidden.value || hidden.getAttribute('value') || '').trim() : ''
				};
			}
			return {text:'', className:'', value:''};
		})()`, &periodInfo))
		if err != nil {
			return "", err
		}
		if strings.TrimSpace(periodInfo.Text) == "" || periodInfo.Value != "1" {
			return "", errors.New("coinglass period selector not found")
		}
		return fmt.Sprintf("text=%q value=%q class=%q", periodInfo.Text, periodInfo.Value, periodInfo.ClassName), nil
	}); err != nil {
		session.close()
		return nil, err
	}

	return session, nil
}

/*
func (m *WebDataSourceManager) newCaptureSessionV4(ctx context.Context, chromePath, profileDir string, progress *webDataSourceProgress) (*webDataSourceSession, error) {
	if err := os.MkdirAll(profileDir, 0o755); err != nil {
		return nil, err
	}
	opts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.ExecPath(chromePath),
		chromedp.Flag("headless", false),
		chromedp.UserAgent("Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36"),
		chromedp.UserDataDir(profileDir),
		chromedp.Flag("disable-blink-features", "AutomationControlled"),
		chromedp.WindowSize(1440, 900),
	)
	allocCtx, cancelAlloc := chromedp.NewExecAllocator(ctx, opts...)
	taskCtx, cancelTask := chromedp.NewContext(allocCtx)
	session := &webDataSourceSession{
		taskCtx: taskCtx,
		cancel: func() {
			cancelTask()
			cancelAlloc()
		},
	}

	findTargetTitleJS := `(() => {
		const headings = Array.from(document.querySelectorAll('h1.MuiTypography-root, h1'));
		return headings.find(h => {
			const text = String(h.textContent || '').trim();
			return text === '比特币交易所清算地图' || (text.includes('比特币交易所清算地图') && !text.includes('Hyperliquid'));
		}) || null;
	})()`

	if err := m.runLoggedStep(session.taskCtx, progress, "访问 https://www.coinglass.com/zh/pro/futures/LiquidationMap", func() (string, error) {
		err := chromedp.Run(session.taskCtx,
			chromedp.ActionFunc(func(ctx context.Context) error {
				_, err := page.AddScriptToEvaluateOnNewDocument(webDataSourceHookJS).Do(ctx)
				return err
			}),
			chromedp.ActionFunc(func(ctx context.Context) error {
				_, _, _, err := page.Navigate("https://www.coinglass.com/zh/pro/futures/LiquidationMap").Do(ctx)
				return err
			}),
		)
		return "页面已打开", err
	}); err != nil {
		session.close()
		return nil, err
	}

	if err := m.runLoggedStep(session.taskCtx, progress, "注入页面数据抓取钩子", func() (string, error) {
		err := chromedp.Run(session.taskCtx, chromedp.Evaluate(webDataSourceHookJS, nil))
		return "hook 已注入", err
	}); err != nil {
		session.close()
		return nil, err
	}

	if err := m.runLoggedStep(session.taskCtx, progress, "找到 BTC 搜索输入框", func() (string, error) {
		var inputInfo struct {
			ID    string `json:"id"`
			Value string `json:"value"`
		}
		err := chromedp.Run(session.taskCtx, chromedp.Evaluate(`(() => {
			const title = `+findTargetTitleJS+`;
			if (!title) return {id:'', value:''};
			let container = title.parentElement;
			for (let i = 0; i < 12 && container; i++) {
				const input = container.querySelector('input.MuiAutocomplete-input[role="combobox"]');
				if (input) return {id: input.id || '', value: String(input.value || '').trim()};
				container = container.parentElement;
			}
			return {id:'', value:''};
		})()`, &inputInfo))
		if err != nil {
			return "", err
		}
		if inputInfo.Value != "BTC" {
			return "", errors.New("coinglass BTC search input not found")
		}
		return fmt.Sprintf("id=%q value=%q", inputInfo.ID, inputInfo.Value), nil
	}); err != nil {
		session.close()
		return nil, err
	}

	if err := m.runLoggedStep(session.taskCtx, progress, "打开 BTC 搜索下拉框", func() (string, error) {
		var ok bool
		err := chromedp.Run(session.taskCtx, chromedp.Evaluate(`(() => {
			const title = `+findTargetTitleJS+`;
			if (!title) return false;
			let container = title.parentElement;
			for (let i = 0; i < 12 && container; i++) {
				const input = container.querySelector('input.MuiAutocomplete-input[role="combobox"]');
				const root = input ? input.closest('.MuiAutocomplete-root') : null;
				const btn = root ? root.querySelector('button.MuiAutocomplete-popupIndicator, button[aria-label="Open"], button[title="Open"], button') : null;
				if (input && btn) {
					input.focus();
					for (const type of ['pointerdown','mousedown','mouseup','click']) {
						btn.dispatchEvent(new MouseEvent(type, {bubbles:true, cancelable:true, view:window}));
					}
					return true;
				}
				container = container.parentElement;
			}
			return false;
		})()`, &ok))
		if err != nil {
			return "", err
		}
		if !ok {
			return "", errors.New("coinglass symbol selector not found")
		}
		return "BTC 下拉框已打开", nil
	}); err != nil {
		session.close()
		return nil, err
	}

	if err := m.runLoggedStep(session.taskCtx, progress, "选择 ETH", func() (string, error) {
		var picked string
		err := chromedp.Run(session.taskCtx,
			chromedp.Evaluate(`(() => {
				const title = `+findTargetTitleJS+`;
				if (!title) return false;
				let container = title.parentElement;
				for (let i = 0; i < 12 && container; i++) {
					const input = container.querySelector('input.MuiAutocomplete-input[role="combobox"]');
					if (input) { input.focus(); return true; }
					container = container.parentElement;
				}
				return false;
			})()`, nil),
			chromedp.Sleep(500*time.Millisecond),
			chromedp.Evaluate(`(() => {
				const all = Array.from(document.querySelectorAll('[role="option"], [role="menuitem"], .MuiOption-root, .MuiMenuItem-root, li[class*="Option"], li[class*="option"]'));
				const visible = all.filter(el => el.offsetParent !== null);
				const norm = s => String(s || '').trim().toUpperCase();
				const btcIdx = visible.findIndex(el => norm(el.textContent) === 'BTC');
				if (btcIdx >= 0 && visible[btcIdx + 1] && norm(visible[btcIdx + 1].textContent) === 'ETH') {
					visible[btcIdx + 1].click();
					return String(visible[btcIdx + 1].textContent || '').trim();
				}
				const exact = visible.find(el => norm(el.textContent) === 'ETH');
				if (exact) {
					exact.click();
					return String(exact.textContent || '').trim();
				}
				return '';
			})()`, &picked),
		)
		if err != nil {
			return "", err
		}
		if strings.ToUpper(strings.TrimSpace(picked)) != "ETH" {
			return "", fmt.Errorf("coinglass selected unexpected symbol %q", picked)
		}
		return fmt.Sprintf("已选择 %q", picked), nil
	}); err != nil {
		session.close()
		return nil, err
	}

	if err := m.runLoggedStep(session.taskCtx, progress, "抓取 zr_0 canvas", func() (string, error) {
	if err := m.runLoggedStep(session.taskCtx, progress, "选择 1天", func() (string, error) {
		var picked string
		err := chromedp.Run(session.taskCtx,
			chromedp.Evaluate(`(() => {
				const headings = Array.from(document.querySelectorAll('h1.MuiTypography-root, h1'));
				const title = headings.find(h => {
					const text = String(h.textContent || '').trim();
					return text === '比特币交易所清算地图' || (text.includes('比特币交易所清算地图') && !text.includes('Hyperliquid'));
				});
				if (!title) return false;
				let container = title.parentElement;
				for (let i = 0; i < 12 && container; i++) {
					const roots = Array.from(container.querySelectorAll('div.MuiSelect-root'));
					const hit = roots.find(root => {
						const btn = root.querySelector('button.MuiSelect-button[role="combobox"]');
						const hidden = root.querySelector('input[aria-hidden="true"]');
						if (!btn || !hidden) return false;
						const text = String(btn.textContent || '').trim();
						const value = String(hidden.value || hidden.getAttribute('value') || '').trim();
						const validText = ['1天', '7天', '30天', '1D', '7D', '30D', '1日', '7日', '30日'].includes(text);
						const validValue = ['1', '7', '30'].includes(value);
						return validText && validValue;
					});
					const btn = hit ? hit.querySelector('button.MuiSelect-button[role="combobox"]') : null;
					if (btn) {
						for (const type of ['pointerdown', 'mousedown', 'mouseup', 'click']) {
							btn.dispatchEvent(new MouseEvent(type, {bubbles:true, cancelable:true, view:window}));
						}
						return true;
					}
					container = container.parentElement;
				}
				return false;
			})()`, nil),
			chromedp.Sleep(300*time.Millisecond),
			chromedp.Evaluate(`(() => {
				const optionSel = '[role="option"], [role="menuitem"], .MuiOption-root, .MuiMenuItem-root, li[class*="Option"], li[class*="option"], li[class*="MenuItem"], [data-value]';
				const all = Array.from(document.querySelectorAll(optionSel)).filter(el => el.offsetParent !== null);
				const norm = s => String(s || '').replace(/\s+/g, '').toLowerCase();
				const hit = all.find(el => {
					const text = norm(el.textContent);
					const value = norm(el.getAttribute('data-value') || el.getAttribute('value') || '');
					return text === '1天' || text === '1d' || text === '1日' || value === '1';
				});
				if (!hit) return '';
				hit.click();
				return String(hit.textContent || '').trim();
			})()`, &picked),
		)
		if err != nil {
			return "", err
		}
		if strings.TrimSpace(picked) == "" {
			return "", errors.New("coinglass 1d option not found")
		}
		return fmt.Sprintf("已选中 %q", picked), nil
	}); err != nil {
		session.close()
		return nil, err
	}

	if err := m.runLoggedStep(session.taskCtx, progress, "点击刷新", func() (string, error) {
		var clicked bool
		err := chromedp.Run(session.taskCtx, chromedp.Evaluate(`(() => {
			const svgs = Array.from(document.querySelectorAll('svg'));
			const refreshSvg = svgs.find(svg => {
				const polylines = Array.from(svg.querySelectorAll('polyline')).map(node => String(node.getAttribute('points') || '').trim());
				return polylines.includes('1 4 1 10 7 10') && polylines.includes('23 20 23 14 17 14') && !!svg.querySelector('path');
			});
			if (!refreshSvg) return false;
			const target = refreshSvg.closest('button') || refreshSvg.parentElement;
			if (!target) return false;
			target.scrollIntoView({block:'center', inline:'center'});
			for (const type of ['pointerdown', 'mousedown', 'mouseup', 'click']) {
				target.dispatchEvent(new MouseEvent(type, {bubbles:true, cancelable:true, view:window}));
			}
			return true;
		})()`, &clicked))
		if err != nil {
			return "", err
		}
		if !clicked {
			return "", errors.New("coinglass refresh button not found")
		}
		return "已点击刷新按钮", nil
	}); err != nil {
		session.close()
		return nil, err
	}

	if err := m.runLoggedStep(session.taskCtx, progress, "抓取图表", func() (string, error) {
		var chartInfo struct {
			RootClass   string `json:"rootClass"`
			ReactClass  string `json:"reactClass"`
			DomID       string `json:"domId"`
			Width       int    `json:"width"`
			Height      int    `json:"height"`
			HasProgress bool   `json:"hasProgress"`
			HasOverlay  bool   `json:"hasOverlay"`
		}
		err := chromedp.Run(session.taskCtx, chromedp.Evaluate(`(() => {
			const canvas = document.querySelector('canvas[data-zr-dom-id="zr_0"]');
			const reactRoot = canvas ? canvas.closest('.echarts-for-react') : null;
			const chartRoot = reactRoot ? reactRoot.closest('div.MuiBox-root') : null;
			const progressbar = chartRoot ? chartRoot.querySelector('[role="progressbar"]') : null;
			const overlay = chartRoot ? Array.from(chartRoot.querySelectorAll('div')).find(node => {
				const style = String(node.getAttribute('style') || '');
				return style.includes('backdrop-filter:blur(8px)') || style.includes('background-color:var(--joy-palette-background-backdrop)');
			}) : null;
			return {
				rootClass: chartRoot ? String(chartRoot.className || '') : '',
				reactClass: reactRoot ? String(reactRoot.className || '') : '',
				domId: canvas ? (canvas.getAttribute('data-zr-dom-id') || '') : '',
				width: canvas ? Number(canvas.width || 0) : 0,
				height: canvas ? Number(canvas.height || 0) : 0,
				hasProgress: !!progressbar,
				hasOverlay: !!overlay
			};
		})()`, &chartInfo))
		if err != nil {
			return "", err
		}
		if chartInfo.DomID != "zr_0" || chartInfo.Width <= 0 || chartInfo.Height <= 0 || strings.TrimSpace(chartInfo.ReactClass) == "" {
			return "", errors.New("coinglass chart not found")
		}
		return fmt.Sprintf("chart=%q react=%q data-zr-dom-id=%q size=%dx%d progress=%t overlay=%t", chartInfo.RootClass, chartInfo.ReactClass, chartInfo.DomID, chartInfo.Width, chartInfo.Height, chartInfo.HasProgress, chartInfo.HasOverlay), nil
	}); err != nil {
		session.close()
		return nil, err
	}

	return session, nil
}

*/

func (m *WebDataSourceManager) newCaptureSessionV4(ctx context.Context, chromePath, profileDir string, progress *webDataSourceProgress) (*webDataSourceSession, error) {
	if err := os.MkdirAll(profileDir, 0o755); err != nil {
		return nil, err
	}
	opts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.ExecPath(chromePath),
		chromedp.Flag("headless", false),
		chromedp.UserAgent("Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36"),
		chromedp.UserDataDir(profileDir),
		chromedp.Flag("disable-blink-features", "AutomationControlled"),
		chromedp.WindowSize(1440, 900),
	)
	allocCtx, cancelAlloc := chromedp.NewExecAllocator(ctx, opts...)
	taskCtx, cancelTask := chromedp.NewContext(allocCtx)
	session := &webDataSourceSession{
		taskCtx: taskCtx,
		cancel: func() {
			cancelTask()
			cancelAlloc()
		},
	}

	if err := minimizeBrowserWindow(session.taskCtx); err != nil {
		m.appendStepLog("Minimize Chrome Window", "info", fmt.Sprintf("best effort skipped: %v", err))
	} else {
		m.appendStepLog("Minimize Chrome Window", "success", "browser launched in minimized mode")
	}

	findTargetPanelJS := webDataSourceFindTargetPanelJS()
	findPeriodRootJS := webDataSourceFindPeriodRootJS(findTargetPanelJS)

	if err := m.runLoggedStep(session.taskCtx, progress, "Open Coinglass Liquidation Map", func() (string, error) {
		err := chromedp.Run(session.taskCtx,
			chromedp.ActionFunc(func(ctx context.Context) error {
				_, err := page.AddScriptToEvaluateOnNewDocument(webDataSourceHookJS).Do(ctx)
				return err
			}),
			chromedp.ActionFunc(func(ctx context.Context) error {
				_, _, _, err := page.Navigate("https://www.coinglass.com/zh/pro/futures/LiquidationMap").Do(ctx)
				return err
			}),
		)
		if err != nil {
			return "", err
		}

		deadline := time.Now().Add(25 * time.Second)
		if ctxDeadline, ok := session.taskCtx.Deadline(); ok && ctxDeadline.Before(deadline) {
			deadline = ctxDeadline
		}
		for {
			var panelInfo struct {
				InputValue  string `json:"inputValue"`
				PeriodValue string `json:"periodValue"`
				HasRefresh  bool   `json:"hasRefresh"`
				HasChart    bool   `json:"hasChart"`
			}
			err = chromedp.Run(session.taskCtx, chromedp.Evaluate(`(() => {
				const isRefreshButton = btn => {
					if (!btn || btn.offsetParent === null) return false;
					const svg = btn.querySelector('svg');
					if (!svg) return false;
					const polylines = Array.from(svg.querySelectorAll('polyline')).map(node => String(node.getAttribute('points') || '').trim());
					return polylines.includes('1 4 1 10 7 10') && polylines.includes('23 20 23 14 17 14') && !!svg.querySelector('path');
				};
				const panel = `+findTargetPanelJS+`;
				const input = panel ? panel.querySelector('input.MuiAutocomplete-input[role="combobox"]') : null;
				const periodRoot = `+findPeriodRootJS+`;
				const hidden = periodRoot ? periodRoot.querySelector('input[aria-hidden="true"]') : null;
				return {
					inputValue: input ? String(input.value || '').trim() : '',
					periodValue: hidden ? String(hidden.value || hidden.getAttribute('value') || '').trim() : '',
					hasRefresh: !!(panel && Array.from(panel.querySelectorAll('button')).some(isRefreshButton)),
					hasChart: !!(panel && panel.querySelector('.echarts-for-react'))
				};
			})()`, &panelInfo))
			if err == nil && strings.TrimSpace(panelInfo.InputValue) != "" && panelInfo.PeriodValue != "" && panelInfo.HasRefresh && panelInfo.HasChart {
				return fmt.Sprintf("symbol=%q period=%q chart=%t refresh=%t", panelInfo.InputValue, panelInfo.PeriodValue, panelInfo.HasChart, panelInfo.HasRefresh), nil
			}
			if time.Now().After(deadline) {
				break
			}
			if err := sleepWithContext(session.taskCtx, 500*time.Millisecond); err != nil {
				return "", err
			}
		}
		return "", errors.New("coinglass target liquidation panel not found")
	}); err != nil {
		session.close()
		return nil, err
	}

	if err := m.runLoggedStep(session.taskCtx, progress, "Inject payload hook", func() (string, error) {
		err := chromedp.Run(session.taskCtx, chromedp.Evaluate(webDataSourceHookJS, nil))
		return "hook injected", err
	}); err != nil {
		session.close()
		return nil, err
	}

	if err := m.runLoggedStep(session.taskCtx, progress, "Locate symbol selector", func() (string, error) {
		var inputInfo struct {
			ID          string `json:"id"`
			Value       string `json:"value"`
			PeriodValue string `json:"periodValue"`
		}
		err := chromedp.Run(session.taskCtx, chromedp.Evaluate(`(() => {
			const panel = `+findTargetPanelJS+`;
			const input = panel ? panel.querySelector('input.MuiAutocomplete-input[role="combobox"]') : null;
			const root = `+findPeriodRootJS+`;
			const hidden = root ? root.querySelector('input[aria-hidden="true"]') : null;
			return {
				id: input ? (input.id || '') : '',
				value: input ? String(input.value || '').trim() : '',
				periodValue: hidden ? String(hidden.value || hidden.getAttribute('value') || '').trim() : ''
			};
		})()`, &inputInfo))
		if err != nil {
			return "", err
		}
		if strings.TrimSpace(inputInfo.Value) == "" {
			return "", errors.New("coinglass symbol input not found")
		}
		return fmt.Sprintf("id=%q value=%q period=%q", inputInfo.ID, inputInfo.Value, inputInfo.PeriodValue), nil
	}); err != nil {
		session.close()
		return nil, err
	}

	if err := m.runLoggedStep(session.taskCtx, progress, "Open symbol selector", func() (string, error) {
		var opened struct {
			Opened      bool   `json:"opened"`
			Current     string `json:"current"`
			VisibleOpts int    `json:"visibleOpts"`
		}
		err := chromedp.Run(session.taskCtx, chromedp.Evaluate(`(() => {
			const panel = `+findTargetPanelJS+`;
			const input = panel ? panel.querySelector('input.MuiAutocomplete-input[role="combobox"]') : null;
			const root = input ? input.closest('.MuiAutocomplete-root') : null;
			const buttons = root ? Array.from(root.querySelectorAll('button')).filter(btn => btn.offsetParent !== null) : [];
			const btn = buttons.find(btn => String(btn.className || '').includes('MuiAutocomplete-popupIndicator')) || buttons[0] || null;
			if (!input) return {opened:false, current:'', visibleOpts:0};
			input.focus();
			const target = btn || input;
			for (const type of ['pointerdown', 'mousedown', 'mouseup', 'click']) {
				target.dispatchEvent(new MouseEvent(type, {bubbles:true, cancelable:true, view:window}));
			}
			input.dispatchEvent(new KeyboardEvent('keydown', {key:'ArrowDown', code:'ArrowDown', bubbles:true}));
			input.dispatchEvent(new KeyboardEvent('keyup', {key:'ArrowDown', code:'ArrowDown', bubbles:true}));
			const optionSel = '[role="option"], [role="menuitem"], .MuiOption-root, .MuiMenuItem-root, li[class*="Option"], li[class*="option"]';
			const visibleOpts = Array.from(document.querySelectorAll(optionSel)).filter(node => node.offsetParent !== null).length;
			return {
				opened: true,
				current: String(input.value || '').trim(),
				visibleOpts
			};
		})()`, &opened))
		if err != nil {
			return "", err
		}
		if !opened.Opened {
			return "", errors.New("coinglass symbol selector not found")
		}
		return fmt.Sprintf("current=%q visibleOptions=%d", opened.Current, opened.VisibleOpts), nil
	}); err != nil {
		session.close()
		return nil, err
	}

	if err := m.runLoggedStep(session.taskCtx, progress, "Select ETH", func() (string, error) {
		if err := sleepWithContext(session.taskCtx, 300*time.Millisecond); err != nil {
			return "", err
		}
		var picked struct {
			PickedText string `json:"pickedText"`
			InputValue string `json:"inputValue"`
		}
		err := chromedp.Run(session.taskCtx, chromedp.Evaluate(`(() => {
			const all = Array.from(document.querySelectorAll('[role="option"], [role="menuitem"], .MuiOption-root, .MuiMenuItem-root, li[class*="Option"], li[class*="option"]'));
			const visible = all.filter(el => el.offsetParent !== null);
			const norm = s => String(s || '').trim().toUpperCase();
			let hit = null;
			const btcIdx = visible.findIndex(el => norm(el.textContent) === 'BTC');
			if (btcIdx >= 0 && visible[btcIdx + 1] && norm(visible[btcIdx + 1].textContent) === 'ETH') {
				hit = visible[btcIdx + 1];
			}
			if (!hit) {
				const exact = visible.filter(el => norm(el.textContent) === 'ETH');
				if (exact.length >= 2) hit = exact[1];
				if (!hit && exact.length == 1) hit = exact[0];
			}
			if (!hit) {
				hit = visible.find(el => norm(el.textContent).includes('ETH')) || null;
			}
			if (!hit) return {pickedText:'', inputValue:''};
			hit.scrollIntoView({block:'nearest'});
			for (const type of ['pointerdown', 'mousedown', 'mouseup', 'click']) {
				hit.dispatchEvent(new MouseEvent(type, {bubbles:true, cancelable:true, view:window}));
			}
			const panel = `+findTargetPanelJS+`;
			const input = panel ? panel.querySelector('input.MuiAutocomplete-input[role="combobox"]') : null;
			return {
				pickedText: String(hit.textContent || '').trim(),
				inputValue: input ? String(input.value || '').trim() : ''
			};
		})()`, &picked))
		if err != nil {
			return "", err
		}
		finalValue := strings.TrimSpace(picked.InputValue)
		deadline := time.Now().Add(4 * time.Second)
		for !strings.EqualFold(finalValue, "ETH") && time.Now().Before(deadline) {
			if err := sleepWithContext(session.taskCtx, 200*time.Millisecond); err != nil {
				return "", err
			}
			err = chromedp.Run(session.taskCtx, chromedp.Evaluate(`(() => {
				const panel = `+findTargetPanelJS+`;
				const input = panel ? panel.querySelector('input.MuiAutocomplete-input[role="combobox"]') : null;
				return input ? String(input.value || '').trim() : '';
			})()`, &finalValue))
			if err != nil {
				return "", err
			}
		}
		if !strings.EqualFold(strings.TrimSpace(finalValue), "ETH") {
			return "", fmt.Errorf("coinglass selected unexpected symbol %q", finalValue)
		}
		return fmt.Sprintf("picked=%q current=%q", picked.PickedText, finalValue), nil
	}); err != nil {
		session.close()
		return nil, err
	}

	if err := m.runLoggedStep(session.taskCtx, progress, "Verify ETH liquidation chart", func() (string, error) {
		var chartInfo struct {
			InputValue  string `json:"inputValue"`
			PeriodValue string `json:"periodValue"`
			ChartClass  string `json:"chartClass"`
		}
		err := chromedp.Run(session.taskCtx, chromedp.Evaluate(`(() => {
			const panel = `+findTargetPanelJS+`;
			const input = panel ? panel.querySelector('input.MuiAutocomplete-input[role="combobox"]') : null;
			const root = `+findPeriodRootJS+`;
			const hidden = root ? root.querySelector('input[aria-hidden="true"]') : null;
			const chart = panel ? panel.querySelector('.echarts-for-react') : null;
			return {
				inputValue: input ? String(input.value || '').trim() : '',
				periodValue: hidden ? String(hidden.value || hidden.getAttribute('value') || '').trim() : '',
				chartClass: chart ? String(chart.className || '').trim() : ''
			};
		})()`, &chartInfo))
		if err != nil {
			return "", err
		}
		if !strings.EqualFold(strings.TrimSpace(chartInfo.InputValue), "ETH") || strings.TrimSpace(chartInfo.PeriodValue) == "" || strings.TrimSpace(chartInfo.ChartClass) == "" {
			return "", errors.New("coinglass ETH chart panel not ready")
		}
		return fmt.Sprintf("symbol=%q period=%q chart=%q", chartInfo.InputValue, chartInfo.PeriodValue, chartInfo.ChartClass), nil
	}); err != nil {
		session.close()
		return nil, err
	}

	return session, nil
}

func (m *WebDataSourceManager) captureWindowV2(session *webDataSourceSession, progress *webDataSourceProgress, days int) (map[string]any, capturedPayloadMeta, error) {
	label := map[int]string{1: "1 day", 7: "7 day", 30: "30 day"}[days]
	if label == "" {
		label = fmt.Sprintf("%d day", days)
	}

	findTargetPanelJS := webDataSourceFindTargetPanelJS()
	findPeriodRootJS := webDataSourceFindPeriodRootJS(findTargetPanelJS)
	dayValue := strconv.Itoa(days)

	if err := m.runLoggedStep(session.taskCtx, progress, fmt.Sprintf("Open period selector for %s", label), func() (string, error) {
		var buttonInfo struct {
			Opened bool   `json:"opened"`
			Text   string `json:"text"`
			Value  string `json:"value"`
		}
		err := chromedp.Run(session.taskCtx, chromedp.Evaluate(`(() => {
			const root = `+findPeriodRootJS+`;
			const button = root ? root.querySelector('button[role="combobox"]') : null;
			const hidden = root ? root.querySelector('input[aria-hidden="true"]') : null;
			if (!button || !hidden) return {opened:false, text:'', value:''};
			button.scrollIntoView({block:'center', inline:'center'});
			for (const type of ['pointerdown', 'mousedown', 'mouseup', 'click']) {
				button.dispatchEvent(new MouseEvent(type, {bubbles:true, cancelable:true, view:window}));
			}
			return {
				opened: true,
				text: String(button.textContent || '').trim(),
				value: String(hidden.value || hidden.getAttribute('value') || '').trim()
			};
		})()`, &buttonInfo))
		if err != nil {
			return "", err
		}
		if !buttonInfo.Opened {
			return "", errors.New("coinglass window selector not found")
		}
		return fmt.Sprintf("current=%q value=%q", buttonInfo.Text, buttonInfo.Value), nil
	}); err != nil {
		return nil, capturedPayloadMeta{}, err
	}

	if err := m.runLoggedStep(session.taskCtx, progress, fmt.Sprintf("Select %s", label), func() (string, error) {
		if err := sleepWithContext(session.taskCtx, 250*time.Millisecond); err != nil {
			return "", err
		}
		{
			var picked struct {
				Selected bool   `json:"selected"`
				Text     string `json:"text"`
				Value    string `json:"value"`
				Debug    string `json:"debug"`
			}
			err := chromedp.Run(session.taskCtx, chromedp.Evaluate(fmt.Sprintf(`(() => {
			const norm = s => String(s || '').replace(/\s+/g, '').toLowerCase();
			const isVisible = el => {
				if (!el) return false;
				const style = window.getComputedStyle(el);
				if (!style || style.display === 'none' || style.visibility === 'hidden' || Number(style.opacity || '1') === 0) return false;
				const rect = el.getBoundingClientRect();
				return rect.width > 0 && rect.height > 0;
			};
			const dayNum = %q;
			const wants = new Set([norm(%q), dayNum, dayNum + 'd', dayNum + 'day', dayNum + 'days', dayNum + 'day(s)', dayNum + ' day']);
			const root = `+findPeriodRootJS+`;
			const hidden = root ? root.querySelector('input[aria-hidden="true"]') : null;
			const button = root ? root.querySelector('button[role="combobox"]') : null;
			const currentValue = String(hidden ? (hidden.value || hidden.getAttribute('value') || '') : '').trim();
			const currentText = norm(button ? button.textContent : '');
			if (currentValue === dayNum || wants.has(currentText)) {
				return {selected:true, text:button ? String(button.textContent || '').trim() : '', value:currentValue, debug:'already selected'};
			}
			const optionSel = '[role="option"], [role="menuitem"], .MuiOption-root, .MuiMenuItem-root, li[class*="Option"], li[class*="option"], li[class*="MenuItem"], [data-value], button';
			const popupId = button ? String(button.getAttribute('aria-controls') || '').trim() : '';
			const popupRoot = popupId ? document.getElementById(popupId) : null;
			const pool = popupRoot ? Array.from(popupRoot.querySelectorAll(optionSel)) : Array.from(document.querySelectorAll(optionSel));
			const candidates = pool.filter(isVisible).map(el => ({
				el,
				text: norm(el.textContent),
				rawText: String(el.textContent || '').trim(),
				value: norm(el.getAttribute('data-value') || el.getAttribute('value') || '')
			}));
			let hit = candidates.find(item => wants.has(item.text) || wants.has(item.value));
			if (!hit) {
				hit = candidates.find(item => {
					const digits = item.text.replace(/[^0-9]/g, '');
					return item.value === dayNum || digits === dayNum || (item.text.includes(dayNum) && item.text.includes('day'));
				});
			}
			const debug = 'popup=' + (popupId || '(none)') + ' options=' + candidates.slice(0, 8).map(item => item.rawText || item.value || '(blank)').join(' | ');
			if (!hit && button && ['1', '7', '30'].includes(currentValue) && ['1', '7', '30'].includes(dayNum)) {
				const order = ['1', '7', '30'];
				const fromIdx = order.indexOf(currentValue);
				const toIdx = order.indexOf(dayNum);
				if (fromIdx >= 0 && toIdx >= 0 && fromIdx !== toIdx) {
					button.focus();
					for (const type of ['pointerdown', 'mousedown', 'mouseup', 'click']) {
						button.dispatchEvent(new MouseEvent(type, {bubbles:true, cancelable:true, view:window}));
					}
					const key = toIdx > fromIdx ? 'ArrowDown' : 'ArrowUp';
					for (let i = 0; i < Math.abs(toIdx - fromIdx); i++) {
						button.dispatchEvent(new KeyboardEvent('keydown', {key, code:key, bubbles:true}));
						button.dispatchEvent(new KeyboardEvent('keyup', {key, code:key, bubbles:true}));
					}
					button.dispatchEvent(new KeyboardEvent('keydown', {key:'Enter', code:'Enter', bubbles:true}));
					button.dispatchEvent(new KeyboardEvent('keyup', {key:'Enter', code:'Enter', bubbles:true}));
					return {selected:true, text:'keyboard-fallback', value:dayNum, debug:debug + ' | keyboard=' + currentValue + '->' + dayNum};
				}
			}
			if (!hit) return {selected:false, text:'', value:'', debug};
			hit.el.scrollIntoView({block:'nearest'});
			for (const type of ['pointerdown', 'mousedown', 'mouseup', 'click']) {
				hit.el.dispatchEvent(new MouseEvent(type, {bubbles:true, cancelable:true, view:window}));
			}
			return {selected:true, text:hit.rawText, value:hit.value, debug};
		})()`, dayValue, label), &picked))
			if err != nil {
				return "", err
			}
			if !picked.Selected {
				detail := strings.TrimSpace(picked.Debug)
				if detail == "" {
					detail = "no visible popup options"
				}
				return "", fmt.Errorf("coinglass target window option not found: %s", detail)
			}
			var current struct {
				Text  string `json:"text"`
				Value string `json:"value"`
			}
			deadline := time.Now().Add(4 * time.Second)
			for {
				err = chromedp.Run(session.taskCtx, chromedp.Evaluate(`(() => {
				const root = `+findPeriodRootJS+`;
				const hidden = root ? root.querySelector('input[aria-hidden="true"]') : null;
				const button = root ? root.querySelector('button[role="combobox"]') : null;
				return {
					text: button ? String(button.textContent || '').trim() : '',
					value: hidden ? String(hidden.value || hidden.getAttribute('value') || '').trim() : ''
				};
			})()`, &current))
				if err != nil {
					return "", err
				}
				if current.Value == dayValue {
					return fmt.Sprintf("selected=%q current=%q value=%q", picked.Text, current.Text, current.Value), nil
				}
				if time.Now().After(deadline) {
					break
				}
				if err := sleepWithContext(session.taskCtx, 200*time.Millisecond); err != nil {
					return "", err
				}
			}
			return "", fmt.Errorf("coinglass period did not switch to %s", dayValue)
		}

		var optionTarget struct {
			Selected bool   `json:"selected"`
			Text     string `json:"text"`
			Value    string `json:"value"`
			Debug    string `json:"debug"`
		}
		err := chromedp.Run(session.taskCtx, chromedp.Evaluate(fmt.Sprintf(`(() => {
			const norm = s => String(s || '').replace(/\s+/g, '').toLowerCase();
			const isVisible = el => {
				if (!el) return false;
				const style = window.getComputedStyle(el);
				if (!style || style.display === 'none' || style.visibility === 'hidden' || Number(style.opacity || '1') === 0) return false;
				const rect = el.getBoundingClientRect();
				return rect.width > 0 && rect.height > 0;
			};
			const dayNum = %q;
			const wants = new Set([
				norm(%q),
				dayNum,
				dayNum + 'd',
				dayNum + ' day',
				dayNum + 'day',
				dayNum + 'days',
				dayNum + 'day(s)' /*
				dayNum + '天',
				dayNum + '日'
			*/ ]);
			const root = `+findPeriodRootJS+`;
			const hidden = root ? root.querySelector('input[aria-hidden="true"]') : null;
			const button = root ? root.querySelector('button[role="combobox"]') : null;
			const currentValue = String(hidden ? (hidden.value || hidden.getAttribute('value') || '') : '').trim();
			const currentText = norm(button ? button.textContent : '');
			if (currentValue === dayNum || wants.has(currentText)) {
				return {
					selected: true,
					text: button ? String(button.textContent || '').trim() : '',
					value: currentValue,
					debug: 'already selected'
				};
			}
			const optionSel = '[role="option"], [role="menuitem"], .MuiOption-root, .MuiMenuItem-root, li[class*="Option"], li[class*="option"], li[class*="MenuItem"], [data-value], button';
			const popupId = button ? String(button.getAttribute('aria-controls') || '').trim() : '';
			const popupRoot = popupId ? document.getElementById(popupId) : null;
			const pool = popupRoot ? Array.from(popupRoot.querySelectorAll(optionSel)) : Array.from(document.querySelectorAll(optionSel));
			const all = pool.filter(el => isVisible(el));
			const candidates = all.map(el => ({
				el,
				text: norm(el.textContent),
				rawText: String(el.textContent || '').trim(),
				value: norm(el.getAttribute('data-value') || el.getAttribute('value') || '')
			}));
			let hit = candidates.find(item => {
				const text = item.text;
				const value = item.value;
				return wants.has(text) || wants.has(value);
			});
			if (!hit) {
				hit = candidates.find(item => {
					const text = item.text;
					const value = item.value;
					return value === dayNum || (text.includes(dayNum) && (text.includes('day') || text.includes('澶? || text.includes('鏃?)));
				});
			*/ }
			if (!hit) {
				hit = candidates.find(item => {
					const text = item.text;
					const value = item.value;
					const digits = text.replace(/[^0-9]/g, '');
					return value === dayNum || digits === dayNum || (text.includes(dayNum) && text.includes('day'));
				});
			}
			const debug = 'popup=' + (popupId || '(none)') + ' options=' + candidates.slice(0, 8).map(item => item.rawText || item.value || '(blank)').join(' | ');
			if (!hit) return {selected:false, text:'', value:'', debug};
			hit.el.scrollIntoView({block:'nearest'});
			for (const type of ['pointerdown', 'mousedown', 'mouseup', 'click']) {
				hit.el.dispatchEvent(new MouseEvent(type, {bubbles:true, cancelable:true, view:window}));
			}
			return {
				selected: true,
				text: hit.rawText,
				value: hit.value,
				debug
			};
		})()`, dayValue, label), &optionTarget))
		if err != nil {
			return "", err
		}
		if !optionTarget.Selected {
			detail := strings.TrimSpace(optionTarget.Debug)
			if detail == "" {
				detail = "no visible popup options"
			}
			return "", fmt.Errorf("coinglass target window option not found: %s", detail)
		}
		var current struct {
			Text  string `json:"text"`
			Value string `json:"value"`
		}
		deadline := time.Now().Add(4 * time.Second)
		for {
			err = chromedp.Run(session.taskCtx, chromedp.Evaluate(`(() => {
				const root = `+findPeriodRootJS+`;
				const hidden = root ? root.querySelector('input[aria-hidden="true"]') : null;
				const button = root ? root.querySelector('button[role="combobox"]') : null;
				return {
					text: button ? String(button.textContent || '').trim() : '',
					value: hidden ? String(hidden.value || hidden.getAttribute('value') || '').trim() : ''
				};
			})()`, &current))
			if err != nil {
				return "", err
			}
			if current.Value == dayValue {
				return fmt.Sprintf("selected=%q current=%q value=%q", optionTarget.Text, current.Text, current.Value), nil
			}
			if time.Now().After(deadline) {
				break
			}
			if err := sleepWithContext(session.taskCtx, 200*time.Millisecond); err != nil {
				return "", err
			}
		}
		return "", fmt.Errorf("coinglass period did not switch to %s", dayValue)
	}); err != nil {
		return nil, capturedPayloadMeta{}, err
	}

	if err := m.runLoggedStep(session.taskCtx, progress, fmt.Sprintf("Refresh %s chart", label), func() (string, error) {
		var refreshInfo struct {
			Clicked bool `json:"clicked"`
		}
		err := chromedp.Run(session.taskCtx, chromedp.Evaluate(`(() => {
			window._liqData = null;
			window._liqLog = [];
			const isRefreshButton = btn => {
				if (!btn || btn.offsetParent === null) return false;
				const svg = btn.querySelector('svg');
				if (!svg) return false;
				const polylines = Array.from(svg.querySelectorAll('polyline')).map(node => String(node.getAttribute('points') || '').trim());
				return polylines.includes('1 4 1 10 7 10') && polylines.includes('23 20 23 14 17 14') && !!svg.querySelector('path');
			};
			const panel = `+findTargetPanelJS+`;
			const btn = panel ? Array.from(panel.querySelectorAll('button')).find(isRefreshButton) : null;
			if (!btn) return {clicked:false};
			btn.scrollIntoView({block:'center', inline:'center'});
			for (const type of ['pointerdown', 'mousedown', 'mouseup', 'click']) {
				btn.dispatchEvent(new MouseEvent(type, {bubbles:true, cancelable:true, view:window}));
			}
			return {clicked:true};
		})()`, &refreshInfo))
		if err != nil {
			return "", err
		}
		if !refreshInfo.Clicked {
			return "", errors.New("coinglass refresh button not found")
		}
		return "refresh clicked", nil
	}); err != nil {
		return nil, capturedPayloadMeta{}, err
	}

	var payload map[string]any
	meta := capturedPayloadMeta{}
	if err := m.runLoggedStep(session.taskCtx, progress, fmt.Sprintf("Capture %s payload", label), func() (string, error) {
		deadline := time.Now().Add((defaultWebDataSourceTimeoutSec - 2) * time.Second)
		if ctxDeadline, ok := session.taskCtx.Deadline(); ok {
			deadline = ctxDeadline.Add(-2 * time.Second)
		}
		if minDeadline := time.Now().Add(8 * time.Second); deadline.Before(minDeadline) {
			deadline = minDeadline
		}
		chartEligibleAt := time.Now().Add(2 * time.Second)
		hookGrace := 1500 * time.Millisecond
		var fallbackPayload map[string]any
		fallbackMeta := capturedPayloadMeta{}
		fallbackPointCount := 0
		var fallbackReadyAt time.Time
		lastHookSignature := ""
		for time.Now().Before(deadline) {
			var hookData map[string]any
			if err := chromedp.Run(session.taskCtx, chromedp.Evaluate(`window._liqData`, &hookData)); err == nil && len(hookData) > 0 {
				points, rangeLow, rangeHigh := normalizeWebDataSourcePayload(hookData)
				var logs []any
				_ = chromedp.Run(session.taskCtx, chromedp.Evaluate(`window._liqLog || []`, &logs))
				hookHits := len(logs)
				if len(points) > 0 {
					signature := fmt.Sprintf("%.2f|%.2f|%d|%d", rangeLow, rangeHigh, len(points), hookHits)
					if signature != lastHookSignature {
						lastHookSignature = signature
						fallbackReadyAt = time.Now().Add(hookGrace)
					}
					fallbackPayload = hookData
					fallbackPointCount = len(points)
					fallbackMeta = capturedPayloadMeta{
						RangeLow:  rangeLow,
						RangeHigh: rangeHigh,
						Source:    "hook",
						HookHits:  hookHits,
					}
					if hookHits >= 2 || (!fallbackReadyAt.IsZero() && time.Now().After(fallbackReadyAt)) {
						payload = fallbackPayload
						meta = fallbackMeta
						return fmt.Sprintf("source=%s hookHits=%d points=%d range=[%.2f, %.2f]", meta.Source, meta.HookHits, fallbackPointCount, meta.RangeLow, meta.RangeHigh), nil
					}
				}
			}

			if time.Now().After(chartEligibleAt) {
				var chartData map[string]any
				if err := chromedp.Run(session.taskCtx, chromedp.Evaluate(webDataSourceExtractChartPayloadJS(findTargetPanelJS), &chartData)); err == nil && len(chartData) > 0 {
					chartPointCount := 0
					if longPoints, ok := chartData["long"].([]any); ok {
						chartPointCount += len(longPoints)
					}
					if shortPoints, ok := chartData["short"].([]any); ok {
						chartPointCount += len(shortPoints)
					}
					if chartPointCount > 0 {
						payload = chartData
						meta = capturedPayloadMeta{
							RangeLow:  toFloatFromAny(chartData["rangeLow"]),
							RangeHigh: toFloatFromAny(chartData["rangeHigh"]),
							Source:    "chart",
						}
						var logs []any
						_ = chromedp.Run(session.taskCtx, chromedp.Evaluate(`window._liqLog || []`, &logs))
						meta.HookHits = len(logs)
						return fmt.Sprintf("source=%s hookHits=%d points=%d range=[%.2f, %.2f]", meta.Source, meta.HookHits, chartPointCount, meta.RangeLow, meta.RangeHigh), nil
					}
				}
			}

			if err := sleepWithContext(session.taskCtx, 250*time.Millisecond); err != nil {
				return "", err
			}
		}
		if len(fallbackPayload) > 0 {
			payload = fallbackPayload
			meta = fallbackMeta
			return fmt.Sprintf("source=%s hookHits=%d points=%d range=[%.2f, %.2f]", meta.Source, meta.HookHits, fallbackPointCount, meta.RangeLow, meta.RangeHigh), nil
		}
		return "", errors.New("timed out waiting for coinglass payload or chart data")
	}); err != nil {
		return nil, capturedPayloadMeta{}, err
	}

	return payload, meta, nil
}

/*
func (m *WebDataSourceManager) captureWindow(session *webDataSourceSession, progress *webDataSourceProgress, days int) (map[string]any, capturedPayloadMeta, error) {
	label := map[int]string{1: "1 day", 7: "7 day", 30: "30 day"}[days]
	if label == "" {
		label = fmt.Sprintf("%d day", days)
	}

	var ok bool
	progress.setAction(fmt.Sprintf("打开周期下拉框，准备切换到 %s", label))
	js := fmt.Sprintf(`(() => {
		const btn = document.querySelectorAll('.MuiSelect-button')[%d];
		if (!btn) return false;
		for (const type of ['pointerdown','mousedown','mouseup','click']) btn.dispatchEvent(new MouseEvent(type, {bubbles:true, cancelable:true, view:window}));
		return true;
	})()`, session.btnIdx)
	if err := chromedp.Run(session.taskCtx, chromedp.Evaluate(js, &ok), chromedp.Sleep(1200*time.Millisecond)); err != nil || !ok {
		return nil, capturedPayloadMeta{}, errors.New("coinglass window selector not found")
	}

	windowIdx := map[int]int{1: 0, 7: 1, 30: 2}[days]
	progress.setAction(fmt.Sprintf("选择 %s 周期", label))
	js = fmt.Sprintf(`(() => {
		const norm = s => String(s || '').replace(/\s+/g, '').toLowerCase();
		const want = norm(%q);
		const dayNum = String(%d);
		const wants = [want, dayNum + 'd', dayNum + 'day', dayNum + 'days'];
		const optionSel = '[role="option"], [role="menuitem"], .MuiOption-root, .MuiMenuItem-root, li[class*="Option"], li[class*="option"], li[class*="MenuItem"], [data-value]';
		const all = Array.from(document.querySelectorAll(optionSel)).filter(el => el.offsetParent !== null);
		const candidates = all.map((el, i) => ({el, i, text: norm(el.textContent), value: norm(el.getAttribute('data-value') || el.getAttribute('value') || '')}));
		let hit = candidates.find(x => wants.includes(x.text) || wants.includes(x.value));
		if (!hit) hit = candidates.find(x => wants.some(w => x.text.includes(w) || x.value.includes(w)));
		if (!hit) hit = candidates.find(x => x.text === dayNum || x.value === dayNum);
		if (!hit && candidates[%d]) hit = candidates[%d];
		if (hit) { for (const type of ['pointerdown','mousedown','mouseup','click']) hit.el.dispatchEvent(new MouseEvent(type, {bubbles:true, cancelable:true, view:window})); return true; }
		return false;
	})()`, label, days, windowIdx, windowIdx)
	if err := chromedp.Run(session.taskCtx, chromedp.Evaluate(js, &ok)); err != nil || !ok {
		return nil, capturedPayloadMeta{}, errors.New("coinglass target window option not found")
	}

	progress.setAction(fmt.Sprintf("%s 周期切换完成，等待 3 秒稳定", label))
	select {
	case <-session.taskCtx.Done():
		return nil, capturedPayloadMeta{}, session.taskCtx.Err()
	case <-time.After(3 * time.Second):
	}

	progress.setAction(fmt.Sprintf("点击刷新并等待 %s 周期数据", label))
	if err := chromedp.Run(session.taskCtx, chromedp.Evaluate(`(() => {
		window._liqData = null;
		window._liqLog = [];
		const btns = Array.from(document.querySelectorAll('button')).filter(b => b.querySelector('svg') && b.textContent.trim() === '' && b.offsetParent !== null);
		if (btns.length) { btns[btns.length - 1].click(); return true; }
		return false;
	})()`, &ok)); err != nil || !ok {
		return nil, capturedPayloadMeta{}, errors.New("coinglass refresh button not found")
	}

	deadline := time.Now().Add((defaultWebDataSourceTimeoutSec - 2) * time.Second)
	if ctxDeadline, ok := session.taskCtx.Deadline(); ok {
		deadline = ctxDeadline.Add(-2 * time.Second)
	}
	if minDeadline := time.Now().Add(5 * time.Second); deadline.Before(minDeadline) {
		deadline = minDeadline
	}
	for time.Now().Before(deadline) {
		var data map[string]any
		if err := chromedp.Run(session.taskCtx, chromedp.Evaluate(`window._liqData`, &data)); err == nil && len(data) > 0 {
			meta := capturedPayloadMeta{
				RangeLow:  toFloatFromAny(data["rangeLow"]),
				RangeHigh: toFloatFromAny(data["rangeHigh"]),
			}
			var logs []any
			_ = chromedp.Run(session.taskCtx, chromedp.Evaluate(`window._liqLog || []`, &logs))
			meta.HookHits = len(logs)
			return data, meta, nil
		}
		select {
		case <-session.taskCtx.Done():
			return nil, capturedPayloadMeta{}, session.taskCtx.Err()
		case <-time.After(1200 * time.Millisecond):
		}
	}
	return nil, capturedPayloadMeta{}, errors.New("timed out waiting for coinglass liqMapV2 payload")
}

*/

func (m *WebDataSourceManager) captureWindow(session *webDataSourceSession, progress *webDataSourceProgress, days int) (map[string]any, capturedPayloadMeta, error) {
	label := map[int]string{1: "1天", 7: "7天", 30: "30天"}[days]
	if label == "" {
		label = fmt.Sprintf("%d天", days)
	}

	if err := m.runLoggedStep(session.taskCtx, progress, fmt.Sprintf("打开周期下拉框（目标 %s）", label), func() (string, error) {
		var buttonText string
		if err := chromedp.Run(session.taskCtx, chromedp.Evaluate(`(() => {
			const title = Array.from(document.querySelectorAll('h1.MuiTypography-root, h1')).find(h => {
				const text = (h.textContent || '').trim();
				return text === '比特币交易所清算地图' || (text.includes('比特币交易所清算地图') && !text.includes('Hyperliquid'));
			});
			if (!title) return '';
			let container = title.parentElement;
			for (let i = 0; i < 12 && container; i++) {
				const roots = Array.from(container.querySelectorAll('div.MuiSelect-root'));
				const hit = roots.find(root => {
					const btn = root.querySelector('button.MuiSelect-button[role="combobox"]');
					const hidden = root.querySelector('input[aria-hidden="true"]');
					if (!btn || !hidden) return false;
					const text = (btn.textContent || '').trim();
					const value = (hidden.value || hidden.getAttribute('value') || '').trim();
					return (text === '1天' || text === '1D' || text === '1日') && value === '1';
				});
				const button = hit ? hit.querySelector('button.MuiSelect-button[role="combobox"]') : null;
				if (button) {
					const text = (button.textContent || '').trim();
					for (const type of ['pointerdown','mousedown','mouseup','click']) {
						button.dispatchEvent(new MouseEvent(type, {bubbles:true, cancelable:true, view:window}));
					}
					return text;
				}
				container = container.parentElement;
			}
			return '';
		})()`, &buttonText)); err != nil {
			return "", err
		}
		if strings.TrimSpace(buttonText) == "" {
			return "", errors.New("coinglass window selector not found")
		}
		return fmt.Sprintf("当前显示=%q", buttonText), nil
	}); err != nil {
		return nil, capturedPayloadMeta{}, err
	}

	if err := m.runLoggedStep(session.taskCtx, progress, fmt.Sprintf("选择 %s", label), func() (string, error) {
		var pickedText string
		if err := chromedp.Run(session.taskCtx, chromedp.Evaluate(fmt.Sprintf(`(() => {
			const norm = s => String(s || '').replace(/\s+/g, '').toLowerCase();
			const dayNum = %d;
			const wants = new Set([
				norm(%q),
				dayNum + 'd',
				dayNum + 'day',
				dayNum + 'days',
				dayNum + '天'
			]);
			const optionSel = '[role="option"], [role="menuitem"], .MuiOption-root, .MuiMenuItem-root, li[class*="Option"], li[class*="option"], li[class*="MenuItem"], [data-value]';
			const all = Array.from(document.querySelectorAll(optionSel)).filter(el => el.offsetParent !== null);
			let hit = all.find(el => {
				const text = norm(el.textContent);
				const value = norm(el.getAttribute('data-value') || el.getAttribute('value') || '');
				return wants.has(text) || wants.has(value);
			});
			if (!hit) {
				hit = all.find(el => {
					const text = norm(el.textContent);
					const value = norm(el.getAttribute('data-value') || el.getAttribute('value') || '');
					return text.includes(String(dayNum)) || value.includes(String(dayNum));
				});
			}
			if (!hit) return '';
			hit.click();
			return (hit.textContent || '').trim();
		})()`, days, label), &pickedText)); err != nil {
			return "", err
		}
		if strings.TrimSpace(pickedText) == "" {
			return "", errors.New("coinglass target window option not found")
		}
		return fmt.Sprintf("已选中 %q", pickedText), nil
	}); err != nil {
		return nil, capturedPayloadMeta{}, err
	}

	var canvasInfo struct {
		DomID  string `json:"domId"`
		Width  int    `json:"width"`
		Height int    `json:"height"`
	}
	if err := m.runLoggedStep(session.taskCtx, progress, "抓取 canvas", func() (string, error) {
		var refreshOK bool
		if err := chromedp.Run(session.taskCtx, chromedp.Evaluate(`(() => {
			window._liqData = null;
			window._liqLog = [];
			const title = Array.from(document.querySelectorAll('h1.MuiTypography-root, h1')).find(h => {
				const text = (h.textContent || '').trim();
				return text === '比特币交易所清算地图' || (text.includes('比特币交易所清算地图') && !text.includes('Hyperliquid'));
			});
			if (!title) return {ok:false, domId:'', width:0, height:0};
			let container = title.parentElement;
			for (let i = 0; i < 12 && container; i++) {
				const canvas = container.querySelector('canvas');
				if (canvas) {
					const buttons = Array.from(container.querySelectorAll('button')).filter(btn => btn.offsetParent !== null);
					const refreshBtn = buttons.find(btn => {
						const text = (btn.textContent || '').trim();
						if (btn.closest('.MuiAutocomplete-root')) return false;
						if (btn.className && String(btn.className).includes('MuiSelect-button')) return false;
						return !!btn.querySelector('svg') && text === '';
					});
					if (refreshBtn) {
						for (const type of ['pointerdown','mousedown','mouseup','click']) {
							refreshBtn.dispatchEvent(new MouseEvent(type, {bubbles:true, cancelable:true, view:window}));
						}
						window._webdsCanvasMeta = {
							domId: canvas.getAttribute('data-zr-dom-id') || '',
							width: Number(canvas.width || 0),
							height: Number(canvas.height || 0)
						};
						return {ok:true, domId: window._webdsCanvasMeta.domId, width: window._webdsCanvasMeta.width, height: window._webdsCanvasMeta.height};
					}
				}
				container = container.parentElement;
			}
			return {ok:false, domId:'', width:0, height:0};
		})()`, &canvasInfo)); err != nil {
			return "", err
		}
		refreshOK = canvasInfo.Width > 0 && canvasInfo.Height > 0
		if !refreshOK {
			return "", errors.New("coinglass refresh button or canvas not found")
		}

		deadline := time.Now().Add((defaultWebDataSourceTimeoutSec - 2) * time.Second)
		if ctxDeadline, ok := session.taskCtx.Deadline(); ok {
			deadline = ctxDeadline.Add(-2 * time.Second)
		}
		if minDeadline := time.Now().Add(5 * time.Second); deadline.Before(minDeadline) {
			deadline = minDeadline
		}
		for time.Now().Before(deadline) {
			var data map[string]any
			if err := chromedp.Run(session.taskCtx, chromedp.Evaluate(`window._liqData`, &data)); err == nil && len(data) > 0 {
				return fmt.Sprintf("data-zr-dom-id=%q size=%dx%d", canvasInfo.DomID, canvasInfo.Width, canvasInfo.Height), nil
			}
			if err := sleepWithContext(session.taskCtx, 1200*time.Millisecond); err != nil {
				return "", err
			}
		}
		return "", errors.New("timed out waiting for coinglass liqMapV2 payload")
	}); err != nil {
		return nil, capturedPayloadMeta{}, err
	}

	deadline := time.Now().Add((defaultWebDataSourceTimeoutSec - 2) * time.Second)
	if ctxDeadline, ok := session.taskCtx.Deadline(); ok {
		deadline = ctxDeadline.Add(-2 * time.Second)
	}
	if minDeadline := time.Now().Add(5 * time.Second); deadline.Before(minDeadline) {
		deadline = minDeadline
	}
	for time.Now().Before(deadline) {
		var data map[string]any
		if err := chromedp.Run(session.taskCtx, chromedp.Evaluate(`window._liqData`, &data)); err == nil && len(data) > 0 {
			meta := capturedPayloadMeta{
				RangeLow:  toFloatFromAny(data["rangeLow"]),
				RangeHigh: toFloatFromAny(data["rangeHigh"]),
			}
			var logs []any
			_ = chromedp.Run(session.taskCtx, chromedp.Evaluate(`window._liqLog || []`, &logs))
			meta.HookHits = len(logs)
			return data, meta, nil
		}
		if err := sleepWithContext(session.taskCtx, 300*time.Millisecond); err != nil {
			return nil, capturedPayloadMeta{}, err
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

func webDataSourcePayloadCurrentPrice(payload map[string]any, rangeLow, rangeHigh float64) float64 {
	currentPrice := firstPositive(payload, "lastPrice", "price", "currentPrice", "markPrice")
	if currentPrice <= 0 && rangeHigh > rangeLow {
		currentPrice = (rangeLow + rangeHigh) / 2
	}
	if currentPrice <= 0 {
		return 0
	}
	return math.Round(currentPrice*10) / 10
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

func parseLiqMapV2Payload(payload map[string]any) []WebDataSourcePoint {
	rawData, ok := payload["data"].([]any)
	if !ok || len(rawData) == 0 {
		return nil
	}

	rangeLow := toFloatFromAny(payload["rangeLow"])
	rangeHigh := toFloatFromAny(payload["rangeHigh"])
	currentPrice := webDataSourcePayloadCurrentPrice(payload, rangeLow, rangeHigh)
	if currentPrice <= 0 {
		return nil
	}

	points := make([]WebDataSourcePoint, 0, 512)
	for _, item := range rawData {
		entry, ok := item.(map[string]any)
		if !ok {
			continue
		}

		exchange := ""
		if instrument, ok := entry["instrument"].(map[string]any); ok {
			exchange = strings.TrimSpace(fmt.Sprint(instrument["exName"]))
		}
		if exchange == "" || exchange == "<nil>" {
			exchange = strings.TrimSpace(fmt.Sprint(entry["exchange"]))
		}
		if exchange == "" || exchange == "<nil>" {
			exchange = "UNKNOWN"
		}

		liqMap, ok := entry["liqMapV2"].(map[string]any)
		if !ok || len(liqMap) == 0 {
			continue
		}

		for key, rawLevels := range liqMap {
			levels, ok := rawLevels.([]any)
			if !ok {
				continue
			}
			for _, level := range levels {
				row, ok := level.([]any)
				if !ok || len(row) < 2 {
					continue
				}
				price := toFloatFromAny(row[0])
				if price <= 0 {
					price = toFloatFromAny(key)
				}
				value := toFloatFromAny(row[1])
				if price <= 0 || value <= 0 {
					continue
				}

				side := ""
				switch {
				case price < currentPrice:
					side = "long"
				case price > currentPrice:
					side = "short"
				default:
					continue
				}

				points = append(points, WebDataSourcePoint{
					Exchange: strings.ToUpper(exchange),
					Side:     side,
					Price:    price,
					LiqValue: value,
				})
			}
		}
	}
	return points
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
	points = append(points, parseLiqMapV2Payload(payload)...)
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

const webDataSourceHookJS = `(() => {
	if (window._liqHookInstalled) return 'hook_exists';
	window._liqHookInstalled = true;
	window._liqData = null;
	window._liqLog = [];
	const captureText = (text, from) => {
		try {
			if (!text || !String(text).includes('liqMapV2')) return;
			window._liqLog.push('hit liqMapV2 via ' + from + ', len=' + String(text).length);
			const parsed = JSON.parse(text);
			window._liqData = parsed;
		} catch(e) {
			try { window._liqLog.push('parse failed via ' + from + ': ' + e.message); } catch(_) {}
		}
	};
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
	const _fetch = window.fetch;
	if (_fetch) {
		window.fetch = async function(...args) {
			const resp = await _fetch.apply(this, args);
			try { resp.clone().text().then(t => captureText(t, 'fetch')).catch(()=>{}); } catch(e) {}
			return resp;
		};
	}
	const _open = XMLHttpRequest.prototype.open;
	const _send = XMLHttpRequest.prototype.send;
	XMLHttpRequest.prototype.open = function(method, url) {
		this._liqURL = url;
		return _open.apply(this, arguments);
	};
	XMLHttpRequest.prototype.send = function() {
		try {
			this.addEventListener('load', function() {
				try { captureText(this.responseText, 'xhr:' + (this._liqURL || '')); } catch(e) {}
			});
		} catch(e) {}
		return _send.apply(this, arguments);
	};
	return 'hook_injected';
})()`

const webDataSourceHTML = `<!doctype html>
<html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>页面数据源</title>
<style>:root{--bg:#f5f7fb;--text:#1f2937;--muted:#64748b;--nav-bg:#0b1220;--nav-border:#243145;--nav-text:#eef3f9;--link:#d6deea;--panel-bg:#fff;--panel-border:#dce3ec;--ctl-bg:#fff;--ctl-text:#111827;--ctl-border:#cbd5e1;--chart-border:#e5e7eb}[data-theme="dark"]{--bg:#000;--text:#e5e7eb;--muted:#94a3b8;--nav-bg:#000;--nav-border:#111827;--nav-text:#eef3f9;--link:#d6deea;--panel-bg:#000;--panel-border:#1f2937;--ctl-bg:#000;--ctl-text:#e5e7eb;--ctl-border:#334155;--chart-border:#1f2937}html,body{height:100%}body{margin:0;background:var(--bg);color:var(--text);font-family:Inter,system-ui,Segoe UI,Arial,sans-serif;overflow:hidden}.nav{height:56px;background:var(--nav-bg);border-bottom:1px solid var(--nav-border);display:flex;align-items:center;justify-content:space-between;padding:0 20px;position:sticky;top:0;z-index:10;box-sizing:border-box}.nav-left,.nav-right{display:flex;align-items:center;gap:14px}.brand{font-size:18px;font-weight:700;color:var(--nav-text)}.menu a{color:var(--link);text-decoration:none;font-size:16px;margin-right:18px}.menu a.active{color:#fff;font-weight:700}.theme-toggle{display:inline-flex;align-items:center;gap:6px;font-size:13px}.theme-toggle button{height:30px;padding:0 10px;border-radius:999px;border:1px solid rgba(148,163,184,0.45);background:transparent;color:var(--nav-text);cursor:pointer}.theme-toggle button.label{cursor:default;opacity:.92}.theme-toggle button.active{background:rgba(255,255,255,0.12);border-color:rgba(255,255,255,0.18);color:#fff}.wrap{width:100%;height:calc(100vh - 56px);margin:0;padding:12px;box-sizing:border-box;display:flex;flex-direction:column}.grid{display:grid;grid-template-columns:330px minmax(0,1fr);gap:12px;min-height:0;flex:1}.panel{border:1px solid var(--panel-border);background:var(--panel-bg);padding:12px;border-radius:8px;box-sizing:border-box}.grid>.panel{overflow:auto}.main-area{display:flex;flex-direction:column;min-width:0;min-height:0}.small{font-size:12px;color:var(--muted)}.field label{display:block;font-size:12px;color:var(--muted);margin-bottom:6px}.field input,.field select,button{height:36px;border:1px solid var(--ctl-border);border-radius:8px;background:var(--ctl-bg);color:var(--ctl-text);padding:0 10px}.field input{width:100%;box-sizing:border-box}button{cursor:pointer}.primary{background:#0f172a;color:#eef3f9;border-color:#0f172a}.row{display:flex;gap:8px;align-items:center;flex-wrap:wrap}.cards{display:grid;grid-template-columns:repeat(4,minmax(0,1fr));gap:10px;margin-bottom:10px}.card{border:1px solid var(--panel-border);border-radius:8px;padding:10px;background:var(--panel-bg)}.card .k{font-size:12px;color:var(--muted)}.card .v{margin-top:6px;font-size:22px;font-weight:700}.chart-panel{display:flex;flex-direction:column;min-height:0;flex:1}.chart-head{display:grid;grid-template-columns:minmax(250px,320px) minmax(0,1fr);gap:16px;align-items:start;margin-bottom:8px}.chart-head-main{min-width:0}.chart-toolbar{display:flex;flex-direction:column;align-items:flex-end;gap:6px;min-width:0}.chart-toolbar-top{display:flex;flex-wrap:wrap;justify-content:flex-end;align-items:flex-end;gap:12px;width:100%}.toolbar-primary-controls{display:flex;flex-wrap:wrap;justify-content:flex-end;align-items:flex-end;gap:12px}.window-switch{display:inline-flex;align-items:center;gap:6px;padding:4px;border:1px solid var(--ctl-border);border-radius:999px;background:rgba(148,163,184,0.08)}.window-switch button{height:30px;padding:0 12px;border-radius:999px;border:1px solid transparent;background:transparent;color:var(--ctl-text);font-size:12px;font-weight:600}.window-switch button.active{border-color:rgba(37,99,235,0.35);background:rgba(37,99,235,0.12);color:#1d4ed8}.control-section{display:flex;flex-direction:column;align-items:flex-end;gap:6px;width:100%}.control-row{display:flex;flex-wrap:wrap;justify-content:flex-end;gap:12px;width:100%}.control-group{display:flex;flex-direction:column;align-items:flex-end;gap:4px;width:100%}.toolbar-control-group{width:auto}.control-host{display:flex;flex-wrap:wrap;justify-content:flex-end;gap:6px}.chart-wrap{position:relative;flex:1;min-height:0}.chart{width:100%;height:100%;min-height:420px;border:1px solid var(--chart-border);border-radius:8px;display:block;background:var(--panel-bg);box-sizing:border-box;flex:1}.map-tip{position:absolute;display:none;min-width:190px;max-width:240px;background:rgba(15,23,42,.96);color:#e2e8f0;border:1px solid rgba(148,163,184,.25);border-radius:10px;padding:10px 12px;font-size:12px;line-height:1.55;box-shadow:0 10px 28px rgba(2,6,23,.35);pointer-events:none;z-index:5}.runs{max-height:260px;overflow:auto}.runs table{width:100%;border-collapse:collapse}.runs th,.runs td{padding:6px 8px;border-bottom:1px solid var(--panel-border);font-size:12px;text-align:left}.tag{display:inline-block;padding:2px 8px;border-radius:999px;background:rgba(37,99,235,.12);color:#2563eb;font-size:12px}.log-box{margin-top:12px;max-height:240px;overflow:auto;white-space:pre-wrap;font:12px/1.5 Consolas,Monaco,'Courier New',monospace;border:1px solid var(--panel-border);border-radius:8px;background:var(--panel-bg);padding:10px;box-sizing:border-box}.footer{display:none}@media (max-width:1320px){.chart-head{grid-template-columns:1fr}.chart-toolbar,.control-section,.control-group{align-items:flex-start}.chart-toolbar-top,.toolbar-primary-controls,.control-host,.control-row{justify-content:flex-start}}@media (max-width:1100px){body{overflow:auto}.wrap{height:auto}.grid{grid-template-columns:1fr}.cards{grid-template-columns:repeat(2,minmax(0,1fr))}.chart{height:560px}}</style></head>
<body><div class="nav"><div class="nav-left"><div class="brand">ETH Liquidation Map</div><div class="menu"><a href="/">清算热区</a><a href="/config">模型配置</a><a href="/monitor">雷区监控</a><a href="/map">盘口汇总</a><a href="/liquidations">强平清算</a><a href="/bubbles">气泡图</a><a href="/webdatasource" class="active">页面数据源</a><a href="/channel">消息通道</a><a href="/analysis">日内分析</a></div></div><div class="nav-right"><div class="theme-toggle"><button class="label" type="button">主题</button><button id="themeDark" onclick="setTheme('dark')">深色</button><button id="themeLight" onclick="setTheme('light')">浅色</button></div></div></div>
<div class="wrap"><div class="grid"><div class="panel"><h2 style="margin:0 0 8px 0">抓取任务</h2><div id="statusBox" class="small">加载中...</div><div class="field" style="margin-top:12px"><label>抓取间隔（分钟）</label><input id="intervalMin" type="number" min="1" step="1"></div><div class="field" style="margin-top:12px"><label>超时（秒）</label><input id="timeoutSec" type="number" min="5" step="1"></div><div class="field" style="margin-top:12px"><label>Chrome 路径</label><input id="chromePath" placeholder="留空自动探测"></div><div class="field" style="margin-top:12px"><label>Profile 目录</label><input id="profileDir"></div><div class="row" style="margin-top:14px"><button class="primary" onclick="saveSettings()">保存设置</button><button onclick="initSession()">初始抓取</button><button onclick="runNow()">立即抓取</button><select id="runWindow"><option value="">抓取全部窗口</option><option value="1">仅 1 天</option><option value="7">仅 7 天</option><option value="30">仅 30 天</option></select></div><div class="small" id="saveMsg" style="margin-top:8px"></div><div id="stepLog" class="log-box">等待抓取日志...</div><div style="margin-top:16px"><div class="row" style="justify-content:space-between"><h3 style="margin:0">最近运行</h3><span class="tag" id="runState">-</span></div><div class="runs" style="margin-top:8px"><table><thead><tr><th>ID</th><th>窗口</th><th>状态</th><th>记录数</th><th>开始</th><th>错误</th></tr></thead><tbody id="runsBody"></tbody></table></div></div></div><div class="main-area"><div class="cards"><div class="card"><div class="k">当前窗口</div><div class="v" id="cardWindow">-</div></div><div class="card"><div class="k">多单总强度</div><div class="v" id="cardLong">-</div></div><div class="card"><div class="k">空单总强度</div><div class="v" id="cardShort">-</div></div><div class="card"><div class="k">最新抓取</div><div class="v" id="cardTime" style="font-size:16px">-</div></div></div><div class="panel chart-panel"><div class="chart-head"><div class="chart-head-main"><strong>ETH 页面数据源清算地图</strong><div class="small" id="chartMeta">加载中...</div><div class="small">滚轮缩放，按住鼠标左键左右拖动</div></div><div class="chart-toolbar" id="chartToolbar"><div class="chart-toolbar-top"><div id="toolbarPrimaryControls" class="toolbar-primary-controls"></div><input id="windowSel" type="hidden" value="30d"><div class="window-switch" role="group" aria-label="时间窗口"><button type="button" data-window-key="30d" onclick="setWindow('30d')">30D</button><button type="button" data-window-key="7d" onclick="setWindow('7d')">7D</button><button type="button" data-window-key="1d" onclick="setWindow('1d')">1D</button></div></div><div id="labelControlSection" class="control-section"></div></div></div><div class="chart-wrap"><canvas id="cv" class="chart" width="1000" height="520"></canvas><div id="mapTip" class="map-tip"></div></div></div></div></div></div><div id="globalFooter" class="footer">Code by Yuhao@jiansutech.com - loading - loading - loading</div>
<script>
function setTheme(t){const theme=(t==='dark')?'dark':'light';document.documentElement.setAttribute('data-theme',theme);try{localStorage.setItem('theme',theme);}catch(_){}const bd=document.getElementById('themeDark'),bl=document.getElementById('themeLight');if(bd)bd.classList.toggle('active',theme==='dark');if(bl)bl.classList.toggle('active',theme==='light');}
function initTheme(){let t='light';try{t=localStorage.getItem('theme')||'light';}catch(_){}setTheme(t);}
function fmtAmt(n){n=Number(n||0);if(!isFinite(n))return '-';if(n>=1e9)return (n/1e9).toFixed(2)+'B';if(n>=1e6)return (n/1e6).toFixed(2)+'M';if(n>=1e3)return (n/1e3).toFixed(2)+'K';return n.toFixed(2);}
function fmtYi(n){n=Number(n||0);if(!isFinite(n))return '-';return (n/1e8).toFixed(2)+'亿';}
function fmtPrice(n){n=Number(n||0);if(!isFinite(n))return '-';return n.toLocaleString('zh-CN',{minimumFractionDigits:1,maximumFractionDigits:1});}
function fmtTime(ts){if(!ts)return '-';return new Date(ts).toLocaleString('zh-CN',{hour12:false});}
function escHTML(v){return String(v||'').replace(/[&<>"']/g,c=>({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]));}
const sharedWindowDaysKey='preferred_window_days';
function saveSharedWindowDays(days){const n=Number(days);if(!(n===0||n===1||n===7||n===30))return;try{localStorage.setItem(sharedWindowDaysKey,String(n));}catch(_){}}
function loadSharedWindowDays(){try{const raw=localStorage.getItem(sharedWindowDaysKey);const n=Number(raw);if(n===0||n===1||n===7||n===30)return n;}catch(_){}return null;}
const bandLabelOptions=[10,20,30,40,50,60,70,80,90,100,150,200,250,300];
const bandLabelStorageKey='webdatasource_band_labels_by_window_v1';
const labelScaleStorageKey='webdatasource_label_scale_v1';
const top3LabelStorageKey='webdatasource_top3_labels_by_window_v1';
const labelScaleStep=0.1;
const labelScaleMin=0.7;
const labelScaleMax=1.6;
function normalizeWindowKey(key){key=String(key||'').toLowerCase();return(key==='1d'||key==='7d'||key==='30d')?key:'30d';}
function normalizeLabelScale(value){value=Number(value);if(!isFinite(value))return 1;value=Math.round(value/labelScaleStep)*labelScaleStep;if(value<labelScaleMin)value=labelScaleMin;if(value>labelScaleMax)value=labelScaleMax;return Math.round(value*100)/100;}
function loadLabelScale(){try{return normalizeLabelScale(localStorage.getItem(labelScaleStorageKey));}catch(_){return 1;}}
function defaultTop3LabelVisibility(){return{long:true,short:true};}
function normalizeTop3LabelVisibility(raw){const base=defaultTop3LabelVisibility();const out={long:base.long,short:base.short};if(raw&&typeof raw==='object'){if(typeof raw.long==='boolean')out.long=raw.long;if(typeof raw.short==='boolean')out.short=raw.short;}return out;}
function loadBandLabelSelections(){const empty={'1d':[],'7d':[],'30d':[]};try{const parsed=JSON.parse(localStorage.getItem(bandLabelStorageKey)||'{}');if(!parsed||typeof parsed!=='object')return empty;const out={};for(const key of Object.keys(empty)){const raw=Array.isArray(parsed[key])?parsed[key]:[];const seen=new Set();out[key]=[];for(const item of raw){const n=Number(item);if(!bandLabelOptions.includes(n)||seen.has(n))continue;seen.add(n);out[key].push(n);}out[key].sort((a,b)=>a-b);}return out;}catch(_){return empty;}}
function loadTop3LabelVisibility(){const windows={'1d':defaultTop3LabelVisibility(),'7d':defaultTop3LabelVisibility(),'30d':defaultTop3LabelVisibility()};try{const parsed=JSON.parse(localStorage.getItem(top3LabelStorageKey)||'{}');if(!parsed||typeof parsed!=='object')return windows;const out={};for(const key of Object.keys(windows)){out[key]=normalizeTop3LabelVisibility(parsed[key]);}return out;}catch(_){return windows;}}
let labelScale=loadLabelScale();
let bandLabelSelections=loadBandLabelSelections();
let top3LabelVisibility=loadTop3LabelVisibility();
function persistLabelScale(){try{localStorage.setItem(labelScaleStorageKey,String(labelScale));}catch(_){}}
function persistBandLabelSelections(){try{localStorage.setItem(bandLabelStorageKey,JSON.stringify(bandLabelSelections));}catch(_){}}
function persistTop3LabelVisibility(){try{localStorage.setItem(top3LabelStorageKey,JSON.stringify(top3LabelVisibility));}catch(_){}}
function selectedBandSet(windowKey){return new Set((bandLabelSelections[normalizeWindowKey(windowKey)]||[]).slice());}
function top3Visibility(windowKey){const key=normalizeWindowKey(windowKey);const current=top3LabelVisibility[key];const normalized=normalizeTop3LabelVisibility(current);top3LabelVisibility[key]=normalized;return normalized;}
function currentWindowKey(){const el=document.getElementById('windowSel');return normalizeWindowKey(el&&el.value);}
function syncWindowButtons(windowKey){const key=normalizeWindowKey(windowKey);for(const btn of document.querySelectorAll('[data-window-key]')){const active=btn.getAttribute('data-window-key')===key;btn.classList.toggle('active',active);btn.setAttribute('aria-pressed',active?'true':'false');}}
function setWindow(windowKey){const el=document.getElementById('windowSel');if(!el)return;const key=normalizeWindowKey(windowKey);el.value=key;syncWindowButtons(key);loadMap();}
function ensureBandControls(){let host=document.getElementById('bandControls');let sizeHost=document.getElementById('labelSizeControls');let top3Host=document.getElementById('top3Controls');if(host&&sizeHost&&top3Host)return host;const toolbar=document.getElementById('chartToolbar');if(!toolbar)return null;let section=document.getElementById('labelControlSection');if(!section){section=document.createElement('div');section.id='labelControlSection';section.className='control-section';toolbar.appendChild(section);}let topRow=document.getElementById('toolbarPrimaryControls');if(!topRow){topRow=document.createElement('div');topRow.id='toolbarPrimaryControls';topRow.className='toolbar-primary-controls';const toolbarTop=document.querySelector('.chart-toolbar-top');if(toolbarTop)toolbarTop.insertBefore(topRow,toolbarTop.firstChild);}if(!document.getElementById('top3Controls')){const top3Wrap=document.createElement('div');top3Wrap.className='control-group toolbar-control-group';const top3Label=document.createElement('div');top3Label.className='small';top3Label.textContent='多空Top3';top3Host=document.createElement('div');top3Host.id='top3Controls';top3Host.className='control-host';top3Wrap.appendChild(top3Label);top3Wrap.appendChild(top3Host);topRow.appendChild(top3Wrap);}if(!document.getElementById('labelSizeControls')){const sizeWrap=document.createElement('div');sizeWrap.className='control-group toolbar-control-group';const sizeLabel=document.createElement('div');sizeLabel.className='small';sizeLabel.textContent='标签大小';sizeHost=document.createElement('div');sizeHost.id='labelSizeControls';sizeHost.className='control-host';sizeHost.style.alignItems='center';sizeWrap.appendChild(sizeLabel);sizeWrap.appendChild(sizeHost);topRow.appendChild(sizeWrap);}if(!document.getElementById('bandControls')){const bandWrap=document.createElement('div');bandWrap.className='control-group';const label=document.createElement('div');label.className='small';label.textContent='标签档位';host=document.createElement('div');host.id='bandControls';host.className='control-host';bandWrap.appendChild(label);bandWrap.appendChild(host);section.appendChild(bandWrap);}return document.getElementById('bandControls');}
function renderLabelSizeControls(){ensureBandControls();const host=document.getElementById('labelSizeControls');if(!host)return;renderedLabelScaleValue=labelScale;const downDisabled=labelScale<=labelScaleMin+0.001,upDisabled=labelScale>=labelScaleMax-0.001;const btnStyle=disabled=>['height:28px','padding:0 10px','border-radius:999px','border:1px solid '+(disabled?'rgba(148,163,184,0.35)':'var(--ctl-border)'),'background:'+(disabled?'rgba(148,163,184,0.08)':'var(--ctl-bg)'),'color:'+(disabled?'#94a3b8':'var(--ctl-text)'),'font-size:12px','cursor:'+(disabled?'not-allowed':'pointer')].join(';');const valueStyle=['min-width:58px','height:28px','line-height:28px','padding:0 10px','border-radius:999px','background:rgba(37,99,235,0.10)','color:#1d4ed8','font-size:12px','font-weight:600','text-align:center'].join(';');host.innerHTML='<button type="button" onclick="adjustLabelScale(-'+labelScaleStep.toFixed(1)+')" '+(downDisabled?'disabled ':'')+'style="'+btnStyle(downDisabled)+'">A-</button><div style="'+valueStyle+'">'+Math.round(labelScale*100)+'%</div><button type="button" onclick="resetLabelScale()" style="'+btnStyle(false)+'">默认</button><button type="button" onclick="adjustLabelScale('+labelScaleStep.toFixed(1)+')" '+(upDisabled?'disabled ':'')+'style="'+btnStyle(upDisabled)+'">A+</button>';}
function renderTop3Controls(windowKey){ensureBandControls();const host=document.getElementById('top3Controls');if(!host)return;const key=normalizeWindowKey(windowKey);const visibility=top3Visibility(key);renderedTop3ControlsKey=key+'|'+(visibility.long?'1':'0')+(visibility.short?'1':'0');const item=(side,label)=>{const active=!!visibility[side];const style=['display:inline-flex','align-items:center','gap:6px','padding:4px 8px','border-radius:999px','border:1px solid '+(active?'rgba(37,99,235,0.45)':'var(--ctl-border)'),'background:'+(active?'rgba(37,99,235,0.10)':'var(--ctl-bg)'),'color:'+(active?'#1d4ed8':'var(--ctl-text)'),'font-size:12px','cursor:pointer','user-select:none'].join(';');return '<label style="'+style+'"><input type="checkbox" onchange="toggleTop3LabelVisibility(\''+side+'\')" '+(active?'checked ':'')+'style="margin:0;width:14px;height:14px;accent-color:#2563eb"><span>'+label+'</span></label>';};host.innerHTML=item('short','空单Top3')+item('long','多单Top3');}
function renderBandControls(windowKey){const host=ensureBandControls();if(!host)return;const key=normalizeWindowKey(windowKey);const selected=selectedBandSet(key);renderedBandControlsKey=key;host.innerHTML=bandLabelOptions.map(band=>{const active=selected.has(band);const style=['display:inline-flex','align-items:center','gap:6px','padding:4px 8px','border-radius:999px','border:1px solid '+(active?'rgba(37,99,235,0.45)':'var(--ctl-border)'),'background:'+(active?'rgba(37,99,235,0.10)':'var(--ctl-bg)'),'color:'+(active?'#1d4ed8':'var(--ctl-text)'),'font-size:12px','cursor:pointer','user-select:none'].join(';');return '<label style="'+style+'"><input type="checkbox" value="'+band+'" onchange="toggleBandLabel('+band+')" '+(active?'checked ':'')+'style="margin:0;width:14px;height:14px;accent-color:#2563eb"><span>'+band+'点</span></label>';}).join('');}
function toggleBandLabel(band){band=Number(band);if(!bandLabelOptions.includes(band))return;const key=currentWindowKey();const selected=selectedBandSet(key);if(selected.has(band))selected.delete(band);else selected.add(band);bandLabelSelections[normalizeWindowKey(key)]=bandLabelOptions.filter(value=>selected.has(value));persistBandLabelSelections();renderBandControls(key);draw();}
let currentMap=null;
let mapViewMin=null;
let mapViewMax=null;
let mapDrag=false;
let mapLastX=0;
let mapHoverMeta=null;
let mapBarHoverMeta=[];
let mapWindowKey='';
let renderedBandControlsKey='';
let renderedLabelScaleValue=null;
let renderedTop3ControlsKey='';
function adjustLabelScale(delta){const next=normalizeLabelScale(labelScale+Number(delta||0));if(Math.abs(next-labelScale)<0.001){renderLabelSizeControls();return;}labelScale=next;persistLabelScale();renderLabelSizeControls();draw();}
function resetLabelScale(){if(Math.abs(labelScale-1)<0.001){renderLabelSizeControls();return;}labelScale=1;persistLabelScale();renderLabelSizeControls();draw();}
function toggleTop3LabelVisibility(side){side=String(side||'').toLowerCase();if(side!=='long'&&side!=='short')return;const key=currentWindowKey();const visibility=top3Visibility(key);visibility[side]=!visibility[side];top3LabelVisibility[normalizeWindowKey(key)]={long:!!visibility.long,short:!!visibility.short};persistTop3LabelVisibility();renderTop3Controls(key);draw();}
function exKey(v){v=String(v||'').toLowerCase();if(v.includes('binance'))return 'binance';if(v.includes('okx'))return 'okx';if(v.includes('bybit'))return 'bybit';return 'other';}
const exStyle={binance:{fill:'rgba(255,131,0,0.58)',stroke:'rgba(255,131,0,0.96)',bg:'rgba(255,243,227,0.94)',text:'rgba(166,85,0,0.98)'},okx:{fill:'rgba(255,196,0,0.62)',stroke:'rgba(255,196,0,0.96)',bg:'rgba(255,248,214,0.94)',text:'rgba(153,118,0,0.98)'},bybit:{fill:'rgba(139,214,217,0.62)',stroke:'rgba(139,214,217,0.96)',bg:'rgba(234,249,249,0.94)',text:'rgba(48,119,123,0.98)'},other:{fill:'rgba(148,163,184,0.48)',stroke:'rgba(100,116,139,0.88)',bg:'rgba(241,245,249,0.94)',text:'rgba(71,85,105,0.98)'}};
const stackOrder=['binance','okx','bybit','other'];
function buildStackGroups(points){
const groups=new Map();
for(const pt of points||[]){const price=Number(pt.price||0),val=Number(pt.liq_value||0);if(!(price>0&&val>0))continue;const side=String(pt.side||'').toLowerCase()==='short'?'short':'long';const key=side+'|'+price.toFixed(4);let g=groups.get(key);if(!g){g={side,price,total:0,parts:{binance:0,okx:0,bybit:0,other:0}};groups.set(key,g);}const ex=exKey(pt.exchange);g.parts[ex]=(g.parts[ex]||0)+val;g.total+=val;}
return Array.from(groups.values()).sort((a,b)=>a.price-b.price);
}
function dominantExchange(g){let best='other',bestVal=0;for(const ex of stackOrder){const v=Number((g.parts||{})[ex]||0);if(v>bestVal){best=ex;bestVal=v;}}return best;}
function topStackLabels(groups){const out=[];for(const side of ['long','short']){out.push(...groups.filter(g=>g.side===side).sort((a,b)=>b.total-a.total).slice(0,3));}return out;}
function groupLabelKey(g){return String(g&&g.side||'')+'|'+Number(g&&g.price||0).toFixed(4);}
function buildCumulativeSeries(groups,side,minP,maxP){const arr=groups.filter(g=>g.side===side&&g.price>=minP&&g.price<=maxP&&g.total>0).sort((a,b)=>side==='long'?b.price-a.price:a.price-b.price);let run=0;return arr.map(g=>{run+=Number(g.total||0);return{price:Number(g.price||0),total:run};});}
function nearestBandGroup(allGroups,side,close,targetPrice){if(!(close>0))return null;let best=null,bestDiff=Infinity;for(const g of allGroups||[]){const price=Number(g&&g.price||0),total=Number(g&&g.total||0);if(!(price>0&&total>0)||String(g&&g.side||'')!==side)continue;if(side==='short'&&!(price>=close&&price<=targetPrice))continue;if(side==='long'&&!(price<=close&&price>=targetPrice))continue;const diff=Math.abs(price-targetPrice);if(diff<bestDiff||(diff===bestDiff&&best&&Math.abs(price-close)<Math.abs(Number(best.price||0)-close))){best=g;bestDiff=diff;}}return best;}
function bandRangeTotal(allGroups,side,close,targetPrice){if(!(close>0&&targetPrice>0))return 0;let total=0;for(const g of allGroups||[]){const price=Number(g&&g.price||0),val=Number(g&&g.total||0);if(!(price>0&&val>0)||String(g&&g.side||'')!==side)continue;if(side==='short'&&price>=close&&price<=targetPrice)total+=val;else if(side==='long'&&price<=close&&price>=targetPrice)total+=val;}return total;}
function buildBandLabelEntries(allGroups,close,minP,maxP){const out=[];const seen=new Set();for(const band of (bandLabelSelections[currentWindowKey()]||[])){for(const target of [{side:'short',price:close+band},{side:'long',price:close-band}]){if(!(target.price>=minP&&target.price<=maxP))continue;const g=nearestBandGroup(allGroups,target.side,close,target.price);if(!g)continue;const anchorPrice=Number(g.price||0);if(!(anchorPrice>=minP&&anchorPrice<=maxP))continue;const anchorKey=groupLabelKey(g);if(seen.has(anchorKey))continue;seen.add(anchorKey);out.push({side:target.side,price:anchorPrice,total:Number(g.total||0),display_price:Math.round(target.price*10)/10,cumulative:bandRangeTotal(allGroups,target.side,close,target.price),display_pct:close>0?((target.price-close)/close*100):0,anchor_price:anchorPrice,anchor_value:Number(g.total||0),anchor_key:anchorKey});}}return out;}
function sideGroupsForClose(groups,close){if(!(close>0))return groups;return groups.filter(g=>(g.side==='long'&&g.price<=close)||(g.side==='short'&&g.price>=close));}
function cumulativeAmountFromClose(groups,target,close){
if(!target||!(close>0)) return 0;
const side=String(target.side||'').toLowerCase()==='short'?'short':'long';
const targetPrice=Number(target.price||0);
if(!(targetPrice>0)) return 0;
let total=0;
for(const g of groups||[]){
const price=Number(g&&g.price||0),val=Number(g&&g.total||0),gSide=String(g&&g.side||'').toLowerCase()==='short'?'short':'long';
if(!(price>0&&val>0)||gSide!==side) continue;
if(side==='long'&&price<=close&&price>=targetPrice) total+=val;
if(side==='short'&&price>=close&&price<=targetPrice) total+=val;
}
return total;
}
function hideMapTip(){const tip=document.getElementById('mapTip');if(tip)tip.style.display='none';}
function showMapTip(hit,ev){
const tip=document.getElementById('mapTip');
const wrap=document.querySelector('.chart-wrap');
if(!tip||!wrap||!hit)return;
tip.innerHTML='<div>价格: $'+fmtPrice(hit.price)+'</div>'+
'<div>当前清算金额: '+fmtYi(hit.total)+'</div>'+
'<div>累计清算金额: '+fmtYi(hit.cumulative)+'</div>'+
'<div>到收盘价差异: '+((Number(hit.pct||0)>=0)?'+':'')+Number(hit.pct||0).toFixed(2)+'%</div>';
tip.style.display='block';
const wr=wrap.getBoundingClientRect();
let left=(ev.clientX-wr.left)+14,top=(ev.clientY-wr.top)+14;
tip.style.left='0px';tip.style.top='0px';
const tw=tip.offsetWidth||220,th=tip.offsetHeight||110;
if(left+tw>wr.width-10)left=Math.max(8,wr.width-10-tw);
if(top+th>wr.height-10)top=Math.max(8,wr.height-10-th);
tip.style.left=left+'px';tip.style.top=top+'px';
}
const sideCurveStyle={long:{fillTop:'rgba(239,68,68,0.24)',fillBottom:'rgba(239,68,68,0.08)',stroke:'rgba(239,68,68,0.96)'},short:{fillTop:'rgba(16,185,129,0.24)',fillBottom:'rgba(16,185,129,0.08)',stroke:'rgba(13,148,136,0.96)'}};
const sideLabelStyle={long:{bg:'rgba(254,242,242,0.96)',stroke:'rgba(239,68,68,0.96)',text:'rgba(185,28,28,0.98)',line:'rgba(239,68,68,0.96)'},short:{bg:'rgba(236,253,245,0.96)',stroke:'rgba(16,185,129,0.96)',text:'rgba(6,95,70,0.98)',line:'rgba(13,148,136,0.96)'}};
function rectsOverlap(a,b,pad=4){return !(a.x+a.w+pad<b.x||b.x+b.w+pad<a.x||a.y+a.h+pad<b.y||b.y+b.h+pad<a.y);}
function placeLabel(px,py,bw,bh,W,H,padB,occupied){
const gapX=Math.max(8,Math.round(8*labelScale)),aboveGap=Math.max(18,Math.round(bh*0.88)),belowGap=Math.max(10,Math.round(10*labelScale)),farAboveGap=Math.max(24,Math.round(bh*1.12)),farBelowGap=Math.max(18,Math.round(18*labelScale));
const tries=[{x:px+gapX,y:py-aboveGap},{x:px-bw-gapX,y:py-aboveGap},{x:px+gapX,y:py+belowGap},{x:px-bw-gapX,y:py+belowGap},{x:px-bw/2,y:py-farAboveGap},{x:px-bw/2,y:py+farBelowGap}];
for(const t of tries){let r={x:Math.max(6,Math.min(t.x,W-6-bw)),y:Math.max(6,Math.min(t.y,H-padB-bh)),w:bw,h:bh};if(!occupied.some(o=>rectsOverlap(r,o))){occupied.push(r);return r;}}
let r={x:Math.max(6,Math.min(px+gapX,W-6-bw)),y:Math.max(6,Math.min(py-aboveGap,H-padB-bh)),w:bw,h:bh};for(let step=0;step<8&&occupied.some(o=>rectsOverlap(r,o));step++){r.y=Math.max(6,Math.min(r.y+(step%2===0?1:-1)*(bh+Math.max(8,Math.round(8*labelScale)))*(Math.floor(step/2)+1),H-padB-bh));}
occupied.push(r);return r;
}
function labelLinesForGroup(g,close,allGroups){const displayPrice=Number(g&&((g.display_price!=null)?g.display_price:g.price)||0),total=Number(g&&g.total||0),pct=Number(g&&((g.display_pct!=null)?g.display_pct:(close>0?((Number(g&&g.price||0)-close)/close*100):0))||0),cum=(g&&g.cumulative!=null)?Number(g.cumulative||0):cumulativeAmountFromClose(allGroups,g,close);return['$'+fmtPrice(displayPrice),fmtYi(total),'累计'+fmtYi(cum),(pct>=0?'+':'')+pct.toFixed(2)+'%'];}
function drawGroupLabel(x,g,close,allGroups,sx,sy,W,H,padB,occupied){const anchorPrice=Number(g&&((g.anchor_price!=null)?g.anchor_price:g.price)||0),anchorVal=Number(g&&((g.anchor_value!=null)?g.anchor_value:g.total)||0);if(!(anchorPrice>0&&anchorVal>0))return;const lines=labelLinesForGroup(g,close,allGroups);const st=sideLabelStyle[g.side]||sideLabelStyle.long;const scale=normalizeLabelScale(labelScale),fontSize=Math.max(10,Math.round(12*scale)),lineHeight=Math.max(fontSize+1,Math.round(13*scale)),padX=Math.max(5,Math.round(5*scale)),padTop=Math.max(fontSize+2,Math.round(14*scale)),padBottom=Math.max(5,Math.round(6*scale));const prevFont=x.font;x.font='bold '+fontSize+'px sans-serif';let tw=0;for(const s of lines)tw=Math.max(tw,x.measureText(s).width);const bw=Math.ceil(tw+padX*2),bh=Math.ceil(padTop+lineHeight*(lines.length-1)+padBottom);const r=placeLabel(sx(anchorPrice),sy(anchorVal),bw,bh,W,H,padB,occupied);x.fillStyle=st.bg;x.strokeStyle=st.stroke;x.lineWidth=Math.max(1,Math.round(scale));x.fillRect(r.x,r.y,r.w,r.h);x.strokeRect(r.x,r.y,r.w,r.h);x.fillStyle=st.text;for(let i=0;i<lines.length;i++)x.fillText(lines[i],r.x+padX,r.y+padTop+i*lineHeight);x.font=prevFont;}
function drawCumulativeLine(x,series,sx,scy,by,side){
if(!series||series.length<2)return;
const st=sideCurveStyle[side]||sideCurveStyle.long;
const fillGrad=x.createLinearGradient(0,0,0,by);
fillGrad.addColorStop(0,st.fillTop);
fillGrad.addColorStop(1,st.fillBottom);
x.save();
x.beginPath();
x.moveTo(sx(series[0].price),by);
for(const point of series)x.lineTo(sx(point.price),scy(point.total));
x.lineTo(sx(series[series.length-1].price),by);
x.closePath();
x.fillStyle=fillGrad;
x.fill();
x.beginPath();
for(let i=0;i<series.length;i++){const point=series[i];if(i===0)x.moveTo(sx(point.price),scy(point.total));else x.lineTo(sx(point.price),scy(point.total));}
x.strokeStyle=st.stroke;
x.lineWidth=2.2;
x.stroke();
x.restore();
}
function drawWebDataSourceChart(x,W,H){
if(renderedBandControlsKey!==currentWindowKey())renderBandControls(currentWindowKey());
if(renderedLabelScaleValue!==labelScale)renderLabelSizeControls();
if(!renderedTop3ControlsKey||!renderedTop3ControlsKey.startsWith(currentWindowKey()+'|'))renderTop3Controls(currentWindowKey());
if(!currentMap||!currentMap.has_data||!(currentMap.points||[]).length){mapHoverMeta=null;x.fillStyle='#64748b';x.font='14px sans-serif';x.fillText('暂无有效抓取点位',20,24);return;}
const pts=currentMap.points||[],allGroups=buildStackGroups(pts);const baseMin=Number(currentMap.range_low||Math.min(...pts.map(p=>Number(p.price||0))));const baseMax=Number(currentMap.range_high||Math.max(...pts.map(p=>Number(p.price||0))));const padL=70,padR=78,padT=20,padB=40,pw=W-padL-padR,ph=H-padT-padB,by=padT+ph;const close=Number(currentMap.current_price||0);
if(!(baseMax>baseMin)){mapHoverMeta=null;x.fillStyle='#64748b';x.font='14px sans-serif';x.fillText('价格区间无效',20,24);return;}
if(mapViewMin==null||mapViewMax==null||!(mapViewMax>mapViewMin)){mapViewMin=baseMin;mapViewMax=baseMax;}
[mapViewMin,mapViewMax]=clampMapView(mapViewMin,mapViewMax,baseMin,baseMax);
const minP=mapViewMin,maxP=mapViewMax,span=Math.max(1e-6,maxP-minP);const groups=sideGroupsForClose(allGroups,close).filter(g=>g.price>=minP&&g.price<=maxP);
if(!groups.length){mapHoverMeta={pw:pw,padL:padL,padR:padR,baseMin:baseMin,baseMax:baseMax};x.fillStyle='#64748b';x.font='14px sans-serif';x.fillText('当前缩放范围内暂无点位',20,24);return;}
const maxV=Math.max(1,...groups.map(g=>Number(g.total||0)));const sx=v=>padL+((v-minP)/span)*pw,sy=v=>by-(v/maxV)*ph*0.86;
x.strokeStyle='#e5e7eb';x.font='12px sans-serif';
for(let i=0;i<=4;i++){const y=padT+ph*(i/4);const val=maxV*(1-i/4);x.beginPath();x.moveTo(padL,y);x.lineTo(W-padR,y);x.stroke();x.fillStyle='#64748b';x.fillText(fmtAmt(val),6,y+4);}
const priceCount=new Set(groups.map(g=>String(Number(g.price||0).toFixed(4)))).size||groups.length;const barW=Math.max(2,Math.min(10,pw/Math.max(80,priceCount*1.35)));
for(const g of groups){const px=sx(g.price);let acc=0;for(const ex of stackOrder){const val=Number((g.parts||{})[ex]||0);if(!(val>0))continue;const y0=sy(acc),y1=sy(acc+val),h=Math.max(1,y0-y1),st=exStyle[ex]||exStyle.other;x.fillStyle=st.fill;x.strokeStyle=st.stroke;x.fillRect(px-barW/2,y1,barW,h);x.strokeRect(px-barW/2,y1,barW,h);acc+=val;}const cum=cumulativeAmountFromClose(allGroups,g,close);const topY=sy(g.total);mapBarHoverMeta.push({x:px-barW/2-3,y:topY,w:barW+6,h:Math.max(8,by-topY),price:g.price,total:g.total,cumulative:cum,pct:close>0?((g.price-close)/close*100):0,side:g.side});}
const cumLong=buildCumulativeSeries(groups,'long',minP,maxP),cumShort=buildCumulativeSeries(groups,'short',minP,maxP);const maxCum=Math.max(0,...(cumLong.length?cumLong.map(it=>it.total):[0]),...(cumShort.length?cumShort.map(it=>it.total):[0]));
if(maxCum>0){const scy=v=>by-(v/maxCum)*ph;x.strokeStyle='#e5e7eb';x.beginPath();x.moveTo(W-padR,padT);x.lineTo(W-padR,by);x.stroke();x.fillStyle='#475569';x.fillText('累计',W-padR+8,padT-4);for(let i=0;i<=4;i++){const y=padT+ph*(i/4),val=maxCum*(1-i/4);x.fillStyle='#64748b';x.fillText(fmtAmt(val),W-padR+8,y+4);}drawCumulativeLine(x,cumLong,sx,scy,by,'long');drawCumulativeLine(x,cumShort,sx,scy,by,'short');}
x.fillStyle='#64748b';for(let i=0;i<=6;i++){const p=minP+span*(i/6),px=sx(p),label=fmtPrice(p),lw=x.measureText(label).width;x.fillText(label,Math.max(padL,Math.min(px-lw/2,W-padR-lw)),H-10);}
drawCurrentPriceMarker(x,close,sx,padT,by,W);
const top3State=top3Visibility(currentWindowKey()),bandLabels=buildBandLabelEntries(allGroups,close,minP,maxP),bandLabelKeys=new Set(bandLabels.map(groupLabelKey)),labels=topStackLabels(groups).filter(g=>!!top3State[String(g&&g.side||'').toLowerCase()==='short'?'short':'long']);x.font='bold 12px sans-serif';
const occupied=[];for(const g of bandLabels)drawGroupLabel(x,g,close,allGroups,sx,sy,W,H,padB,occupied);for(const g of labels){if(bandLabelKeys.has(groupLabelKey(g)))continue;drawGroupLabel(x,g,close,allGroups,sx,sy,W,H,padB,occupied);}
mapHoverMeta={pw:pw,padL:padL,padR:padR,baseMin:baseMin,baseMax:baseMax};
}
function drawCurrentPriceMarker(x,close,sx,padT,by,W){
if(!(close>0))return;
const px=sx(close);
if(!(px>=0&&px<=W))return;
const lineTop=padT+30;
const arrowTipY=padT+16;
const arrowBaseY=padT+28;
const arrowHalfW=5;
const label='最新价格:'+Number(close).toFixed(1);
let labelW=x.measureText(label).width;
let labelX=Math.max(6,Math.min(px-labelW/2,W-6-labelW));
const lineColor='#b91c1c';
const fillColor='rgba(255,255,255,0.96)';
x.save();
x.strokeStyle=lineColor;
x.fillStyle=lineColor;
x.lineWidth=2;
x.setLineDash([6,4]);
x.beginPath();
x.moveTo(px,lineTop);
x.lineTo(px,by);
x.stroke();
x.setLineDash([]);
x.beginPath();
x.moveTo(px,arrowTipY);
x.lineTo(px-arrowHalfW,arrowBaseY);
x.lineTo(px+arrowHalfW,arrowBaseY);
x.closePath();
x.fill();
x.fillStyle=fillColor;
x.fillRect(labelX-4,padT-1,labelW+8,16);
x.fillStyle='#374151';
x.font='12px sans-serif';
x.fillText(label,labelX,padT+11);
x.restore();
}
function clamp(v,a,b){return Math.max(a,Math.min(b,v));}
function resetMapView(){mapViewMin=null;mapViewMax=null;}
function clampMapView(minP,maxP,baseMin,baseMax){
const fullSpan=Math.max(1e-6,baseMax-baseMin);
const minSpan=Math.max(1e-4,fullSpan*0.08);
let span=Math.max(minSpan,maxP-minP);
span=Math.min(fullSpan,span);
let vMin=minP,vMax=vMin+span;
if(vMin<baseMin){vMin=baseMin;vMax=vMin+span;}
if(vMax>baseMax){vMax=baseMax;vMin=vMax-span;}
return [vMin,vMax];
}
function bindMapInteraction(){
const c=document.getElementById('cv');
if(!c||c.dataset.bound==='1')return;
c.dataset.bound='1';
c.addEventListener('mousedown',e=>{if(e.button!==0||!mapHoverMeta)return;mapDrag=true;mapLastX=e.clientX;hideMapTip();});
c.addEventListener('mousemove',e=>{if(mapDrag){hideMapTip();return;}const rect=c.getBoundingClientRect();const mx=e.clientX-rect.left,my=e.clientY-rect.top;let hit=null;for(const it of mapBarHoverMeta){if(mx>=it.x&&mx<=it.x+it.w&&my>=it.y&&my<=it.y+it.h){hit=it;break;}}if(hit)showMapTip(hit,e);else hideMapTip();});
c.addEventListener('mouseleave',()=>{hideMapTip();});
window.addEventListener('mouseup',()=>{mapDrag=false;});
window.addEventListener('mousemove',e=>{
if(!mapDrag||!mapHoverMeta||mapViewMin==null||mapViewMax==null)return;
const s=mapHoverMeta;
const pw=s.pw||1;
if(!(pw>0))return;
const dx=e.clientX-mapLastX;
mapLastX=e.clientX;
let vMin=mapViewMin-(dx/pw)*(mapViewMax-mapViewMin);
let vMax=mapViewMax-(dx/pw)*(mapViewMax-mapViewMin);
[vMin,vMax]=clampMapView(vMin,vMax,s.baseMin,s.baseMax);
mapViewMin=vMin;
mapViewMax=vMax;
draw();
});
c.addEventListener('wheel',e=>{
if(!mapHoverMeta)return;
e.preventDefault();
hideMapTip();
const s=mapHoverMeta;
const rect=c.getBoundingClientRect();
const pw=s.pw||Math.max(1,rect.width-(s.padL||70)-(s.padR||20));
if(!(pw>0))return;
const baseMin=s.baseMin,baseMax=s.baseMax;
const curMin=(mapViewMin==null?baseMin:mapViewMin);
const curMax=(mapViewMax==null?baseMax:mapViewMax);
let span=Math.max(1e-6,curMax-curMin);
const ratio=clamp((e.clientX-rect.left-(s.padL||70))/pw,0,1);
const focus=curMin+span*ratio;
const isPan=Math.abs(e.deltaX)>Math.abs(e.deltaY)||e.shiftKey;
if(isPan){
const d=(Math.abs(e.deltaX)>0?e.deltaX:e.deltaY);
let vMin=curMin-(d/pw)*span;
let vMax=curMax-(d/pw)*span;
[vMin,vMax]=clampMapView(vMin,vMax,baseMin,baseMax);
mapViewMin=vMin;
mapViewMax=vMax;
}else{
const full=Math.max(1e-6,baseMax-baseMin);
const nextSpan=clamp(span*(e.deltaY<0?0.88:1.14),full*0.08,full);
let vMin=focus-nextSpan*ratio;
let vMax=vMin+nextSpan;
[vMin,vMax]=clampMapView(vMin,vMax,baseMin,baseMax);
mapViewMin=vMin;
mapViewMax=vMax;
}
draw();
},{passive:false});
}
let localStepLogs=[];
function fmtLogClock(ts){if(!ts)return '--:--:--';return new Date(ts).toLocaleTimeString('zh-CN',{hour12:false});}
function logLabel(result){if(result==='success')return '成功';if(result==='failed')return '失败';return '进行中';}
function mergeStepLogs(serverLogs){const seen=new Set();const out=[];for(const it of [...localStepLogs,...(serverLogs||[])]){const key=[it.ts,it.step,it.result,it.detail].join('|');if(seen.has(key))continue;seen.add(key);out.push(it);}return out;}
function renderStepLogs(serverLogs,currentAction){const box=document.getElementById('stepLog');if(!box)return;const logs=mergeStepLogs(serverLogs);if(!logs.length){box.textContent=currentAction?('当前步骤: '+currentAction):'等待抓取日志...';return;}box.textContent=logs.map(it=>'['+fmtLogClock(it.ts)+'] ['+logLabel(it.result)+'] '+String(it.step||'-')+(it.detail?(' | '+String(it.detail)):'')).join('\n')+(currentAction?('\n['+fmtLogClock(Date.now())+'] [进行中] 当前步骤 | '+currentAction):'');box.scrollTop=box.scrollHeight;}
async function loadStatus(){const d=await fetch('/api/webdatasource/status').then(r=>r.json()).catch(()=>null);if(!d)return;document.getElementById('intervalMin').value=d.interval_min||15;document.getElementById('timeoutSec').value=d.timeout_sec||60;document.getElementById('chromePath').value=d.chrome_path||'';document.getElementById('profileDir').value=d.profile_dir||'';document.getElementById('statusBox').textContent='运行中: '+(d.running?'是':'否')+' | 默认间隔 '+(d.interval_min||15)+' 分钟 | 下次抓取 '+fmtTime(d.next_run_ts)+' | 超时 '+(d.timeout_sec||60)+' 秒 | 当前步骤 '+(d.running?(d.current_action||'-'):'-')+' | 最近成功 '+fmtTime(d.last_success_ts)+' | 最近错误 '+(d.last_error||'-');document.getElementById('runState').textContent=d.running?'抓取中':'空闲';renderStepLogs(d.step_logs||[],d.running?(d.current_action||''):'');const body=document.getElementById('runsBody');body.innerHTML='';for(const it of (d.recent_runs||[])){const err=escHTML(it.error_message||'-');const tr=document.createElement('tr');tr.innerHTML='<td>'+it.id+'</td><td>'+it.window_days+'天</td><td>'+escHTML(it.status)+'</td><td>'+it.records_count+'</td><td>'+fmtTime(it.started_at)+'</td><td title="'+err+'">'+err+'</td>';body.appendChild(tr);}return d;}
async function saveSettings(){const body={interval_min:Number(document.getElementById('intervalMin').value||15),timeout_sec:Number(document.getElementById('timeoutSec').value||60),chrome_path:document.getElementById('chromePath').value||'',profile_dir:document.getElementById('profileDir').value||''};const r=await fetch('/api/webdatasource/settings',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify(body)});document.getElementById('saveMsg').textContent=r.ok?'保存成功':'保存失败';loadStatus();}
async function initSession(){const msg=document.getElementById('saveMsg');localStepLogs=[{ts:Date.now(),step:'点击“初始抓取”',result:'success',detail:'正在打开 Coinglass 登录页并等待 90 秒'}];renderStepLogs([], '正在打开登录会话');const r=await fetch('/api/webdatasource/init',{method:'POST'});if(!r.ok){msg.textContent='初始化失败: '+await r.text();localStepLogs.push({ts:Date.now(),step:'点击“初始抓取”',result:'failed',detail:'服务器拒绝执行'});await loadStatus();return;}const ret=await r.json().catch(()=>({timeout_sec:90}));const timeoutSec=Math.max(90,Number(ret.timeout_sec||90));msg.textContent='Chrome 已打开，请在 '+timeoutSec+' 秒内登录 Coinglass，关闭前会自动保留 session';for(let i=0;i<timeoutSec+40;i++){await new Promise(res=>setTimeout(res,1000));const d=await loadStatus();if(!d)continue;if(!d.running){msg.textContent=d.last_error?('初始化失败: '+d.last_error):'初始抓取完成，Coinglass session 已保存';return;}}msg.textContent='初始化仍在进行，请稍后查看状态';}
async function runNow(){const raw=document.getElementById('runWindow').value;const body=raw?{window_days:Number(raw)}:{};const msg=document.getElementById('saveMsg');localStepLogs=[{ts:Date.now(),step:'点击“立刻抓取”',result:'success',detail:'已发送抓取请求'}];renderStepLogs([], '正在发送抓取请求');const r=await fetch('/api/webdatasource/run',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify(body)});if(!r.ok){msg.textContent='触发失败: '+await r.text();localStepLogs.push({ts:Date.now(),step:'点击“立刻抓取”',result:'failed',detail:'服务器拒绝执行'});await loadStatus();return;}msg.textContent='已触发，等待结果...';for(let i=0;i<120;i++){await new Promise(res=>setTimeout(res,1000));const d=await loadStatus();if(!d)continue;if(!d.running){const lr=d.last_run||{};if(String(lr.status)==='success'&&Number(lr.records_count||0)>0){msg.textContent='抓取成功: '+lr.records_count+' 条';loadMap();}else{msg.textContent='抓取失败: '+(lr.error_message||d.last_error||('记录数 '+(lr.records_count||0)));}return;}}msg.textContent='抓取仍在进行，请稍后查看最近运行';}
function draw(){
const c=document.getElementById('cv'),x=c.getContext('2d');const rect=c.getBoundingClientRect(),dpr=window.devicePixelRatio||1;const W=Math.max(760,Math.floor(rect.width)),H=Math.max(420,Math.floor(rect.height));c.width=W*dpr;c.height=H*dpr;x.setTransform(dpr,0,0,dpr,0,0);x.clearRect(0,0,W,H);x.fillStyle='#fff';x.fillRect(0,0,W,H);mapBarHoverMeta=[];hideMapTip();
return drawWebDataSourceChart(x,W,H);
}
async function loadMap(){const window=document.getElementById('windowSel').value;syncWindowButtons(window);saveSharedWindowDays(window==='30d'?30:(window==='7d'?7:(window==='1d'?1:null)));if(mapWindowKey!==window){resetMapView();mapWindowKey=window;}const d=await fetch('/api/webdatasource/map?window='+encodeURIComponent(window)).then(r=>r.json()).catch(()=>null);currentMap=d;if(!d||!d.has_data||!(d.points||[]).length){document.getElementById('chartMeta').textContent='暂无有效抓取点位'+(d&&d.last_error?(' | 最近错误: '+d.last_error):'');document.getElementById('cardWindow').textContent=window.toUpperCase();document.getElementById('cardLong').textContent='-';document.getElementById('cardShort').textContent='-';document.getElementById('cardTime').textContent='-';draw();return;}document.getElementById('chartMeta').textContent='窗口 '+window.toUpperCase()+' | 价格区间 '+fmtPrice(d.range_low)+' - '+fmtPrice(d.range_high);document.getElementById('cardWindow').textContent=window.toUpperCase();document.getElementById('cardLong').textContent=fmtAmt(d.long_total);document.getElementById('cardShort').textContent=fmtAmt(d.short_total);document.getElementById('cardTime').textContent=fmtTime(d.generated_at);draw();}
window.addEventListener('resize',draw);bindMapInteraction();initTheme();const windowSelEl=document.getElementById('windowSel');const sharedWindowDays=loadSharedWindowDays();const initialWindow=sharedWindowDays===1?'1d':(sharedWindowDays===7?'7d':'30d');if(windowSelEl)windowSelEl.value=initialWindow;syncWindowButtons(initialWindow);saveSharedWindowDays(initialWindow==='30d'?30:(initialWindow==='7d'?7:1));renderLabelSizeControls();renderTop3Controls(currentWindowKey());renderBandControls(currentWindowKey());loadStatus();loadMap();(async()=>{try{const r=await fetch('/api/version');const v=await r.json();const el=document.getElementById('globalFooter');if(el)el.textContent='Code by Yuhao@jiansutech.com - '+(v.commit_time||'-')+' - '+(v.commit_id||'-')+' - '+(v.branch||'-');}catch(_){}})();setInterval(loadStatus,2000);
</script></body></html>`
