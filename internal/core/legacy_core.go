package liqmap

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"io"
	"log"
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	_ "image/png"

	"github.com/chromedp/cdproto/emulation"
	"github.com/chromedp/chromedp"
	"github.com/gorilla/websocket"

	_ "modernc.org/sqlite"
)

const (
	defaultDBPath       = "data/liqmap.db"
	defaultSymbol       = "ETHUSDT"
	defaultServerAddr   = ":8888"
	defaultWindowDays   = 1
	windowIntraday      = 0
	defaultLookbackMin  = 1440
	defaultBucketMin    = 5
	defaultPriceStep    = 5.0
	defaultPriceRange   = 400.0
	fixedLeverageCSV    = "1,5,10,20,30,50,100"
	marketStateFreshAge = 10 * time.Minute
	// modelAlgoRev invalidates cached model_liqmap_snapshots when the model logic
	// changes in a way that affects outputs.
	modelAlgoRev int64 = 3
)

var bandSizes = []int{10, 20, 30, 40, 50, 60, 80, 100, 125, 150, 175, 200, 250, 300, 350, 400}

type App struct {
	db            *sql.DB
	httpClient    *http.Client
	ob            *OrderBookHub
	webds         *WebDataSourceManager
	mu            sync.RWMutex
	analysisMu    sync.Mutex
	analysisCache AnalysisSnapshot
	analysisAt    time.Time
	fitStatsMu    sync.Mutex
	fitStatsCount int
	fitStatsSE    float64
	fitStatsAt    time.Time
	fitStatsBusy  bool
	errLogMu      sync.Mutex
	errLogNext    map[string]time.Time
	apiGuardMu    sync.Mutex
	apiGuards     map[string]*ExchangeAPIGuard
	retrySignals  map[string]chan struct{}
	liqWSMu       sync.RWMutex
	liqWS         map[string]*liquidationWSState
	liqSymbolsMu  sync.RWMutex
	liqSymbols    map[string]struct{}
	windowDays    int
	debug         bool
	lastNotify    int64
	testSendMu    sync.Mutex
	testSending   bool
	bundleSendMu  sync.Mutex
	bundleSending bool
}

var errResnapshot = errors.New("periodic resnapshot")

const (
	exchangeErrorTripThreshold = 3
	exchangePauseDuration      = 180 * time.Minute
)

func getenv(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

func setupLogging(debug bool) (func(), error) {
	if !debug {
		return func() {}, nil
	}
	logPath := getenv("DEBUG_LOG", "log/server.log")
	if dir := filepath.Dir(logPath); dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("create debug log dir %s: %w", dir, err)
		}
	}
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open debug log %s: %w", logPath, err)
	}
	log.SetOutput(io.MultiWriter(os.Stdout, logFile))
	log.Printf("debug log enabled: %s", logPath)
	return func() {
		_ = logFile.Close()
	}, nil
}

func (a *App) window() int {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.windowDays
}

func (a *App) setWindow(days int) {
	a.mu.Lock()
	a.windowDays = days
	a.mu.Unlock()
}

func (a *App) modelLiquidationMap(windowDays, lookbackMin, bucketMin int, priceStep, priceRange float64) (map[string]any, error) {
	cfg := a.loadModelConfig()
	if windowDays <= 0 {
		windowDays = a.window()
	}
	defaultLookbackMin := lookbackMinForWindow(time.Now(), windowDays)
	lookbackOverridden := lookbackMin > 0
	if !lookbackOverridden {
		lookbackMin = defaultLookbackMin
	}
	bucketOverridden := bucketMin > 0
	if !bucketOverridden {
		bucketMin = cfg.BucketMin
	}
	stepOverridden := priceStep > 0
	if !stepOverridden {
		priceStep = cfg.PriceStep
	}
	rangeOverridden := priceRange > 0
	if !rangeOverridden {
		priceRange = cfg.PriceRange
	}
	configRev := a.getSettingInt64("model_config_rev", 0) + modelAlgoRev*1_000_000
	useCache := !lookbackOverridden && !bucketOverridden && !stepOverridden && !rangeOverridden && lookbackMin == defaultLookbackMin
	if useCache {
		if snap, ok := a.loadModelMapSnapshot(defaultSymbol, windowDays, configRev); ok {
			return snap, nil
		}
	}
	resp, err := a.buildModelLiquidationMap(defaultSymbol, lookbackMin, bucketMin, priceStep, priceRange, cfg)
	if err != nil {
		return nil, err
	}
	if useCache {
		a.saveModelMapSnapshot(defaultSymbol, windowDays, configRev, resp)
	}
	return resp, nil
}

func (a *App) fetchCoinGlassMap(symbol, window string) ([]byte, error) {
	apiKey := strings.TrimSpace(os.Getenv("CG_API_KEY"))
	if apiKey == "" {
		return nil, BadRequestError{Message: "CG_API_KEY is not set"}
	}

	symbol = strings.TrimSpace(symbol)
	if symbol == "" {
		symbol = "ETH"
	}
	window = strings.TrimSpace(window)
	if window == "" {
		window = "1d"
	}

	url := fmt.Sprintf("https://open-api-v4.coinglass.com/api/futures/liquidation/aggregated-map?symbol=%s&interval=%s", symbol, window)
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("CG-API-KEY", apiKey)

	resp, err := a.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("coinglass returned %s", resp.Status)
	}
	return body, nil
}

func (a *App) getSetting(key string) string {
	row := a.db.QueryRow(`SELECT value FROM app_settings WHERE key=?`, key)
	var value string
	if err := row.Scan(&value); err != nil {
		return ""
	}
	return value
}

func (a *App) getSettingInt(key string, fallback int) int {
	raw := strings.TrimSpace(a.getSetting(key))
	if raw == "" {
		return fallback
	}
	if v, err := strconv.Atoi(raw); err == nil {
		return v
	}
	return fallback
}

func (a *App) getSettingInt64(key string, fallback int64) int64 {
	raw := strings.TrimSpace(a.getSetting(key))
	if raw == "" {
		return fallback
	}
	if v, err := strconv.ParseInt(raw, 10, 64); err == nil {
		return v
	}
	return fallback
}

func (a *App) getSettingFloat(key string, fallback float64) float64 {
	raw := strings.TrimSpace(a.getSetting(key))
	if raw == "" {
		return fallback
	}
	if v, err := strconv.ParseFloat(raw, 64); err == nil {
		return v
	}
	return fallback
}

func (a *App) setSetting(key, value string) error {
	_, err := a.db.Exec(`INSERT INTO app_settings(key, value) VALUES(?, ?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, value)
	return err
}

func (a *App) loadModelMapSnapshot(symbol string, windowDays int, configRev int64) (map[string]any, bool) {
	var raw string
	if err := a.db.QueryRow(`SELECT payload_json FROM model_liqmap_snapshots
		WHERE symbol=? AND window_days=? AND config_rev=?
		ORDER BY generated_at DESC LIMIT 1`, symbol, windowDays, configRev).Scan(&raw); err != nil {
		return nil, false
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil, false
	}
	return out, true
}

func (a *App) saveModelMapSnapshot(symbol string, windowDays int, configRev int64, payload map[string]any) {
	if payload == nil {
		return
	}
	genAt := time.Now().UnixMilli()
	if v, ok := payload["generated_at"]; ok {
		if f, ok2 := v.(float64); ok2 && int64(f) > 0 {
			genAt = int64(f)
		}
	}
	payload["window_days"] = windowDays
	payload["config_rev"] = configRev
	b, err := json.Marshal(payload)
	if err != nil {
		return
	}
	_, _ = a.db.Exec(`INSERT INTO model_liqmap_snapshots(symbol, window_days, config_rev, generated_at, payload_json)
		VALUES(?, ?, ?, ?, ?)`, symbol, windowDays, configRev, genAt, string(b))
	cutoff := time.Now().Add(-6 * time.Hour).UnixMilli()
	_, _ = a.db.Exec(`DELETE FROM model_liqmap_snapshots
		WHERE symbol=? AND window_days=? AND config_rev=? AND generated_at<?`, symbol, windowDays, configRev, cutoff)
}

func (a *App) getLastNotifyTS() int64 {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.lastNotify
}

func (a *App) setLastNotifyTS(ts int64) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.lastNotify = ts
}

func (a *App) latestSuccessfulNotifyTS() int64 {
	var sentAt int64
	err := a.db.QueryRow(`SELECT sent_at
		FROM telegram_send_history
		WHERE group_name='monitor-30d-text' AND status='success'
		ORDER BY sent_at DESC
		LIMIT 1`).Scan(&sentAt)
	if err != nil {
		return 0
	}
	return sentAt
}

func (a *App) sendTelegramThirtyDayBundle(isTest bool) error {
	if !a.beginTelegramBundleSend() {
		return errors.New("telegram bundle send already running")
	}
	defer a.endTelegramBundleSend()
	return a.sendTelegramThirtyDayBundleLocked(isTest)
}

func (a *App) sendTelegramThirtyDayBundleLocked(isTest bool) error {
	const windowDays = 30
	sendMode := "auto"
	if isTest {
		sendMode = "test"
	}
	settings := a.loadSettings()

	webMap := a.webds.loadLatestMap("30d")
	displayPrice := a.resolveUnifiedDisplayPrice(webMap, 0)
	monitorReport, modelBands, err := a.buildModelHeatReportBundleForWindow(windowDays, displayPrice)
	if err != nil {
		return fmt.Errorf("build 30-day monitor report: %w", err)
	}
	monitorBands := buildMonitorHeatBandsFromWebMapAtPrice(webMap, displayPrice)
	if len(monitorBands) == 0 {
		monitorBands = modelBands
	}

	var errs []string
	var analysisSnapshot AnalysisSnapshot
	var analysisSnapshotLoaded bool
	loadAnalysisSnapshot := func() (AnalysisSnapshot, error) {
		if analysisSnapshotLoaded {
			return analysisSnapshot, nil
		}
		snapshot, err := a.BuildAnalysisSnapshot()
		if err != nil {
			return AnalysisSnapshot{}, err
		}
		analysisSnapshot = snapshot
		analysisSnapshotLoaded = true
		return analysisSnapshot, nil
	}

	if settings.Group1Enabled {
		if !webMap.HasData || len(webMap.Points) == 0 {
			errText := strings.TrimSpace(webMap.LastError)
			if errText == "" {
				errText = "no 30-day webdatasource snapshot available"
			}
			msg := fmt.Sprintf("数据缺失: %s", errText)
			a.recordTelegramSendHistory(sendMode, 1, "webdatasource-30d-image", "failed", msg)
			errs = append(errs, msg)
		} else {
			webImage, err := a.captureWebDataSourceScreenshotJPEG("30d")
			if err != nil {
				msg := fmt.Sprintf("截图失败: %v", err)
				a.recordTelegramSendHistory(sendMode, 1, "webdatasource-30d-image", "failed", msg)
				errs = append(errs, msg)
			} else if err := a.sendTelegramPhoto("", webImage); err != nil {
				msg := fmt.Sprintf("发送失败: %v", err)
				a.recordTelegramSendHistory(sendMode, 1, "webdatasource-30d-image", "failed", msg)
				errs = append(errs, msg)
			} else {
				a.recordTelegramSendHistory(sendMode, 1, "webdatasource-30d-image", "success", "")
			}
		}

	}

	if settings.Group2Enabled {
		monitorImage, err := a.captureMonitorScreenshotJPEG(windowDays)
		if err != nil {
			msg := fmt.Sprintf("截图失败: %v", err)
			a.recordTelegramSendHistory(sendMode, 2, "monitor-30d-image", "failed", msg)
			errs = append(errs, msg)
		} else if err := a.sendTelegramPhoto("", monitorImage); err != nil {
			msg := fmt.Sprintf("发送失败: %v", err)
			a.recordTelegramSendHistory(sendMode, 2, "monitor-30d-image", "failed", msg)
			errs = append(errs, msg)
		} else {
			a.recordTelegramSendHistory(sendMode, 2, "monitor-30d-image", "success", "")
		}

	}

	if settings.Group3Enabled {
		text := a.buildTelegramThirtyDayTextSafe(monitorReport, monitorBands, webMap)
		if err := a.sendTelegramText(text); err != nil {
			msg := fmt.Sprintf("发送失败: %v", err)
			a.recordTelegramSendHistory(sendMode, 3, "monitor-30d-text", "failed", msg)
			errs = append(errs, msg)
		} else {
			a.recordTelegramSendHistory(sendMode, 3, "monitor-30d-text", "success", "")
		}

	}

	if settings.Group4Enabled {
		analysisImage, err := a.captureAnalysisScreenshotJPEG()
		if err != nil {
			msg := fmt.Sprintf("截图失败: %v", err)
			a.recordTelegramSendHistory(sendMode, 4, "analysis-image", "failed", msg)
			errs = append(errs, msg)
		} else if err := a.sendTelegramPhoto("", analysisImage); err != nil {
			msg := fmt.Sprintf("发送失败: %v", err)
			a.recordTelegramSendHistory(sendMode, 4, "analysis-image", "failed", msg)
			errs = append(errs, msg)
		} else {
			a.recordTelegramSendHistory(sendMode, 4, "analysis-image", "success", "")
		}
	}

	if settings.Group5Enabled {
		snapshot, err := loadAnalysisSnapshot()
		if err != nil {
			msg := fmt.Sprintf("生成日内分析失败: %v", err)
			a.recordTelegramSendHistory(sendMode, 5, "analysis-text", "failed", msg)
			errs = append(errs, msg)
		} else if err := a.sendTelegramText(a.buildAnalysisTelegramTextSafe(snapshot)); err != nil {
			msg := fmt.Sprintf("发送失败: %v", err)
			a.recordTelegramSendHistory(sendMode, 5, "analysis-text", "failed", msg)
			errs = append(errs, msg)
		} else {
			a.recordTelegramSendHistory(sendMode, 5, "analysis-text", "success", "")
		}
	}

	if settings.Group6Enabled {
		structureImage, err := a.captureLiquidationsStructureScreenshotJPEG()
		if err != nil {
			msg := fmt.Sprintf("截图失败: %v", err)
			a.recordTelegramSendHistory(sendMode, 6, "liquidations-structure-image", "failed", msg)
			errs = append(errs, msg)
		} else if err := a.sendTelegramPhoto("", structureImage); err != nil {
			msg := fmt.Sprintf("发送失败: %v", err)
			a.recordTelegramSendHistory(sendMode, 6, "liquidations-structure-image", "failed", msg)
			errs = append(errs, msg)
		} else {
			a.recordTelegramSendHistory(sendMode, 6, "liquidations-structure-image", "success", "")
		}
		if err := a.sendTelegramText(a.buildLiquidationPatternQuestionAttachment()); err != nil {
			msg := fmt.Sprintf("发送失败: %v", err)
			a.recordTelegramSendHistory(sendMode, 6, "liquidations-pattern-text", "failed", msg)
			errs = append(errs, msg)
		} else {
			a.recordTelegramSendHistory(sendMode, 6, "liquidations-pattern-text", "success", "")
		}
	}

	if settings.Group7Enabled {
		bubblesImage, err := a.captureBubblesScreenshotJPEG()
		if err != nil {
			msg := fmt.Sprintf("截图失败: %v", err)
			a.recordTelegramSendHistory(sendMode, 7, "bubbles-5m-24h-image", "failed", msg)
			errs = append(errs, msg)
		} else if err := a.sendTelegramPhoto("", bubblesImage); err != nil {
			msg := fmt.Sprintf("发送失败: %v", err)
			a.recordTelegramSendHistory(sendMode, 7, "bubbles-5m-24h-image", "failed", msg)
			errs = append(errs, msg)
		} else {
			a.recordTelegramSendHistory(sendMode, 7, "bubbles-5m-24h-image", "success", "")
		}
	}

	if settings.Group8Enabled {
		var err error
		if isTest {
			err = a.sendLiquidationSyncAlertTest(sendMode)
		} else {
			err = a.maybeSendLiquidationSyncAlert(sendMode)
		}
		if err != nil {
			errs = append(errs, fmt.Sprintf("liquidations sync alert: %v", err))
		}
	}

	if len(errs) > 0 {
		return errors.New(strings.Join(errs, " | "))
	}
	return nil
}

func (a *App) startTelegramNotifier(ctx context.Context) {
	go func() {
		tk := time.NewTicker(30 * time.Second)
		defer tk.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tk.C:
				nowTime := time.Now()
				nextTS, ok := a.nextTelegramAutoNotifyTS(nowTime)
				if !ok {
					continue
				}
				now := nowTime.UnixMilli()
				if nextTS > now {
					continue
				}
				if !a.beginTelegramBundleSend() {
					continue
				}
				err := a.sendTelegramThirtyDayBundleLocked(false)
				a.endTelegramBundleSend()
				if err != nil {
					if a.debug {
						log.Printf("telegram auto bundle send failed: %v", err)
					}
					continue
				}
				a.setLastNotifyTS(time.Now().UnixMilli())
			}
		}
	}()
}

func (a *App) buildHeatReportMessage(d Dashboard) string {
	showBands := map[int]bool{10: true, 20: true, 30: true, 40: true, 50: true, 60: true, 80: true, 100: true, 150: true}

	lines := []string{
		"Heat report",
		fmt.Sprintf("Current price %.1f", d.CurrentPrice),
		"Window: 1d",
	}
	for _, b := range d.Bands {
		if !showBands[b.Band] {
			continue
		}
		lines = append(lines, fmt.Sprintf("%dpt up %.1f %.2fwan | down %.1f %.2fwan", b.Band, b.UpPrice, b.UpNotionalUSD/1e4, b.DownPrice, b.DownNotionalUSD/1e4))
	}
	return strings.Join(lines, "\n")
}

func (a *App) buildModelHeatReportData() (HeatReportData, error) {
	cfg := a.loadModelConfig()
	model, err := a.buildModelLiquidationMap(defaultSymbol, cfg.LookbackMin, cfg.BucketMin, cfg.PriceStep, cfg.PriceRange, cfg)
	if err != nil {
		return HeatReportData{}, err
	}
	return buildHeatReportDataFromModel(model), nil
}

func (a *App) buildModelHeatReportDataForWindow(windowDays int) (HeatReportData, error) {
	report, _, err := a.buildModelHeatReportBundleForWindow(windowDays, 0)
	return report, err
}

func (a *App) buildMonitorHeatBandsForWindow(windowDays int) ([]HeatReportBand, error) {
	_, bands, err := a.buildModelHeatReportBundleForWindow(windowDays, 0)
	return bands, err
}

func (a *App) buildModelHeatReportBundleForWindow(windowDays int, anchorPrice float64) (HeatReportData, []HeatReportBand, error) {
	cfg := a.loadModelConfig()
	lookbackMin := lookbackMinForWindow(time.Now(), windowDays)
	model, err := a.buildModelLiquidationMap(defaultSymbol, lookbackMin, cfg.BucketMin, cfg.PriceStep, cfg.PriceRange, cfg)
	if err != nil {
		return HeatReportData{}, nil, err
	}
	return buildHeatReportDataFromModelAtPrice(model, anchorPrice), buildMonitorHeatBandsFromModelAtPrice(model, anchorPrice), nil
}

type heatModelPoint struct {
	price float64
	value float64
}

func buildMonitorHeatBandsFromModel(model map[string]any) []HeatReportBand {
	return buildMonitorHeatBandsFromModelAtPrice(model, 0)
}

func buildMonitorHeatBandsFromWebMapAtPrice(webMap WebDataSourceMapResponse, anchorPrice float64) []HeatReportBand {
	if !webMap.HasData || len(webMap.Points) == 0 {
		return nil
	}
	current := roundDisplayPrice1(anchorPrice)
	if current <= 0 {
		current = roundDisplayPrice1(webMap.CurrentPrice)
	}
	if current <= 0 {
		return nil
	}
	type groupedPoint struct {
		side  string
		price float64
		total float64
	}
	groupMap := make(map[string]*groupedPoint)
	for _, pt := range webMap.Points {
		side := strings.ToLower(strings.TrimSpace(pt.Side))
		if side != "short" {
			side = "long"
		}
		if pt.Price <= 0 || pt.LiqValue <= 0 {
			continue
		}
		key := side + "|" + strconv.FormatFloat(pt.Price, 'f', 4, 64)
		group := groupMap[key]
		if group == nil {
			group = &groupedPoint{side: side, price: pt.Price}
			groupMap[key] = group
		}
		group.total += pt.LiqValue
	}
	if len(groupMap) == 0 {
		return nil
	}
	desired := []int{10, 20, 30, 40, 50, 60, 70, 80, 90, 100, 150, 200, 250, 300}
	out := make([]HeatReportBand, 0, len(desired))
	highlight := map[int]bool{20: true, 50: true, 80: true, 100: true, 200: true, 300: true}
	for _, band := range desired {
		upTotal, downTotal := 0.0, 0.0
		upBound := current + float64(band)
		downBound := current - float64(band)
		for _, group := range groupMap {
			switch group.side {
			case "short":
				if group.price >= current && group.price <= upBound {
					upTotal += group.total
				}
			default:
				if group.price <= current && group.price >= downBound {
					downTotal += group.total
				}
			}
		}
		out = append(out, HeatReportBand{
			Band:            band,
			UpPrice:         math.Round(upBound*10) / 10,
			UpNotionalUSD:   upTotal,
			DownPrice:       math.Round(downBound*10) / 10,
			DownNotionalUSD: downTotal,
			DiffUSD:         math.Abs(downTotal - upTotal),
			Highlight:       highlight[band],
		})
	}
	return out
}

func buildMonitorHeatBandsFromModelAtPrice(model map[string]any, anchorPrice float64) []HeatReportBand {
	current, points, ok := modelHeatPointsAndAnchor(model, anchorPrice)
	if !ok {
		return nil
	}
	desired := []int{10, 20, 30, 40, 50, 60, 70, 80, 90, 100, 150, 200, 250, 300}
	out := make([]HeatReportBand, 0, len(desired))
	highlight := map[int]bool{20: true, 50: true, 80: true, 100: true, 200: true, 300: true}
	for _, band := range desired {
		upTotal, downTotal := 0.0, 0.0
		for _, p := range points {
			dist := p.price - current
			if dist >= 0 && dist <= float64(band) {
				upTotal += p.value
			}
			if dist <= 0 && math.Abs(dist) <= float64(band) {
				downTotal += p.value
			}
		}
		out = append(out, HeatReportBand{
			Band:            band,
			UpPrice:         math.Round((current+float64(band))*10) / 10,
			UpNotionalUSD:   upTotal,
			DownPrice:       math.Round((current-float64(band))*10) / 10,
			DownNotionalUSD: downTotal,
			DiffUSD:         math.Abs(downTotal - upTotal),
			Highlight:       highlight[band],
		})
	}
	return out
}

func buildHeatReportDataFromModel(model map[string]any) HeatReportData {
	return buildHeatReportDataFromModelAtPrice(model, 0)
}

func buildHeatReportDataFromModelAtPrice(model map[string]any, anchorPrice float64) HeatReportData {
	current, points, ok := modelHeatPointsAndAnchor(model, anchorPrice)
	out := HeatReportData{GeneratedAt: time.Now().UnixMilli(), CurrentPrice: current}
	if v := toFloatAny(model["generated_at"]); v > 0 {
		out.GeneratedAt = int64(v)
	}
	if !ok {
		return out
	}
	bands := []int{10, 20, 30, 40, 50, 60, 70, 80, 90, 100, 150, 200, 250, 300}
	highlight := map[int]bool{20: true, 50: true, 80: true, 100: true, 200: true, 300: true}
	for _, band := range bands {
		upTotal, downTotal := 0.0, 0.0
		upPrice, downPrice := current+float64(band), current-float64(band)
		for _, p := range points {
			dist := p.price - current
			if dist >= 0 && dist <= float64(band) {
				upTotal += p.value
			}
			if dist <= 0 && math.Abs(dist) <= float64(band) {
				downTotal += p.value
			}
		}
		out.Bands = append(out.Bands, HeatReportBand{
			Band:            band,
			UpPrice:         math.Round(upPrice*10) / 10,
			UpNotionalUSD:   upTotal,
			DownPrice:       math.Round(downPrice*10) / 10,
			DownNotionalUSD: downTotal,
			DiffUSD:         math.Abs(downTotal - upTotal),
			Highlight:       highlight[band],
		})
	}
	for _, p := range points {
		if p.price >= current {
			out.ShortTotalUSD += p.value
			if p.value > out.ShortPeak.SingleUSD {
				out.ShortPeak = HeatReportPeak{Price: p.price, SingleUSD: p.value, Distance: math.Abs(p.price - current)}
			}
		}
		if p.price <= current {
			out.LongTotalUSD += p.value
			if p.value > out.LongPeak.SingleUSD {
				out.LongPeak = HeatReportPeak{Price: p.price, SingleUSD: p.value, Distance: math.Abs(p.price - current)}
			}
		}
	}
	for _, p := range points {
		if out.ShortPeak.Price > 0 && p.price >= current && p.price <= out.ShortPeak.Price {
			out.ShortPeak.CumulativeUSD += p.value
		}
		if out.LongPeak.Price > 0 && p.price <= current && p.price >= out.LongPeak.Price {
			out.LongPeak.CumulativeUSD += p.value
		}
	}
	return out
}

func modelHeatPointsAndAnchor(model map[string]any, anchorPrice float64) (float64, []heatModelPoint, bool) {
	current := toFloatAny(model["current_price"])
	prices, _ := model["prices"].([]float64)
	grid, _ := model["intensity_grid"].([][]float64)
	if current <= 0 || len(prices) == 0 || len(grid) == 0 {
		return 0, nil, false
	}
	if anchorPrice > 0 {
		current = math.Round(anchorPrice*10) / 10
	}
	points := make([]heatModelPoint, 0, len(prices))
	for i, p := range prices {
		if i >= len(grid) || p <= 0 {
			continue
		}
		sum := 0.0
		for _, v := range grid[i] {
			if v > 0 {
				sum += v
			}
		}
		if sum > 0 {
			points = append(points, heatModelPoint{price: p, value: sum})
		}
	}
	if len(points) == 0 {
		return 0, nil, false
	}
	return current, points, true
}

func (a *App) buildHeatReportCaption(r HeatReportData) string {
	lines := []string{
		fmt.Sprintf("ETH heat report | price $%.1f", r.CurrentPrice),
		fmt.Sprintf("long total %s yi | short total %s yi", formatYi1(r.LongTotalUSD), formatYi1(r.ShortTotalUSD)),
		"",
		"[long peak]",
		fmt.Sprintf("price $%.1f | distance %.0f", r.LongPeak.Price, r.LongPeak.Distance),
		fmt.Sprintf("single %s yi | cumulative %s yi", formatYi1(r.LongPeak.SingleUSD), formatYi1(r.LongPeak.CumulativeUSD)),
		"[short peak]",
		fmt.Sprintf("price $%.1f | distance %.0f", r.ShortPeak.Price, r.ShortPeak.Distance),
		fmt.Sprintf("single %s yi | cumulative %s yi", formatYi1(r.ShortPeak.SingleUSD), formatYi1(r.ShortPeak.CumulativeUSD)),
		"[summary]",
	}
	for _, band := range []int{20, 50, 80, 100} {
		b := heatReportBandBySize(r, band)
		total := b.UpNotionalUSD + b.DownNotionalUSD
		upPct, downPct := 0.0, 0.0
		if total > 0 {
			upPct = b.UpNotionalUSD / total * 100
			downPct = b.DownNotionalUSD / total * 100
		}
		bias := heatReportBias(b.UpNotionalUSD, b.DownNotionalUSD)
		lines = append(lines, fmt.Sprintf("%dpt up %s yi (%.0f%%) / down %s yi (%.0f%%) <b>%s</b>",
			band, formatYi2(b.UpNotionalUSD), upPct, formatYi2(b.DownNotionalUSD), downPct, bias))
	}
	return strings.Join(lines, "\n")
}

func (a *App) renderHeatReportPNG(r HeatReportData) ([]byte, error) {
	chromePath := detectChromePath()
	if chromePath == "" {
		return nil, errors.New("chrome/chromium executable not found")
	}
	var rows strings.Builder
	for i, b := range r.Bands {
		cls := ""
		if i%2 == 1 {
			cls += " alt"
		}
		if b.Highlight {
			cls += " hot"
		}
		diffClass := "diff-flat"
		if b.DownNotionalUSD > b.UpNotionalUSD {
			diffClass = "diff-long"
		} else if b.UpNotionalUSD > b.DownNotionalUSD {
			diffClass = "diff-short"
		}
		rows.WriteString(fmt.Sprintf(`<tr class="%s"><td>%d点内</td><td>%.1f</td><td>%.2f</td><td>%.1f</td><td>%.2f</td><td class="diff-cell %s">%.2f</td></tr>`,
			cls, b.Band, b.DownPrice, b.DownNotionalUSD/1e8, b.UpPrice, b.UpNotionalUSD/1e8, diffClass, b.DiffUSD/1e8))
	}
	longestDiffClass := "diff-flat"
	if r.LongPeak.SingleUSD > r.ShortPeak.SingleUSD {
		longestDiffClass = "diff-long"
	} else if r.ShortPeak.SingleUSD > r.LongPeak.SingleUSD {
		longestDiffClass = "diff-short"
	}
	rows.WriteString(fmt.Sprintf(`<tr class="longest"><td>鏈€闀挎煴</td><td>%.1f</td><td>%.2f</td><td>%.1f</td><td>%.2f</td><td class="diff-cell %s">%.2f</td></tr>`,
		r.LongPeak.Price, r.LongPeak.CumulativeUSD/1e8, r.ShortPeak.Price, r.ShortPeak.CumulativeUSD/1e8, longestDiffClass, math.Abs(r.LongPeak.CumulativeUSD-r.ShortPeak.CumulativeUSD)/1e8))
	html := `<!doctype html><html><head><meta charset="utf-8"><style>
body{margin:0;background:#fff;font-family:Arial,"Microsoft YaHei",sans-serif;color:#34445a}
.shot{width:974px;background:#fff}
table{width:974px;border-collapse:collapse;table-layout:fixed;font-weight:800;font-size:18px}
th,td{border:1px solid #f4f6f8;text-align:center;padding:8px 6px;height:24px}
.title th{background:#394c66;color:#fff;font-size:20px;padding:12px 6px}
.spot th{background:#394c66;color:#fff;text-align:left;padding-left:46px;font-size:18px}
.spot .price{text-align:left;padding-left:36px}
.group th{background:#d8dde5;color:#34445a}
.group .down{background:#f3e9c3;color:#c96f34}
.sub th{background:#d8dde5}.sub .down{background:#f5edc8;color:#c96f34}
tbody tr{background:#eeeeee}tbody tr.alt{background:#e0e0e0}tbody tr.hot{background:#d4d4d4}
tbody tr.longest{background:#e6e6e6}
td:nth-child(4),td:nth-child(5){color:#cf7436}
.diff-cell.diff-long{color:#cf7436}
.diff-cell.diff-short{color:#34445a}
.diff-cell.diff-flat{color:#34445a}
</style></head><body><div class="shot"><table>
<thead><tr class="title"><th colspan="6">ETH Heat Report (` + time.UnixMilli(r.GeneratedAt).Format("2006.1.2") + `)</th></tr>
<tr class="spot"><th colspan="1">ETH Price</th><th colspan="5" class="price">` + fmt.Sprintf("%.1f", r.CurrentPrice) + `</th></tr>
<tr class="group"><th rowspan="2">Band</th><th colspan="2" class="down">Lower Longs</th><th colspan="2">Upper Shorts</th><th rowspan="2">Diff (yi)</th></tr>
<tr class="sub"><th class="down">Price</th><th class="down">Size (yi)</th><th>Price</th><th>Size (yi)</th></tr></thead><tbody>` + rows.String() + `</tbody></table></div></body></html>`
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	opts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.ExecPath(chromePath),
		chromedp.Flag("headless", true),
		chromedp.WindowSize(974, 780),
	)
	allocCtx, cancelAlloc := chromedp.NewExecAllocator(ctx, opts...)
	defer cancelAlloc()
	taskCtx, cancelTask := chromedp.NewContext(allocCtx)
	defer cancelTask()
	var png []byte
	err := chromedp.Run(taskCtx,
		chromedp.Navigate("data:text/html;charset=utf-8,"+url.QueryEscape(html)),
		chromedp.Sleep(250*time.Millisecond),
		chromedp.FullScreenshot(&png, 100),
	)
	return png, err
}

func buildHeatReportTableHTML(r HeatReportData) string {
	var rows strings.Builder
	for i, b := range r.Bands {
		cls := ""
		if i%2 == 1 {
			cls += " alt"
		}
		if b.Highlight {
			cls += " hot"
		}
		diffClass := "diff-flat"
		if b.DownNotionalUSD > b.UpNotionalUSD {
			diffClass = "diff-long"
		} else if b.UpNotionalUSD > b.DownNotionalUSD {
			diffClass = "diff-short"
		}
		rows.WriteString(fmt.Sprintf(`<tr class="%s"><td>%d点内</td><td>%.1f</td><td>%.2f</td><td>%.1f</td><td>%.2f</td><td class="diff-cell %s">%.2f</td></tr>`,
			cls, b.Band, b.DownPrice, b.DownNotionalUSD/1e8, b.UpPrice, b.UpNotionalUSD/1e8, diffClass, b.DiffUSD/1e8))
	}
	longestDiffClass := "diff-flat"
	if r.LongPeak.SingleUSD > r.ShortPeak.SingleUSD {
		longestDiffClass = "diff-long"
	} else if r.ShortPeak.SingleUSD > r.LongPeak.SingleUSD {
		longestDiffClass = "diff-short"
	}
	rows.WriteString(fmt.Sprintf(`<tr class="longest"><td>鏈€闀挎煴</td><td>%.1f</td><td>%.2f</td><td>%.1f</td><td>%.2f</td><td class="diff-cell %s">%.2f</td></tr>`,
		r.LongPeak.Price, r.LongPeak.CumulativeUSD/1e8, r.ShortPeak.Price, r.ShortPeak.CumulativeUSD/1e8, longestDiffClass, math.Abs(r.LongPeak.CumulativeUSD-r.ShortPeak.CumulativeUSD)/1e8))
	return `<!doctype html><html><head><meta charset="utf-8"></head><body><table><thead>` +
		`<tr class="group"><th rowspan="2">Band</th><th colspan="2" class="down">Lower Longs</th><th colspan="2">Upper Shorts</th><th rowspan="2">Diff (yi)</th></tr>` +
		`<tr class="sub"><th class="down">Price</th><th class="down">Size (yi)</th><th>Price</th><th>Size (yi)</th></tr>` +
		`</thead><tbody>` + rows.String() + `</tbody></table></body></html>`
}

func pngToJPEG(pngBytes []byte, quality int) ([]byte, error) {
	if len(pngBytes) == 0 {
		return nil, errors.New("empty png image")
	}
	img, _, err := image.Decode(bytes.NewReader(pngBytes))
	if err != nil {
		return nil, err
	}
	if quality <= 0 || quality > 100 {
		quality = 95
	}
	var out bytes.Buffer
	if err := jpeg.Encode(&out, img, &jpeg.Options{Quality: quality}); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func captureElementJPEG(pageURL, selector string, width, height int, prepareScript, waitScript string) ([]byte, error) {
	return captureElementJPEGWithScale(pageURL, selector, width, height, 1, prepareScript, waitScript)
}

func captureElementJPEGWithScale(pageURL, selector string, width, height int, deviceScaleFactor float64, prepareScript, waitScript string) ([]byte, error) {
	chromePath := detectChromePath()
	if chromePath == "" {
		return nil, errors.New("chrome/chromium executable not found")
	}
	if deviceScaleFactor < 1 {
		deviceScaleFactor = 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	opts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.ExecPath(chromePath),
		chromedp.Flag("headless", true),
		chromedp.WindowSize(width, height),
	)
	allocCtx, cancelAlloc := chromedp.NewExecAllocator(ctx, opts...)
	defer cancelAlloc()
	taskCtx, cancelTask := chromedp.NewContext(allocCtx)
	defer cancelTask()

	var pngBytes []byte
	actions := []chromedp.Action{
		emulation.SetDeviceMetricsOverride(int64(width), int64(height), deviceScaleFactor, false),
		chromedp.Navigate(pageURL),
		chromedp.WaitReady("body", chromedp.ByQuery),
		chromedp.Sleep(600 * time.Millisecond),
	}
	if strings.TrimSpace(prepareScript) != "" {
		actions = append(actions, chromedp.EvaluateAsDevTools(prepareScript, nil))
	}
	if strings.TrimSpace(waitScript) != "" {
		actions = append(actions,
			chromedp.Poll(waitScript, nil, chromedp.WithPollingInterval(250*time.Millisecond), chromedp.WithPollingTimeout(40*time.Second)),
		)
	}
	actions = append(actions,
		chromedp.Sleep(800*time.Millisecond),
		chromedp.Screenshot(selector, &pngBytes, chromedp.NodeVisible, chromedp.ByQuery),
	)
	if err := chromedp.Run(taskCtx, actions...); err != nil {
		return nil, err
	}
	return pngToJPEG(pngBytes, 95)
}

func captureCanvasJPEGWithScale(pageURL, selector string, width, height int, deviceScaleFactor float64, prepareScript, waitScript string) ([]byte, error) {
	chromePath := detectChromePath()
	if chromePath == "" {
		return nil, errors.New("chrome/chromium executable not found")
	}
	if deviceScaleFactor < 1 {
		deviceScaleFactor = 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	opts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.ExecPath(chromePath),
		chromedp.Flag("headless", true),
		chromedp.WindowSize(width, height),
	)
	allocCtx, cancelAlloc := chromedp.NewExecAllocator(ctx, opts...)
	defer cancelAlloc()
	taskCtx, cancelTask := chromedp.NewContext(allocCtx)
	defer cancelTask()

	var dataURL string
	exportJS := fmt.Sprintf(`(function(){
		const c=document.querySelector(%q);
		if(!c||typeof c.toDataURL!=='function') return '';
		return c.toDataURL('image/png');
	})()`, selector)

	actions := []chromedp.Action{
		emulation.SetDeviceMetricsOverride(int64(width), int64(height), deviceScaleFactor, false),
		chromedp.Navigate(pageURL),
		chromedp.WaitReady("body", chromedp.ByQuery),
		chromedp.Sleep(600 * time.Millisecond),
	}
	if strings.TrimSpace(prepareScript) != "" {
		actions = append(actions, chromedp.EvaluateAsDevTools(prepareScript, nil))
	}
	if strings.TrimSpace(waitScript) != "" {
		actions = append(actions,
			chromedp.Poll(waitScript, nil, chromedp.WithPollingInterval(250*time.Millisecond), chromedp.WithPollingTimeout(40*time.Second)),
		)
	}
	actions = append(actions,
		chromedp.Sleep(800*time.Millisecond),
		chromedp.Evaluate(exportJS, &dataURL),
	)
	if err := chromedp.Run(taskCtx, actions...); err != nil {
		return nil, err
	}
	dataURL = strings.TrimSpace(dataURL)
	const prefix = "data:image/png;base64,"
	if !strings.HasPrefix(dataURL, prefix) {
		return nil, errors.New("canvas export returned empty image")
	}
	pngBytes, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(dataURL, prefix))
	if err != nil {
		return nil, err
	}
	return pngToJPEG(pngBytes, 95)
}

func (a *App) captureMonitorScreenshotJPEG(windowDays int) ([]byte, error) {
	pageURL := fmt.Sprintf("http://127.0.0.1%s/monitor?capture=1", defaultServerAddr)
	prepare := fmt.Sprintf(`(async()=>{ if (typeof setTheme==='function') setTheme('light'); if (typeof setWindow==='function') { await setWindow(%d); } return true; })()`, windowDays)
	wait := fmt.Sprintf(`(function(){ const btn=document.querySelector('.btns button[data-days=%q]'); const wrap=document.getElementById('heatReport'); const state=window.__monitorLoadState||{}; if(!btn || !btn.classList.contains('active') || !wrap) return false; if(state.pending) return false; if(!state.done) return false; if(Number(state.days||-1)!==%d) return false; if(state.error) return false; const table=wrap.querySelector('table'); const hint=(wrap.textContent||'').trim(); return !!(state.heatReady || table || hint.length>0); })()`, strconv.Itoa(windowDays), windowDays)
	return captureElementJPEG(pageURL, "#heatReport", 1440, 1200, prepare, wait)
}

func (a *App) captureWebDataSourceScreenshotJPEG(window string) ([]byte, error) {
	pageURL := fmt.Sprintf("http://127.0.0.1%s/webdatasource", defaultServerAddr)
	quotedWindow := strconv.Quote(window)
	prepare := `(async()=>{
		const targetWindow=` + quotedWindow + `;
		if (typeof setTheme==='function') setTheme('light');
		const emptyBands={'1d':[],'7d':[],'30d':[]};
		const top3On={'1d':{long:true,short:true},'7d':{long:true,short:true},'30d':{long:true,short:true}};
		try{
			localStorage.setItem('webdatasource_band_labels_by_window_v1', JSON.stringify(emptyBands));
			localStorage.setItem('webdatasource_top3_labels_by_window_v1', JSON.stringify(top3On));
			localStorage.setItem('webdatasource_label_scale_v1', '1');
			localStorage.setItem('preferred_window_days', targetWindow==='30d'?'30':(targetWindow==='7d'?'7':(targetWindow==='1d'?'1':'30')));
		}catch(_){}
		if(typeof bandLabelSelections!=='undefined') bandLabelSelections=emptyBands;
		if(typeof top3LabelVisibility!=='undefined') top3LabelVisibility=top3On;
		if(typeof labelScale!=='undefined') labelScale=1;
		if(typeof renderedBandControlsKey!=='undefined') renderedBandControlsKey='';
		if(typeof renderedTop3ControlsKey!=='undefined') renderedTop3ControlsKey='';
		if(typeof renderedLabelScaleValue!=='undefined') renderedLabelScaleValue=null;
		if(typeof mapViewMin!=='undefined') mapViewMin=null;
		if(typeof mapViewMax!=='undefined') mapViewMax=null;
		if(typeof mapWindowKey!=='undefined') mapWindowKey='';
		const sel=document.getElementById('windowSel');
		if (sel) sel.value=targetWindow;
		if (typeof renderLabelSizeControls==='function') renderLabelSizeControls();
		if (typeof renderTop3Controls==='function') renderTop3Controls(targetWindow);
		if (typeof renderBandControls==='function') renderBandControls(targetWindow);
		if (typeof loadMap==='function') {
			await loadMap();
		}
		await new Promise(resolve=>setTimeout(resolve,500));
		if (typeof draw==='function') draw();
		return true;
	})()`
	wait := `(function(){
		const targetWindow=` + quotedWindow + `;
		const sel=document.getElementById('windowSel');
		const cv=document.getElementById('cv');
		const mapReady=(typeof currentMap!=='undefined' && currentMap && currentMap.has_data && Array.isArray(currentMap.points) && currentMap.points.length>0);
		const top3Ready=(typeof top3LabelVisibility==='undefined')||((top3LabelVisibility&&top3LabelVisibility[targetWindow]&&top3LabelVisibility[targetWindow].long===true&&top3LabelVisibility[targetWindow].short===true));
		const bandsReady=(typeof bandLabelSelections==='undefined')||((bandLabelSelections&&Array.isArray(bandLabelSelections[targetWindow])&&bandLabelSelections[targetWindow].length===0));
		return !!(sel && sel.value===targetWindow && mapReady && cv && cv.width>0 && cv.height>0 && top3Ready && bandsReady);
	})()`
	return captureCanvasJPEGWithScale(pageURL, "#cv", 1480, 980, 2, prepare, wait)
}

func (a *App) captureAnalysisScreenshotJPEG() ([]byte, error) {
	pageURL := fmt.Sprintf("http://127.0.0.1%s/analysis", defaultServerAddr)
	prepare := `(async()=>{ window.scrollTo(0,0); return true; })()`
	wait := `(function(){
		const wrap=document.getElementById('analysisCapture');
		const title=document.getElementById('title');
		const indicators=document.querySelectorAll('#indicators .metric-card');
		const broadcast=document.getElementById('broadcastHeadline');
		if(!wrap || !title || !broadcast) return false;
		const titleText=(title.textContent||'').trim();
		const broadcastText=(broadcast.textContent||'').trim();
		if(!titleText || titleText==='加载中...' || titleText==='加载失败') return false;
		if(!broadcastText || broadcastText==='加载中...' || broadcastText==='加载失败') return false;
		return indicators.length > 0;
	})()`
	return captureElementJPEGWithScale(pageURL, "#analysisCapture", 1640, 1760, 1.6, prepare, wait)
}

func (a *App) buildAnalysisTelegramText(snapshot AnalysisSnapshot) string {
	lines := []string{
		fmt.Sprintf("<b>ETH 日内分析 | 现价$%s</b>", formatPrice1(snapshot.CurrentPrice)),
		escTelegramHTML(snapshot.Broadcast.Headline),
		escTelegramHTML(snapshot.Broadcast.Text),
		"",
		"<b>多周期指标</b>",
	}
	for _, it := range snapshot.Indicators {
		lines = append(lines, fmt.Sprintf("• <b>%s</b>: %s", escTelegramHTML(it.Label), escTelegramHTML(strings.TrimSpace(it.Value+" "+it.Subvalue))))
	}
	if len(snapshot.Broadcast.Bullets) > 0 {
		lines = append(lines, "", "<b>播报要点</b>")
		for _, it := range snapshot.Broadcast.Bullets {
			lines = append(lines, "• "+escTelegramHTML(it))
		}
	}
	return strings.Join(lines, "\n")
}

func (a *App) buildMonitorBundleCaption(windowDays int, dash Dashboard, report HeatReportData, isTest bool) string {
	b20 := bandRowOrDefault(dash.Bands, dash.CurrentPrice, 20)
	label := telegramSendLabel(isTest)
	return strings.Join([]string{
		fmt.Sprintf("<b>%s 1/3</b>", label),
		"<b>/monitor heat report</b>",
		fmt.Sprintf("<code>http://127.0.0.1:8888/monitor</code> | %d day window", windowDays),
		fmt.Sprintf("Price $%.1f | Updated %s", dash.CurrentPrice, time.UnixMilli(dash.GeneratedAt).Format("2006-01-02 15:04:05")),
		fmt.Sprintf("Within 20pt: upper %s yi / lower %s yi", formatYi2(b20.UpNotionalUSD), formatYi2(b20.DownNotionalUSD)),
	}, "\n")
}

func (a *App) buildWebDataSourceBundleCaption(windowDays int, m WebDataSourceMapResponse, isTest bool) string {
	label := telegramSendLabel(isTest)
	return strings.Join([]string{
		fmt.Sprintf("<b>%s 2/3</b>", label),
		"<b>/webdatasource ETH web datasource map</b>",
		fmt.Sprintf("<code>http://127.0.0.1:8888/webdatasource</code> | %d day window", windowDays),
		fmt.Sprintf("Range $%.1f - $%.1f | Updated %s", m.RangeLow, m.RangeHigh, time.UnixMilli(m.GeneratedAt).Format("2006-01-02 15:04:05")),
		fmt.Sprintf("Web longs %s yi | Web shorts %s yi", formatYi2(m.LongTotal), formatYi2(m.ShortTotal)),
	}, "\n")
}

func (a *App) buildHeatReportInterpretation(windowDays int, dash Dashboard, monitor HeatReportData, webMap WebDataSourceMapResponse, isTest bool) string {
	monitorBands := buildMonitorHeatBandsFromWebMapAtPrice(webMap, monitor.CurrentPrice)
	return a.buildTelegramThirtyDayTextV4(monitor, monitorBands, webMap)
}

func (a *App) resolveUnifiedDisplayPrice(webMap WebDataSourceMapResponse, fallback float64) float64 {
	if webMap.CurrentPrice > 0 {
		return roundDisplayPrice1(webMap.CurrentPrice)
	}
	return roundDisplayPrice1(fallback)
}

func (a *App) resolveTelegramDisplayPrice(fallback float64) float64 {
	return a.resolveUnifiedDisplayPrice(WebDataSourceMapResponse{}, fallback)
}

func (a *App) enrichHeatReportForTelegram(monitor HeatReportData, webMap WebDataSourceMapResponse) HeatReportData {
	display := monitor
	price := roundDisplayPrice1(monitor.CurrentPrice)
	if price <= 0 {
		price = a.resolveUnifiedDisplayPrice(webMap, monitor.CurrentPrice)
	}
	if price <= 0 {
		return display
	}
	display.CurrentPrice = price
	if display.LongPeak.Price > 0 {
		display.LongPeak.Distance = math.Abs(display.LongPeak.Price - price)
	}
	if display.ShortPeak.Price > 0 {
		display.ShortPeak.Distance = math.Abs(display.ShortPeak.Price - price)
	}
	return display
}

func resolveTelegramPeakFromWebMap(currentPrice float64, side string, webMap WebDataSourceMapResponse) (HeatReportPeak, bool) {
	if !webMap.HasData || len(webMap.Points) == 0 {
		return HeatReportPeak{}, false
	}
	side = strings.ToLower(strings.TrimSpace(side))
	if side != "short" {
		side = "long"
	}
	type groupedPoint struct {
		price float64
		total float64
	}
	groupMap := make(map[string]*groupedPoint)
	for _, pt := range webMap.Points {
		ptSide := strings.ToLower(strings.TrimSpace(pt.Side))
		if ptSide != side || pt.Price <= 0 || pt.LiqValue <= 0 {
			continue
		}
		key := strconv.FormatFloat(pt.Price, 'f', 4, 64)
		group := groupMap[key]
		if group == nil {
			group = &groupedPoint{price: pt.Price}
			groupMap[key] = group
		}
		group.total += pt.LiqValue
	}
	if len(groupMap) == 0 {
		return HeatReportPeak{}, false
	}

	peak := HeatReportPeak{}
	for _, group := range groupMap {
		if group.total > peak.SingleUSD {
			peak.Price = group.price
			peak.SingleUSD = group.total
			continue
		}
		if group.total == peak.SingleUSD && peak.Price > 0 {
			groupDist := math.Abs(group.price - currentPrice)
			peakDist := math.Abs(peak.Price - currentPrice)
			if groupDist < peakDist || (groupDist == peakDist && group.price < peak.Price) {
				peak.Price = group.price
			}
		}
	}
	if peak.Price <= 0 || peak.SingleUSD <= 0 {
		return HeatReportPeak{}, false
	}
	if currentPrice > 0 {
		peak.Distance = math.Abs(peak.Price - currentPrice)
	}
	for _, group := range groupMap {
		switch side {
		case "short":
			if currentPrice > 0 {
				if group.price < currentPrice || group.price > peak.Price {
					continue
				}
			} else if group.price > peak.Price {
				continue
			}
		default:
			if currentPrice > 0 {
				if group.price > currentPrice || group.price < peak.Price {
					continue
				}
			} else if group.price < peak.Price {
				continue
			}
		}
		peak.CumulativeUSD += group.total
	}
	if peak.CumulativeUSD < peak.SingleUSD {
		peak.CumulativeUSD = peak.SingleUSD
	}
	return peak, true
}

func (a *App) alignTelegramTextHeatReport(monitor HeatReportData, webMap WebDataSourceMapResponse) HeatReportData {
	display := a.enrichHeatReportForTelegram(monitor, webMap)
	if webMap.LongTotal > 0 {
		display.LongTotalUSD = webMap.LongTotal
	}
	if webMap.ShortTotal > 0 {
		display.ShortTotalUSD = webMap.ShortTotal
	}
	if peak, ok := resolveTelegramPeakFromWebMap(display.CurrentPrice, "long", webMap); ok {
		display.LongPeak = peak
	}
	if peak, ok := resolveTelegramPeakFromWebMap(display.CurrentPrice, "short", webMap); ok {
		display.ShortPeak = peak
	}
	return display
}

func buildTelegramImbalanceLine(b HeatReportBand) string {
	sign := ""
	switch {
	case b.DownNotionalUSD > b.UpNotionalUSD:
		sign = "+"
	case b.UpNotionalUSD > b.DownNotionalUSD:
		sign = "-"
	}
	return fmt.Sprintf(
		"<b>%d点内</b> %s（%s%s亿）",
		b.Band,
		telegramImbalanceLabel(b.UpNotionalUSD, b.DownNotionalUSD),
		sign,
		formatYi2(math.Abs(b.DownNotionalUSD-b.UpNotionalUSD)),
	)
}

func (a *App) buildTelegramThirtyDayTextV4(monitor HeatReportData, monitorBands []HeatReportBand, webMap WebDataSourceMapResponse) string {
	return a.buildTelegramThirtyDayTextSafe(monitor, monitorBands, webMap)
}

func escTelegramHTML(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")
	return r.Replace(strings.TrimSpace(s))
}

func maskSensitive(s string) string {
	s = strings.TrimSpace(s)
	if len(s) <= 8 {
		return s
	}
	return s[:4] + strings.Repeat("*", len(s)-8) + s[len(s)-4:]
}

func (a *App) startCollector(ctx context.Context) {
	intervalSec := 30
	if raw := strings.TrimSpace(os.Getenv("COLLECT_INTERVAL_SEC")); raw != "" {
		if v, err := strconv.Atoi(raw); err == nil && v >= 10 {
			intervalSec = v
		}
	}
	interval := time.Duration(intervalSec) * time.Second
	go func() {
		if err := a.collectAndStore(defaultSymbol); err != nil && a.debug {
			log.Printf("collector initial run failed: %v", err)
		}
		tk := time.NewTicker(interval)
		defer tk.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tk.C:
				if err := a.collectAndStore(defaultSymbol); err != nil && a.debug {
					log.Printf("collector run failed: %v", err)
				}
			}
		}
	}()
}

func (a *App) collectAndStore(symbol string) error {
	now := time.Now().UnixMilli()
	snapshots := make([]Snapshot, 0, 3)
	allFailuresPaused := true
	if s, err := a.fetchBinanceSnapshot(symbol); err == nil {
		allFailuresPaused = false
		snapshots = append(snapshots, s)
	} else if a.debug {
		if !isExchangePausedError(err) {
			allFailuresPaused = false
			log.Printf("binance fetch failed: %v", err)
		}
	} else if err != nil && !isExchangePausedError(err) {
		allFailuresPaused = false
	}
	if s, err := a.fetchBybitSnapshot(symbol); err == nil {
		allFailuresPaused = false
		snapshots = append(snapshots, s)
	} else if a.debug {
		if !isExchangePausedError(err) {
			allFailuresPaused = false
			log.Printf("bybit fetch failed: %v", err)
		}
	} else if err != nil && !isExchangePausedError(err) {
		allFailuresPaused = false
	}
	if s, err := a.fetchOKXSnapshot("ETH-USDT-SWAP"); err == nil {
		allFailuresPaused = false
		s.Symbol = symbol
		snapshots = append(snapshots, s)
	} else if a.debug {
		if !isExchangePausedError(err) {
			allFailuresPaused = false
			log.Printf("okx fetch failed: %v", err)
		}
	} else if err != nil && !isExchangePausedError(err) {
		allFailuresPaused = false
	}
	if len(snapshots) == 0 {
		if allFailuresPaused {
			return nil
		}
		return errors.New("all market fetches failed")
	}
	for _, s := range snapshots {
		if err := a.upsertMarketState(s); err != nil {
			return err
		}
		_ = a.insertOISnapshot(s)
	}
	_ = now
	return nil
}

func (a *App) insertOISnapshot(s Snapshot) error {
	if s.MarkPrice <= 0 || s.OIValueUSD <= 0 {
		return nil
	}
	_, err := a.db.Exec(`INSERT INTO oi_snapshots(exchange, symbol, mark_price, oi_value_usd, funding_rate, long_short_ratio, updated_ts)
		VALUES(?, ?, ?, ?, ?, ?, ?)`,
		strings.ToLower(strings.TrimSpace(s.Exchange)), s.Symbol, s.MarkPrice, s.OIValueUSD, s.FundingRate, nullableFloat(s.LongShortRatio), s.UpdatedTS)
	return err
}

func (a *App) upsertMarketState(s Snapshot) error {
	_, err := a.db.Exec(`INSERT INTO market_state(exchange, symbol, mark_price, oi_qty, oi_value_usd, funding_rate, long_short_ratio, updated_ts)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(exchange, symbol) DO UPDATE SET
			mark_price=excluded.mark_price,
			oi_qty=excluded.oi_qty,
			oi_value_usd=excluded.oi_value_usd,
			funding_rate=excluded.funding_rate,
			long_short_ratio=excluded.long_short_ratio,
			updated_ts=excluded.updated_ts`,
		s.Exchange, s.Symbol, s.MarkPrice, s.OIQty, s.OIValueUSD, s.FundingRate, nullableFloat(s.LongShortRatio), s.UpdatedTS)
	return err
}

func (a *App) insertBandAndLongest(symbol string, snapshots []Snapshot, nowTS int64) error {
	if len(snapshots) == 0 {
		return nil
	}
	states := make([]MarketState, 0, len(snapshots))
	avgFunding := 0.0
	for _, s := range snapshots {
		oiQty := s.OIQty
		oiUSD := s.OIValueUSD
		funding := s.FundingRate
		states = append(states, MarketState{
			Exchange:    s.Exchange,
			Symbol:      s.Symbol,
			MarkPrice:   s.MarkPrice,
			OIQty:       &oiQty,
			OIValueUSD:  &oiUSD,
			FundingRate: &funding,
			UpdatedTS:   s.UpdatedTS,
		})
		avgFunding += s.FundingRate
	}
	avgFunding /= float64(len(snapshots))
	current := averageSnapshotPrice(snapshots)
	current = math.Round(current*10) / 10
	if current <= 0 {
		return nil
	}

	tx, err := a.db.Begin()
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	var maxLongBand int
	var maxLongPrice, maxLongNotional float64
	var maxShortBand int
	var maxShortPrice, maxShortNotional float64
	prevUpTotal := 0.0
	prevDownTotal := 0.0

	leverageLevels := []float64{8, 10, 12, 15, 20, 25, 30, 40, 50, 75, 100}
	levelWeights := []float64{0.07, 0.09, 0.11, 0.13, 0.14, 0.13, 0.11, 0.1, 0.07, 0.03, 0.02}
	weightSum := 0.0
	for _, w := range levelWeights {
		weightSum += w
	}
	for i := range levelWeights {
		levelWeights[i] /= weightSum
	}

	for _, band := range bandSizes {
		bandF := float64(band)
		upNotional := 0.0
		downNotional := 0.0
		for _, s := range snapshots {
			exchangeOI := s.OIValueUSD
			if exchangeOI <= 0 {
				continue
			}
			// funding > 0 usually means longs are more crowded, so downside liquidation intensity should be higher.
			longCrowd := clamp(0.52+s.FundingRate*7000, 0.22, 0.82)
			shortCrowd := 1 - longCrowd
			for i, lev := range leverageLevels {
				w := levelWeights[i]
				dist := current * (0.88 / lev)
				spread := math.Max(6.0, dist*0.12)
				cdf := 1.0 / (1.0 + math.Exp(-(bandF-dist)/spread))
				upNotional += exchangeOI * shortCrowd * w * cdf
				downNotional += exchangeOI * longCrowd * w * cdf
			}
		}

		// global funding tilt to avoid symmetric curves
		globalLongTilt := clamp(0.5+avgFunding*4000, 0.35, 0.65)
		downNotional *= (0.8 + 0.4*globalLongTilt)
		upNotional *= (0.8 + 0.4*(1-globalLongTilt))

		upPrice := current + float64(band)
		downPrice := current - float64(band)
		if downPrice <= 0 {
			downPrice = current * 0.3
		}

		if _, err = tx.Exec(`INSERT INTO band_reports(report_ts, symbol, current_price, band, up_price, up_notional_usd, down_price, down_notional_usd)
			VALUES(?, ?, ?, ?, ?, ?, ?, ?)`, nowTS, symbol, current, band, upPrice, upNotional, downPrice, downNotional); err != nil {
			return err
		}
		upInc := math.Max(0, upNotional-prevUpTotal)
		downInc := math.Max(0, downNotional-prevDownTotal)
		prevUpTotal = upNotional
		prevDownTotal = downNotional

		if upInc > maxShortNotional {
			maxShortNotional = upInc
			maxShortPrice = upPrice
			maxShortBand = band
		}
		if downInc > maxLongNotional {
			maxLongNotional = downInc
			maxLongPrice = downPrice
			maxLongBand = band
		}
	}

	if _, err = tx.Exec(`INSERT INTO longest_bar_reports(report_ts, symbol, side, bucket_size, bucket_price, bucket_notional_usd)
		VALUES(?, ?, 'short', ?, ?, ?)`, nowTS, symbol, maxShortBand, maxShortPrice, maxShortNotional); err != nil {
		return err
	}
	if _, err = tx.Exec(`INSERT INTO longest_bar_reports(report_ts, symbol, side, bucket_size, bucket_price, bucket_notional_usd)
		VALUES(?, ?, 'long', ?, ?, ?)`, nowTS, symbol, maxLongBand, maxLongPrice, maxLongNotional); err != nil {
		return err
	}
	return tx.Commit()
}

func exchangeFromEndpoint(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	host := strings.ToLower(strings.TrimSpace(u.Hostname()))
	switch {
	case strings.Contains(host, "binance."):
		return "binance"
	case strings.Contains(host, "bybit."):
		return "bybit"
	case strings.Contains(host, "okx."):
		return "okx"
	default:
		return ""
	}
}

func collapseExchangeErrorText(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	raw = strings.Join(strings.Fields(raw), " ")
	if len(raw) > 240 {
		raw = raw[:240]
	}
	return raw
}

func exchangeHTTPErrorKey(statusCode int, body string) (string, string) {
	text := collapseExchangeErrorText(body)
	if text == "" {
		text = fmt.Sprintf("http %d", statusCode)
	}
	return fmt.Sprintf("%d|%s", statusCode, text), text
}

func isExchangePausedError(err error) bool {
	var pausedErr *ExchangePausedError
	return errors.As(err, &pausedErr)
}

func (a *App) exchangePauseState(exchange string) (time.Time, string, bool) {
	exchange = strings.ToLower(strings.TrimSpace(exchange))
	if exchange == "" {
		return time.Time{}, "", false
	}
	a.apiGuardMu.Lock()
	defer a.apiGuardMu.Unlock()
	guard := a.apiGuards[exchange]
	if guard == nil || guard.PausedUntil.IsZero() {
		return time.Time{}, "", false
	}
	now := time.Now()
	if !guard.PausedUntil.After(now) {
		guard.PausedUntil = time.Time{}
		guard.PauseReason = ""
		guard.ConsecutiveHTTPError = 0
		guard.LastHTTPErrorKey = ""
		guard.LastHTTPErrorText = ""
		return time.Time{}, "", false
	}
	return guard.PausedUntil, guard.PauseReason, true
}

func (a *App) exchangePauseUntil(exchange string) (time.Time, bool) {
	until, _, ok := a.exchangePauseState(exchange)
	return until, ok
}

func (a *App) noteExchangeHTTPStatus(exchange string, statusCode int, body string) {
	exchange = strings.ToLower(strings.TrimSpace(exchange))
	if exchange == "" {
		return
	}
	a.apiGuardMu.Lock()
	defer a.apiGuardMu.Unlock()
	if a.apiGuards == nil {
		a.apiGuards = map[string]*ExchangeAPIGuard{}
	}
	guard := a.apiGuards[exchange]
	if guard == nil {
		guard = &ExchangeAPIGuard{}
		a.apiGuards[exchange] = guard
	}
	now := time.Now()
	if !guard.PausedUntil.IsZero() && !guard.PausedUntil.After(now) {
		guard.PausedUntil = time.Time{}
		guard.PauseReason = ""
		guard.ConsecutiveHTTPError = 0
		guard.LastHTTPErrorKey = ""
		guard.LastHTTPErrorText = ""
	}
	if statusCode >= 200 && statusCode < 300 {
		guard.ConsecutiveHTTPError = 0
		guard.LastHTTPErrorKey = ""
		guard.LastHTTPErrorText = ""
		return
	}
	errKey, errText := exchangeHTTPErrorKey(statusCode, body)
	if errKey == guard.LastHTTPErrorKey {
		guard.ConsecutiveHTTPError++
	} else {
		guard.LastHTTPErrorKey = errKey
		guard.LastHTTPErrorText = errText
		guard.ConsecutiveHTTPError = 1
	}
	if guard.ConsecutiveHTTPError >= exchangeErrorTripThreshold {
		guard.ConsecutiveHTTPError = 0
		guard.PausedUntil = now.Add(exchangePauseDuration)
		guard.PauseReason = errText
		log.Printf("%s access paused for %d minutes after %d repeated identical errors (%s) until %s",
			exchange, int(exchangePauseDuration/time.Minute), exchangeErrorTripThreshold,
			errText, guard.PausedUntil.Local().Format("2006-01-02 15:04:05"))
		return
	}
}

func (a *App) waitExchangeRetry(ctx context.Context, exchange string, fallback time.Duration) bool {
	delay := fallback
	if until, ok := a.exchangePauseUntil(exchange); ok {
		delay = time.Until(until)
	}
	if delay <= 0 {
		return true
	}
	var retryCh <-chan struct{}
	a.apiGuardMu.Lock()
	if a.retrySignals == nil {
		a.retrySignals = map[string]chan struct{}{}
	}
	if ch, ok := a.retrySignals[exchange]; ok {
		retryCh = ch
	} else {
		ch = make(chan struct{}, 1)
		a.retrySignals[exchange] = ch
		retryCh = ch
	}
	a.apiGuardMu.Unlock()
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	case <-retryCh:
		return true
	}
}

func (a *App) fetchJSON(rawURL string, out any) error {
	exchange := exchangeFromEndpoint(rawURL)
	if until, reason, ok := a.exchangePauseState(exchange); ok {
		return &ExchangePausedError{Exchange: exchange, Until: until, Reason: reason}
	}
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "MultipleExchangeLiquidationMap/1.0")
	req.Header.Set("Accept", "application/json")
	resp, err := a.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		bodyText := strings.TrimSpace(string(body))
		a.noteExchangeHTTPStatus(exchange, resp.StatusCode, bodyText)
		return &HTTPStatusError{
			URL:        rawURL,
			Status:     resp.Status,
			StatusCode: resp.StatusCode,
			Body:       bodyText,
		}
	}
	a.noteExchangeHTTPStatus(exchange, resp.StatusCode, "")
	return json.NewDecoder(resp.Body).Decode(out)
}

func (a *App) fetchBinanceLongShortRatio(symbol string) (float64, error) {
	urls := []string{
		"https://fapi.binance.com/futures/data/globalLongShortAccountRatio?symbol=" + symbol + "&period=5m&limit=1",
		"https://fapi.binance.com/futures/data/topLongShortPositionRatio?symbol=" + symbol + "&period=5m&limit=1",
		"https://fapi.binance.com/futures/data/topLongShortAccountRatio?symbol=" + symbol + "&period=5m&limit=1",
	}
	var lastErr error
	for _, u := range urls {
		var arr []map[string]any
		if err := a.fetchJSON(u, &arr); err != nil {
			lastErr = err
			continue
		}
		if len(arr) == 0 {
			continue
		}
		m := arr[len(arr)-1]
		if v := parseAnyFloat(m["longShortRatio"]); v > 0 {
			return v, nil
		}
		longV := parseAnyFloat(m["longAccount"])
		if !(longV > 0) {
			longV = parseAnyFloat(m["longPosition"])
		}
		shortV := parseAnyFloat(m["shortAccount"])
		if !(shortV > 0) {
			shortV = parseAnyFloat(m["shortPosition"])
		}
		if longV > 0 && shortV > 0 {
			return longV / shortV, nil
		}
	}
	if lastErr == nil {
		lastErr = errors.New("binance long/short ratio unavailable")
	}
	return 0, lastErr
}

func (a *App) fetchBybitLongShortRatio(symbol string) (float64, error) {
	urls := []string{
		"https://api.bybit.com/v5/market/account-ratio?category=linear&symbol=" + symbol + "&period=5min&limit=1",
		"https://api.bybit.com/v5/market/account-ratio?category=linear&symbol=" + symbol + "&period=5min&limit=50",
	}
	var lastErr error
	for _, u := range urls {
		var resp map[string]any
		if err := a.fetchJSON(u, &resp); err != nil {
			lastErr = err
			continue
		}
		// v5 shape: {retCode, result:{list:[{buyRatio,sellRatio, ...}]}}
		result, _ := resp["result"].(map[string]any)
		list, _ := result["list"].([]any)
		if len(list) == 0 {
			continue
		}
		row, _ := list[0].(map[string]any)
		if row == nil && len(list) > 0 {
			row, _ = list[len(list)-1].(map[string]any)
		}
		if row == nil {
			continue
		}
		if v := parseAnyFloat(row["longShortRatio"]); v > 0 {
			return v, nil
		}
		buy := parseAnyFloat(row["buyRatio"])
		sell := parseAnyFloat(row["sellRatio"])
		if buy > 0 && sell > 0 {
			return buy / sell, nil
		}
		longV := parseAnyFloat(row["longAccount"])
		shortV := parseAnyFloat(row["shortAccount"])
		if longV > 0 && shortV > 0 {
			return longV / shortV, nil
		}
	}
	if lastErr == nil {
		lastErr = errors.New("bybit long/short ratio unavailable")
	}
	return 0, lastErr
}

func (a *App) fetchOKXLongShortRatio(ccy string) (float64, error) {
	type okxResp struct {
		Code string           `json:"code"`
		Msg  string           `json:"msg"`
		Data []map[string]any `json:"data"`
	}
	u := "https://www.okx.com/api/v5/rubik/stat/contracts/long-short-account-ratio?ccy=" + url.QueryEscape(ccy) + "&period=5m"
	var resp okxResp
	if err := a.fetchJSON(u, &resp); err != nil {
		return 0, err
	}
	if len(resp.Data) == 0 {
		return 0, errors.New("okx long/short ratio empty response")
	}
	row := resp.Data[len(resp.Data)-1]
	if v := parseAnyFloat(row["longShortRatio"]); v > 0 {
		return v, nil
	}
	// Some wrappers use longAccount/shortAccount or long/short.
	longV := parseAnyFloat(row["longAccount"])
	shortV := parseAnyFloat(row["shortAccount"])
	if longV > 0 && shortV > 0 {
		return longV / shortV, nil
	}
	longV = parseAnyFloat(row["long"])
	shortV = parseAnyFloat(row["short"])
	if longV > 0 && shortV > 0 {
		return longV / shortV, nil
	}
	return 0, errors.New("okx long/short ratio parse failed")
}

func (a *App) fetchBinanceSnapshot(symbol string) (Snapshot, error) {
	var premium struct {
		MarkPrice       string `json:"markPrice"`
		LastFundingRate string `json:"lastFundingRate"`
	}
	var oi struct {
		OpenInterest string `json:"openInterest"`
	}
	if err := a.fetchJSON("https://fapi.binance.com/fapi/v1/premiumIndex?symbol="+symbol, &premium); err != nil {
		return Snapshot{}, err
	}
	if err := a.fetchJSON("https://fapi.binance.com/fapi/v1/openInterest?symbol="+symbol, &oi); err != nil {
		return Snapshot{}, err
	}
	mark := parseFloat(premium.MarkPrice)
	oiQty := parseFloat(oi.OpenInterest)
	lsr, _ := a.fetchBinanceLongShortRatio(symbol)
	return Snapshot{
		Exchange:       "binance",
		Symbol:         symbol,
		MarkPrice:      mark,
		OIQty:          oiQty,
		OIValueUSD:     oiQty * mark,
		FundingRate:    parseFloat(premium.LastFundingRate),
		LongShortRatio: lsr,
		UpdatedTS:      time.Now().UnixMilli(),
	}, nil
}

func (a *App) fetchBybitSnapshot(symbol string) (Snapshot, error) {
	var resp struct {
		RetCode int    `json:"retCode"`
		RetMsg  string `json:"retMsg"`
		Result  struct {
			List []struct {
				MarkPrice         string `json:"markPrice"`
				OpenInterest      string `json:"openInterest"`
				OpenInterestValue string `json:"openInterestValue"`
				FundingRate       string `json:"fundingRate"`
			} `json:"list"`
		} `json:"result"`
	}
	if err := a.fetchJSON("https://api.bybit.com/v5/market/tickers?category=linear&symbol="+symbol, &resp); err != nil {
		return Snapshot{}, err
	}
	if resp.RetCode != 0 || len(resp.Result.List) == 0 {
		return Snapshot{}, fmt.Errorf("bybit invalid response: code=%d msg=%s", resp.RetCode, resp.RetMsg)
	}
	row := resp.Result.List[0]
	mark := parseFloat(row.MarkPrice)
	oiQty := parseFloat(row.OpenInterest)
	oiUSD := parseFloat(row.OpenInterestValue)
	if oiUSD <= 0 {
		oiUSD = oiQty * mark
	}
	lsr, _ := a.fetchBybitLongShortRatio(symbol)
	return Snapshot{
		Exchange:       "bybit",
		Symbol:         symbol,
		MarkPrice:      mark,
		OIQty:          oiQty,
		OIValueUSD:     oiUSD,
		FundingRate:    parseFloat(row.FundingRate),
		LongShortRatio: lsr,
		UpdatedTS:      time.Now().UnixMilli(),
	}, nil
}

func (a *App) fetchOKXSnapshot(instID string) (Snapshot, error) {
	var markResp struct {
		Code string `json:"code"`
		Data []struct {
			MarkPx string `json:"markPx"`
		} `json:"data"`
	}
	var oiResp struct {
		Code string `json:"code"`
		Data []struct {
			OI    string `json:"oi"`
			OIUsd string `json:"oiUsd"`
		} `json:"data"`
	}
	var fundingResp struct {
		Code string `json:"code"`
		Data []struct {
			FundingRate string `json:"fundingRate"`
		} `json:"data"`
	}
	if err := a.fetchJSON("https://www.okx.com/api/v5/public/mark-price?instType=SWAP&instId="+instID, &markResp); err != nil {
		return Snapshot{}, err
	}
	if err := a.fetchJSON("https://www.okx.com/api/v5/public/open-interest?instType=SWAP&instId="+instID, &oiResp); err != nil {
		return Snapshot{}, err
	}
	if err := a.fetchJSON("https://www.okx.com/api/v5/public/funding-rate?instId="+instID, &fundingResp); err != nil {
		return Snapshot{}, err
	}
	if len(markResp.Data) == 0 || len(oiResp.Data) == 0 {
		return Snapshot{}, errors.New("okx empty response")
	}
	mark := parseFloat(markResp.Data[0].MarkPx)
	oiQty := parseFloat(oiResp.Data[0].OI)
	oiUSD := parseFloat(oiResp.Data[0].OIUsd)
	if oiUSD <= 0 {
		oiUSD = oiQty * mark
	}
	funding := 0.0
	if len(fundingResp.Data) > 0 {
		funding = parseFloat(fundingResp.Data[0].FundingRate)
	}
	ccy := "ETH"
	if parts := strings.Split(strings.TrimSpace(instID), "-"); len(parts) > 0 && parts[0] != "" {
		ccy = parts[0]
	}
	lsr, _ := a.fetchOKXLongShortRatio(ccy)
	return Snapshot{
		Exchange:       "okx",
		MarkPrice:      mark,
		OIQty:          oiQty,
		OIValueUSD:     oiUSD,
		FundingRate:    funding,
		LongShortRatio: lsr,
		UpdatedTS:      time.Now().UnixMilli(),
	}, nil
}

func (a *App) buildDashboard(days int) (Dashboard, error) {
	states, err := a.loadMarketStates(defaultSymbol)
	if err != nil {
		return Dashboard{}, err
	}
	currentPrice := averageMarkPrice(states)
	currentPrice = math.Round(currentPrice*10) / 10
	cutoff := windowCutoff(time.Now(), days)
	bands, short, long, err := a.buildHeatFromLiquidationEvents(defaultSymbol, currentPrice, cutoff)
	if err != nil {
		return Dashboard{}, err
	}
	eventsCutoff := cutoff
	if days != windowIntraday {
		eventsCutoff = time.Now().Add(-24 * time.Hour).UnixMilli()
	}
	events := a.loadRecentEvents(defaultSymbol, eventsCutoff)
	analytics := a.buildHeatZoneAnalytics(defaultSymbol, currentPrice, states, bands, short, long, events, cutoff)
	return Dashboard{
		Symbol:       defaultSymbol,
		WindowDays:   days,
		GeneratedAt:  time.Now().UnixMilli(),
		WindowCutoff: cutoff,
		States:       states,
		CurrentPrice: currentPrice,
		Bands:        bands,
		LongestShort: short,
		LongestLong:  long,
		Events:       events,
		Analytics:    analytics,
	}, nil
}

func weightedFitForAnalysis(hours, minEvents int, counts map[string]map[string]any, suggestions []modelFitSuggestion) (int, float64) {
	if len(suggestions) == 0 {
		return 0, 0
	}
	totalNotional := 0.0
	weightedSE := 0.0
	totalCount := 0
	for _, sug := range suggestions {
		totalNotional += sug.Notional
		weightedSE += sug.WeightedSE * sug.Notional
		totalCount += sug.Count
	}
	if totalCount == 0 {
		for _, raw := range counts {
			if raw == nil {
				continue
			}
			if v, ok := raw["count"].(int); ok {
				totalCount += v
			}
		}
	}
	if totalNotional <= 0 {
		return totalCount, 0
	}
	_ = hours
	_ = minEvents
	return totalCount, weightedSE / totalNotional
}

func (a *App) loadGlobalFitStats(hours, minEvents int) (int, float64) {
	a.fitStatsMu.Lock()
	if !a.fitStatsAt.IsZero() && time.Since(a.fitStatsAt) < 15*time.Minute {
		count, se := a.fitStatsCount, a.fitStatsSE
		a.fitStatsMu.Unlock()
		return count, se
	}
	if !a.fitStatsBusy {
		a.fitStatsBusy = true
		go func() {
			resp, err := a.runModelFit(hours, minEvents, "", "global")
			count := 0
			se := 0.0
			if err == nil {
				body, err := json.Marshal(resp)
				if err == nil {
					var payload struct {
						Counts      map[string]map[string]any `json:"counts"`
						Suggestions []modelFitSuggestion      `json:"suggestions"`
					}
					if err := json.Unmarshal(body, &payload); err == nil {
						count, se = weightedFitForAnalysis(hours, minEvents, payload.Counts, payload.Suggestions)
					}
				}
			}
			a.fitStatsMu.Lock()
			a.fitStatsCount = count
			a.fitStatsSE = se
			a.fitStatsAt = time.Now()
			a.fitStatsBusy = false
			a.fitStatsMu.Unlock()
		}()
	}
	count, se := a.fitStatsCount, a.fitStatsSE
	a.fitStatsMu.Unlock()
	return count, se
}

type webSnapshotBand struct {
	current float64
	up20    float64
	down20  float64
	up50    float64
	down50  float64
	up100   float64
	down100 float64
}

func hitBandFromEvents(events []EventRow, startIdx int, endTS int64, current, up, down float64) (bool, float64) {
	hit := false
	maxMove := 0.0
	for i := startIdx; i < len(events); i++ {
		e := events[i]
		if e.EventTS >= endTS {
			break
		}
		move := math.Abs(e.Price - current)
		if move > maxMove {
			maxMove = move
		}
		side := strings.ToLower(strings.TrimSpace(e.Side))
		switch side {
		case "short":
			if up > 0 && e.Price >= current && e.Price <= up {
				hit = true
			}
		case "long":
			if down > 0 && e.Price <= current && e.Price >= down {
				hit = true
			}
		default:
			if (up > 0 && e.Price >= current && e.Price <= up) || (down > 0 && e.Price <= current && e.Price >= down) {
				hit = true
			}
		}
	}
	return hit, maxMove
}

func (a *App) loadWebDataSourceBacktestBands(symbol string, startTS int64) ([]int64, map[int64]webSnapshotBand) {
	rows, err := a.db.Query(`SELECT id, captured_at, range_low, range_high, payload_json
		FROM webdatasource_snapshots
		WHERE symbol IN (?, 'ETH') AND window_days=1 AND captured_at>=?
			AND EXISTS (SELECT 1 FROM webdatasource_points WHERE snapshot_id=webdatasource_snapshots.id LIMIT 1)
		ORDER BY captured_at`, symbol, startTS)
	if err != nil {
		return nil, nil
	}

	type snapshotMeta struct {
		id        int64
		ts        int64
		rangeLow  float64
		rangeHigh float64
		payload   string
	}
	metas := make([]snapshotMeta, 0, 128)

	for rows.Next() {
		var meta snapshotMeta
		if err := rows.Scan(&meta.id, &meta.ts, &meta.rangeLow, &meta.rangeHigh, &meta.payload); err != nil {
			continue
		}
		metas = append(metas, meta)
	}
	_ = rows.Close()

	ordered := make([]int64, 0, len(metas))
	out := map[int64]webSnapshotBand{}
	for _, meta := range metas {
		current := 0.0
		if strings.TrimSpace(meta.payload) != "" {
			var payload map[string]any
			if err := json.Unmarshal([]byte(meta.payload), &payload); err == nil {
				current = webDataSourcePayloadCurrentPrice(payload, meta.rangeLow, meta.rangeHigh)
			}
		}
		if current <= 0 && meta.rangeHigh > meta.rangeLow {
			current = math.Round(((meta.rangeHigh+meta.rangeLow)/2)*10) / 10
		}
		if current <= 0 {
			continue
		}
		points, err := a.db.Query(`SELECT side, price, liq_value FROM webdatasource_points WHERE snapshot_id=?`, meta.id)
		if err != nil {
			continue
		}
		best := webSnapshotBand{current: current}
		bestVal := map[string]float64{}
		for points.Next() {
			var side string
			var price, liq float64
			if err := points.Scan(&side, &price, &liq); err != nil {
				continue
			}
			if price <= 0 || liq <= 0 {
				continue
			}
			side = strings.ToLower(strings.TrimSpace(side))
			if side == "short" && price >= current {
				dist := price - current
				switch {
				case dist <= 20 && liq > bestVal["up20"]:
					best.up20, bestVal["up20"] = price, liq
				case dist <= 50 && liq > bestVal["up50"]:
					best.up50, bestVal["up50"] = price, liq
				case dist <= 100 && liq > bestVal["up100"]:
					best.up100, bestVal["up100"] = price, liq
				}
			}
			if side == "long" && price <= current {
				dist := current - price
				switch {
				case dist <= 20 && liq > bestVal["down20"]:
					best.down20, bestVal["down20"] = price, liq
				case dist <= 50 && liq > bestVal["down50"]:
					best.down50, bestVal["down50"] = price, liq
				case dist <= 100 && liq > bestVal["down100"]:
					best.down100, bestVal["down100"] = price, liq
				}
			}
		}
		_ = points.Close()
		if best.up20 == 0 && best.down20 == 0 && best.up50 == 0 && best.down50 == 0 && best.up100 == 0 && best.down100 == 0 {
			continue
		}
		ordered = append(ordered, meta.ts)
		out[meta.ts] = best
	}
	return ordered, out
}

func (a *App) buildWebDataSourceBacktestSummary(symbol string, horizonMin int, fitHours, fitMinEvents int) AnalysisBacktest {
	out := AnalysisBacktest{
		HorizonMin: horizonMin,
		Source:     "页面数据源(Coinglass)",
		Summary:    "页面数据源近 24 小时样本不足，暂时无法形成可靠回测。",
	}
	now := time.Now()
	startTS := now.Add(-24 * time.Hour).UnixMilli()
	orderedTS, snaps := a.loadWebDataSourceBacktestBands(symbol, startTS)
	if len(orderedTS) == 0 || len(snaps) == 0 {
		return out
	}

	eventRows, err := a.db.Query(`SELECT exchange, side, price, qty, notional_usd, event_ts
		FROM liquidation_events
		WHERE symbol=? AND event_ts>=? AND event_ts<?
		ORDER BY event_ts`, symbol, startTS, now.Add(time.Duration(horizonMin)*time.Minute).UnixMilli())
	if err != nil {
		return out
	}
	defer eventRows.Close()
	events := make([]EventRow, 0, 4096)
	for eventRows.Next() {
		var e EventRow
		if err := eventRows.Scan(&e.Exchange, &e.Side, &e.Price, &e.Qty, &e.NotionalUSD, &e.EventTS); err != nil {
			continue
		}
		e.Side = strings.ToLower(strings.TrimSpace(e.Side))
		if e.Price <= 0 || e.EventTS <= 0 {
			continue
		}
		events = append(events, e)
	}
	if len(events) == 0 {
		return out
	}

	hit20 := 0
	hit50 := 0
	hit100 := 0
	maxMoveSum := 0.0
	samples := 0
	horizonMS := int64(horizonMin) * 60 * 1000
	startIdx := 0
	for _, ts := range orderedTS {
		snap := snaps[ts]
		endTS := ts + horizonMS
		for startIdx < len(events) && events[startIdx].EventTS < ts {
			startIdx++
		}
		local20, max20 := hitBandFromEvents(events, startIdx, endTS, snap.current, snap.up20, snap.down20)
		local50, max50 := hitBandFromEvents(events, startIdx, endTS, snap.current, snap.up50, snap.down50)
		local100, max100 := hitBandFromEvents(events, startIdx, endTS, snap.current, snap.up100, snap.down100)
		if local20 {
			hit20++
		}
		if local50 {
			hit50++
		}
		if local100 {
			hit100++
		}
		maxMove := math.Max(max20, math.Max(max50, max100))
		maxMoveSum += maxMove
		samples++
	}
	if samples == 0 {
		return out
	}
	out.SampleCount = samples
	out.Hit20Rate = float64(hit20) / float64(samples)
	out.Hit50Rate = float64(hit50) / float64(samples)
	out.Hit100Rate = float64(hit100) / float64(samples)
	out.AvgMaxMove = maxMoveSum / float64(samples)
	out.FitEventCount, out.FitWeightedSE = a.loadGlobalFitStats(fitHours, fitMinEvents)
	out.Summary = fmt.Sprintf(
		"页面数据源过去 24 小时共抽取 %d 个 Coinglass 快照，未来 %d 分钟内命中 ±20/50/100 关键区间的比例分别为 %.0f%% / %.0f%% / %.0f%%。",
		samples, horizonMin, out.Hit20Rate*100, out.Hit50Rate*100, out.Hit100Rate*100,
	)
	return out
}

func (a *App) buildBacktestSummary(symbol string, horizonMin int, fitHours, fitMinEvents int) AnalysisBacktest {
	out := AnalysisBacktest{
		HorizonMin: horizonMin,
		Source:     "本地快照",
		Summary:    "近 24 小时样本不足，暂时无法形成可靠回测。",
	}
	now := time.Now()
	startTS := now.Add(-24 * time.Hour).UnixMilli()

	rows, err := a.db.Query(`SELECT report_ts, band, AVG(current_price), AVG(up_price), AVG(down_price)
		FROM band_reports
		WHERE symbol=? AND report_ts>=?
		GROUP BY report_ts, band
		ORDER BY report_ts, band`, symbol, startTS)
	if err != nil {
		return out
	}
	defer rows.Close()

	type bandSnap struct {
		current float64
		up      float64
		down    float64
	}
	snapshots := map[int64]map[int]bandSnap{}
	orderedTS := make([]int64, 0, 256)
	for rows.Next() {
		var ts int64
		var band int
		var current, up, down float64
		if err := rows.Scan(&ts, &band, &current, &up, &down); err != nil {
			continue
		}
		if _, ok := snapshots[ts]; !ok {
			snapshots[ts] = map[int]bandSnap{}
			orderedTS = append(orderedTS, ts)
		}
		snapshots[ts][band] = bandSnap{current: current, up: up, down: down}
	}
	if len(orderedTS) == 0 {
		return a.buildWebDataSourceBacktestSummary(symbol, horizonMin, fitHours, fitMinEvents)
	}

	eventRows, err := a.db.Query(`SELECT side, price, event_ts
		FROM liquidation_events
		WHERE symbol=? AND event_ts>=? AND event_ts<?
		ORDER BY event_ts`, symbol, startTS, now.Add(time.Duration(horizonMin)*time.Minute).UnixMilli())
	if err != nil {
		return out
	}
	defer eventRows.Close()

	type evt struct {
		side  string
		price float64
		ts    int64
	}
	events := make([]evt, 0, 2048)
	for eventRows.Next() {
		var e evt
		if err := eventRows.Scan(&e.side, &e.price, &e.ts); err != nil {
			continue
		}
		e.side = strings.ToLower(strings.TrimSpace(e.side))
		if e.price <= 0 || e.ts <= 0 {
			continue
		}
		events = append(events, e)
	}

	hit20 := 0
	hit50 := 0
	hit100 := 0
	maxMoveSum := 0.0
	samples := 0
	horizonMS := int64(horizonMin) * 60 * 1000
	startIdx := 0
	for _, ts := range orderedTS {
		bands := snapshots[ts]
		s20, ok20 := bands[20]
		s50, ok50 := bands[50]
		s100, ok100 := bands[100]
		if !ok20 || !ok50 || !ok100 || s20.current <= 0 {
			continue
		}
		endTS := ts + horizonMS
		for startIdx < len(events) && events[startIdx].ts < ts {
			startIdx++
		}
		local20 := false
		local50 := false
		local100 := false
		localMaxMove := 0.0
		for i := startIdx; i < len(events); i++ {
			e := events[i]
			if e.ts >= endTS {
				break
			}
			move := math.Abs(e.price - s20.current)
			if move > localMaxMove {
				localMaxMove = move
			}
			switch e.side {
			case "short":
				if e.price >= s20.current && e.price <= s20.up {
					local20 = true
				}
				if e.price >= s50.current && e.price <= s50.up {
					local50 = true
				}
				if e.price >= s100.current && e.price <= s100.up {
					local100 = true
				}
			case "long":
				if e.price <= s20.current && e.price >= s20.down {
					local20 = true
				}
				if e.price <= s50.current && e.price >= s50.down {
					local50 = true
				}
				if e.price <= s100.current && e.price >= s100.down {
					local100 = true
				}
			default:
				if math.Abs(e.price-s20.current) <= math.Abs(s20.up-s20.current) {
					local20 = true
				}
				if math.Abs(e.price-s50.current) <= math.Abs(s50.up-s50.current) {
					local50 = true
				}
				if math.Abs(e.price-s100.current) <= math.Abs(s100.up-s100.current) {
					local100 = true
				}
			}
		}
		if local20 {
			hit20++
		}
		if local50 {
			hit50++
		}
		if local100 {
			hit100++
		}
		maxMoveSum += localMaxMove
		samples++
	}
	if samples == 0 {
		return out
	}

	out.SampleCount = samples
	out.Hit20Rate = float64(hit20) / float64(samples)
	out.Hit50Rate = float64(hit50) / float64(samples)
	out.Hit100Rate = float64(hit100) / float64(samples)
	out.AvgMaxMove = maxMoveSum / float64(samples)
	out.FitEventCount, out.FitWeightedSE = a.loadGlobalFitStats(fitHours, fitMinEvents)
	out.Summary = fmt.Sprintf(
		"过去 24 小时共抽取 %d 个热区快照，未来 %d 分钟内命中 ±20/50/100 区间的比例分别为 %.0f%% / %.0f%% / %.0f%%。",
		samples, horizonMin, out.Hit20Rate*100, out.Hit50Rate*100, out.Hit100Rate*100,
	)
	if samples < 5 {
		if fallback := a.buildWebDataSourceBacktestSummary(symbol, horizonMin, fitHours, fitMinEvents); fallback.SampleCount > samples {
			return fallback
		}
	}
	return out
}

func (a *App) buildExchangeCards(symbol string, states []MarketState, nowTS int64) []ExchangeAnalysisCard {
	exchanges := []string{"binance", "okx", "bybit"}
	recentByEx := a.sumLiquidationNotionalByExchangeSince(symbol, nowTS-int64(time.Hour/time.Millisecond))
	out := make([]ExchangeAnalysisCard, 0, len(exchanges))
	for _, ex := range exchanges {
		card := ExchangeAnalysisCard{Exchange: normalizeExchangeName(ex)}
		state := findStateByExchange(states, ex)
		if state != nil {
			card.MarkPrice = state.MarkPrice
			if state.OIValueUSD != nil {
				card.OIValueUSD = *state.OIValueUSD
			}
			if state.FundingRate != nil {
				card.FundingRate = *state.FundingRate
			}
			if state.LongShortRatio != nil {
				card.LongShortRatio = *state.LongShortRatio
			}
		}
		up, down := a.sumBandNotionalInRangeByExchange(symbol, ex, card.MarkPrice, 50, nowTS-int64(time.Hour/time.Millisecond), nowTS)
		card.UpperRiskUSD = up
		card.LowerRiskUSD = down
		total := up + down
		card.ShortRiskScore = 42
		card.LongRiskScore = 42
		if total > 0 {
			card.ShortRiskScore += 38 * (up / total)
			card.LongRiskScore += 38 * (down / total)
		}
		if card.FundingRate > 0 {
			card.ShortRiskScore += clamp(card.FundingRate*120000, 0, 10)
		} else if card.FundingRate < 0 {
			card.LongRiskScore += clamp(-card.FundingRate*120000, 0, 10)
		}
		card.ShortRiskScore = clamp(card.ShortRiskScore, 0, 100)
		card.LongRiskScore = clamp(card.LongRiskScore, 0, 100)
		card.RecentLiquidUSD = recentByEx[ex]
		switch {
		case card.ShortRiskScore-card.LongRiskScore >= 8:
			card.Bias = "偏上冲挤空"
			card.FocusSide = "up"
			card.FocusPrice = card.MarkPrice + 20
		case card.LongRiskScore-card.ShortRiskScore >= 8:
			card.Bias = "偏下探踩踏"
			card.FocusSide = "down"
			card.FocusPrice = math.Max(0, card.MarkPrice-20)
		default:
			card.Bias = "相对均衡"
			card.FocusSide = "flat"
			card.FocusPrice = card.MarkPrice
		}
		card.Summary = fmt.Sprintf("%s 近 1 小时上方风险 %s USD，下方风险 %s USD，近 1 小时实际清算 %s USD。",
			card.Exchange, humanizeCompactUSD(card.UpperRiskUSD), humanizeCompactUSD(card.LowerRiskUSD), humanizeCompactUSD(card.RecentLiquidUSD))
		out = append(out, card)
	}
	sort.Slice(out, func(i, j int) bool {
		li := math.Max(out[i].ShortRiskScore, out[i].LongRiskScore)
		lj := math.Max(out[j].ShortRiskScore, out[j].LongRiskScore)
		if li == lj {
			return out[i].Exchange < out[j].Exchange
		}
		return li > lj
	})
	return out
}

func (a *App) buildAnalysisSnapshot() (AnalysisSnapshot, error) {
	dash, err := a.buildDashboard(windowIntraday)
	if err != nil {
		return AnalysisSnapshot{}, err
	}
	{
		instantBand20 := bandRowOrDefault(dash.Bands, dash.CurrentPrice, 20)
		factorBase := analysisScoreFromSnapshotBase(analysisScoreSnapshot{
			CurrentPrice: dash.CurrentPrice,
			Bands:        dash.Bands,
			CoreZone:     dash.Analytics.CoreZone,
			AvgFunding:   dash.Analytics.Market.AvgFunding,
		})
		factorShort, factorLong := analysisApplyMomentumTilt(factorBase.ShortRisk, factorBase.LongRisk, a.analysisShortTermMomentumTilt(defaultSymbol, dash.CurrentPrice))
		factorDirection, factorConfidence := analysisDirectionAndConfidence(factorShort, factorLong)
		factorTitle, factorBias := analysisOverviewHeadline(factorShort, factorLong)

		nowTS := time.Now().UnixMilli()
		fourHour := int64((4 * time.Hour) / time.Millisecond)
		recent4h := a.sumLiquidationNotionalSince(defaultSymbol, nowTS-fourHour)

		changes := a.buildAnalysisChangeCharts(defaultSymbol, 24)

		keyZones := []AnalysisKeyZone{
			{Name: "上方最近强清算区", Side: "up", Price: dash.Analytics.CoreZone.UpPrice, Distance: nearestZoneDistance(dash.CurrentPrice, dash.Analytics.CoreZone.UpPrice), NotionalUSD: dash.Analytics.CoreZone.UpNotionalUSD, Note: "更适合观察短线挤空触发点。"},
			{Name: "下方最近强清算区", Side: "down", Price: dash.Analytics.CoreZone.DownPrice, Distance: nearestZoneDistance(dash.CurrentPrice, dash.Analytics.CoreZone.DownPrice), NotionalUSD: dash.Analytics.CoreZone.DownNotionalUSD, Note: "更适合观察短线踩踏触发点。"},
			{Name: "上方即时压力带", Side: "up", Price: instantBand20.UpPrice, Distance: nearestZoneDistance(dash.CurrentPrice, instantBand20.UpPrice), NotionalUSD: instantBand20.UpNotionalUSD, Note: "离现价最近，适合盯短线突破。"},
			{Name: "下方即时支撑带", Side: "down", Price: instantBand20.DownPrice, Distance: nearestZoneDistance(dash.CurrentPrice, instantBand20.DownPrice), NotionalUSD: instantBand20.DownNotionalUSD, Note: "离现价最近，适合盯短线跌破。"},
		}

		overview := AnalysisOverview{
			Title:      factorTitle,
			Bias:       factorBias,
			Direction:  factorDirection,
			Confidence: factorConfidence,
			Summary: fmt.Sprintf(
				"当前价格 %.1f，近端上方风险 %.0f 分、下方风险 %.0f 分；主导交易所为 %s，提示 %s。",
				dash.CurrentPrice, factorShort, factorLong, dash.Analytics.DominantExchange, dash.Analytics.Alert.Level,
			),
		}
		backtest := a.buildBacktestSummary(defaultSymbol, 60, 24, 25)

		return AnalysisSnapshot{
			Symbol:        dash.Symbol,
			GeneratedAt:   time.Now().UnixMilli(),
			CurrentPrice:  dash.CurrentPrice,
			Overview:      overview,
			Broadcast:     buildBroadcastSummary(dash.CurrentPrice, overview, factorShort, factorLong, keyZones, dash),
			Indicators:    a.buildAnalysisIndicators(dash.CurrentPrice),
			RiskScores:    []AnalysisRiskScore{{Label: "空头被挤压风险", Score: factorShort, Tone: scoreTone(factorShort)}, {Label: "多头被踩踏风险", Score: factorLong, Tone: scoreTone(factorLong)}, {Label: "短线波动放大风险", Score: clamp(recent4h/1_200_000*100, 0, 100), Tone: scoreTone(clamp(recent4h/1_200_000*100, 0, 100))}},
			KeyZones:      keyZones,
			Changes:       changes,
			ExchangeCards: a.buildExchangeCards(defaultSymbol, dash.States, nowTS),
			Backtest:      backtest,
			Dashboard:     dash,
		}, nil
	}

	b20 := bandRowOrDefault(dash.Bands, dash.CurrentPrice, 20)
	b50 := bandRowOrDefault(dash.Bands, dash.CurrentPrice, 50)
	b100 := bandRowOrDefault(dash.Bands, dash.CurrentPrice, 100)

	upWeighted := b20.UpNotionalUSD*0.5 + math.Max(0, b50.UpNotionalUSD-b20.UpNotionalUSD)*0.3 + math.Max(0, b100.UpNotionalUSD-b50.UpNotionalUSD)*0.2
	downWeighted := b20.DownNotionalUSD*0.5 + math.Max(0, b50.DownNotionalUSD-b20.DownNotionalUSD)*0.3 + math.Max(0, b100.DownNotionalUSD-b50.DownNotionalUSD)*0.2
	totalWeighted := upWeighted + downWeighted

	fundingBias := 0.0
	if dash.Analytics.Market.AvgFunding > 0 {
		fundingBias = clamp(dash.Analytics.Market.AvgFunding*120000, 0, 8)
	} else if dash.Analytics.Market.AvgFunding < 0 {
		fundingBias = clamp(-dash.Analytics.Market.AvgFunding*120000, 0, 8)
	}
	nearUpBonus := 0.0
	nearDownBonus := 0.0
	if dash.Analytics.CoreZone.UpPrice > 0 {
		nearUpBonus = clamp((120-nearestZoneDistance(dash.CurrentPrice, dash.Analytics.CoreZone.UpPrice))/12, 0, 10)
	}
	if dash.Analytics.CoreZone.DownPrice > 0 {
		nearDownBonus = clamp((120-nearestZoneDistance(dash.CurrentPrice, dash.Analytics.CoreZone.DownPrice))/12, 0, 10)
	}

	shortRiskScore := 45.0
	longRiskScore := 45.0
	if totalWeighted > 0 {
		shortRiskScore += 35 * (upWeighted / totalWeighted)
		longRiskScore += 35 * (downWeighted / totalWeighted)
	}
	if dash.Analytics.Market.AvgFunding > 0 {
		shortRiskScore += fundingBias
	} else if dash.Analytics.Market.AvgFunding < 0 {
		longRiskScore += fundingBias
	}
	shortRiskScore = clamp(shortRiskScore+nearUpBonus, 0, 100)
	longRiskScore = clamp(longRiskScore+nearDownBonus, 0, 100)

	// Add a short-term momentum correction so the overview respects recent price moves
	// instead of relying only on liquidation structure farther away from spot.
	shortTermTilt := a.analysisShortTermMomentumTilt(defaultSymbol, dash.CurrentPrice)
	if shortTermTilt > 0 {
		shortRiskScore = clamp(shortRiskScore+shortTermTilt, 0, 100)
		longRiskScore = clamp(longRiskScore-shortTermTilt*0.55, 0, 100)
	} else if shortTermTilt < 0 {
		downTilt := -shortTermTilt
		longRiskScore = clamp(longRiskScore+downTilt, 0, 100)
		shortRiskScore = clamp(shortRiskScore-downTilt*0.55, 0, 100)
	}

	bias := "区间内多空风险相对均衡"
	title := "当前市场更像震荡蓄能"
	if shortRiskScore-longRiskScore >= 8 {
		bias = "上方空头清算风险更高，短线更容易被向上挤压"
		title = "当前市场偏向上冲挤空结构"
	} else if longRiskScore-shortRiskScore >= 8 {
		bias = "下方多头清算风险更高，短线更容易出现向下踩踏"
		title = "当前市场偏向下探踩踏结构"
	}
	confidence := clamp(math.Abs(shortRiskScore-longRiskScore)*4.5, 18, 92)

	nowTS := time.Now().UnixMilli()
	fourHour := int64((4 * time.Hour) / time.Millisecond)
	recent4h := a.sumLiquidationNotionalSince(defaultSymbol, nowTS-fourHour)

	changes := a.buildAnalysisChangeCharts(defaultSymbol, 24)

	keyZones := []AnalysisKeyZone{
		{Name: "上方最近强清算区", Side: "up", Price: dash.Analytics.CoreZone.UpPrice, Distance: nearestZoneDistance(dash.CurrentPrice, dash.Analytics.CoreZone.UpPrice), NotionalUSD: dash.Analytics.CoreZone.UpNotionalUSD, Note: "更适合观察短线挤空触发点。"},
		{Name: "下方最近强清算区", Side: "down", Price: dash.Analytics.CoreZone.DownPrice, Distance: nearestZoneDistance(dash.CurrentPrice, dash.Analytics.CoreZone.DownPrice), NotionalUSD: dash.Analytics.CoreZone.DownNotionalUSD, Note: "更适合观察短线踩踏触发点。"},
		{Name: "上方即时压力带", Side: "up", Price: b20.UpPrice, Distance: nearestZoneDistance(dash.CurrentPrice, b20.UpPrice), NotionalUSD: b20.UpNotionalUSD, Note: "离现价最近，适合盯短线突破。"},
		{Name: "下方即时支撑带", Side: "down", Price: b20.DownPrice, Distance: nearestZoneDistance(dash.CurrentPrice, b20.DownPrice), NotionalUSD: b20.DownNotionalUSD, Note: "离现价最近，适合盯短线跌破。"},
	}

	overview := AnalysisOverview{
		Title: title,
		Bias:  bias,
		Direction: func() string {
			switch {
			case shortRiskScore-longRiskScore >= 8:
				return "up"
			case longRiskScore-shortRiskScore >= 8:
				return "down"
			default:
				return "flat"
			}
		}(),
		Confidence: confidence,
		Summary: fmt.Sprintf(
			"当前价格 %.1f，近端上方风险 %.0f 分、下方风险 %.0f 分；主导交易所为 %s，提示 %s。",
			dash.CurrentPrice, shortRiskScore, longRiskScore, dash.Analytics.DominantExchange, dash.Analytics.Alert.Level,
		),
	}
	backtest := a.buildBacktestSummary(defaultSymbol, 60, 24, 25)

	return AnalysisSnapshot{
		Symbol:        dash.Symbol,
		GeneratedAt:   time.Now().UnixMilli(),
		CurrentPrice:  dash.CurrentPrice,
		Overview:      overview,
		Broadcast:     buildBroadcastSummary(dash.CurrentPrice, overview, shortRiskScore, longRiskScore, keyZones, dash),
		Indicators:    a.buildAnalysisIndicators(dash.CurrentPrice),
		RiskScores:    []AnalysisRiskScore{{Label: "空头被挤压风险", Score: shortRiskScore, Tone: scoreTone(shortRiskScore)}, {Label: "多头被踩踏风险", Score: longRiskScore, Tone: scoreTone(longRiskScore)}, {Label: "短线波动放大风险", Score: clamp(recent4h/1_200_000*100, 0, 100), Tone: scoreTone(clamp(recent4h/1_200_000*100, 0, 100))}},
		KeyZones:      keyZones,
		Changes:       changes,
		ExchangeCards: a.buildExchangeCards(defaultSymbol, dash.States, nowTS),
		Backtest:      backtest,
		Dashboard:     dash,
	}, nil
}

func (a *App) analysisShortTermMomentumTilt(symbol string, currentPrice float64) float64 {
	history := a.loadAnalysisBandHistory(symbol, 24)
	if len(history) == 0 || currentPrice <= 0 {
		return 0
	}
	replayHistory := make([]analysisScoreSnapshot, 0, len(history))
	for _, snap := range history {
		replayHistory = append(replayHistory, analysisScoreSnapshot{
			TS:           snap.TS,
			CurrentPrice: snap.Current,
			UpByBand:     snap.UpByBand,
			DownByBand:   snap.DownByBand,
		})
	}
	return analysisShortTermMomentumTiltFromSnapshots(replayHistory)
	latest := history[len(history)-1]
	push20 := analysisBandPushScore(latest.UpByBand[20], latest.DownByBand[20])
	push50 := analysisBandPushScore(latest.UpByBand[50], latest.DownByBand[50])
	pushTilt := clamp(push20*0.20+push50*0.08, -18, 18)

	nowTS := time.Now().UnixMilli()
	change5m := analysisSnapshotPriceChangePct(history, nowTS, currentPrice, 5*time.Minute)
	change15m := analysisSnapshotPriceChangePct(history, nowTS, currentPrice, 15*time.Minute)
	change30m := analysisSnapshotPriceChangePct(history, nowTS, currentPrice, 30*time.Minute)
	priceTilt := clamp(change5m*10+change15m*7+change30m*5, -24, 24)

	return clamp(pushTilt+priceTilt, -24, 24)
}

func analysisSnapshotPriceChangePct(history []analysisBandHistorySnapshot, latestTS int64, latestPrice float64, lookback time.Duration) float64 {
	if len(history) < 2 || latestPrice <= 0 || lookback <= 0 {
		return 0
	}
	targetTS := latestTS - lookback.Milliseconds()
	pastPrice := 0.0
	for i := len(history) - 2; i >= 0; i-- {
		if history[i].TS <= targetTS && history[i].Current > 0 {
			pastPrice = history[i].Current
			break
		}
	}
	if pastPrice <= 0 {
		for i := len(history) - 2; i >= 0; i-- {
			if history[i].Current > 0 {
				pastPrice = history[i].Current
				break
			}
		}
	}
	if pastPrice <= 0 {
		return 0
	}
	return ((latestPrice - pastPrice) / pastPrice) * 100
}

func (a *App) buildHeatFromLiquidationEvents(symbol string, currentPrice float64, cutoff int64) ([]BandRow, []any, []any, error) {
	if currentPrice <= 0 {
		return []BandRow{}, []any{"-", 0}, []any{"-", 0}, nil
	}
	rows, err := a.db.Query(`SELECT side, price, notional_usd FROM liquidation_events WHERE symbol=? AND event_ts>=?`, symbol, cutoff)
	if err != nil {
		return nil, nil, nil, err
	}
	defer rows.Close()
	type ev struct {
		side     string
		price    float64
		notional float64
	}
	events := make([]ev, 0, 1024)
	for rows.Next() {
		var side string
		var price, notional float64
		if err := rows.Scan(&side, &price, &notional); err != nil {
			continue
		}
		if price <= 0 || notional <= 0 {
			continue
		}
		events = append(events, ev{side: strings.ToLower(strings.TrimSpace(side)), price: price, notional: notional})
	}
	out := make([]BandRow, 0, len(bandSizes))
	prevUpTotal := 0.0
	prevDownTotal := 0.0
	maxShortPrice := 0.0
	maxLongPrice := 0.0
	maxShortNotional := 0.0
	maxLongNotional := 0.0
	for _, band := range bandSizes {
		b := float64(band)
		upPrice := math.Round((currentPrice+b)*10) / 10
		downPrice := math.Round((currentPrice-b)*10) / 10
		upTotal := 0.0
		downTotal := 0.0
		for _, e := range events {
			if e.price < downPrice || e.price > upPrice {
				continue
			}
			switch e.side {
			case "short":
				if e.price >= currentPrice {
					upTotal += e.notional
				}
			case "long":
				if e.price <= currentPrice {
					downTotal += e.notional
				}
			default:
				if e.price >= currentPrice {
					upTotal += e.notional
				}
				if e.price <= currentPrice {
					downTotal += e.notional
				}
			}
		}
		out = append(out, BandRow{
			Band:            band,
			UpPrice:         upPrice,
			UpNotionalUSD:   upTotal,
			DownPrice:       downPrice,
			DownNotionalUSD: downTotal,
		})
		upInc := math.Max(0, upTotal-prevUpTotal)
		downInc := math.Max(0, downTotal-prevDownTotal)
		prevUpTotal = upTotal
		prevDownTotal = downTotal
		if upInc > maxShortNotional {
			maxShortNotional = upInc
			maxShortPrice = upPrice
		}
		if downInc > maxLongNotional {
			maxLongNotional = downInc
			maxLongPrice = downPrice
		}
	}
	short := []any{"-", 0}
	if maxShortPrice > 0 {
		short = []any{maxShortPrice, maxShortNotional}
	}
	long := []any{"-", 0}
	if maxLongPrice > 0 {
		long = []any{maxLongPrice, maxLongNotional}
	}
	return out, short, long, nil
}

func toFloatAny(v any) float64 {
	switch t := v.(type) {
	case float64:
		return t
	case float32:
		return float64(t)
	case int:
		return float64(t)
	case int64:
		return float64(t)
	case string:
		f, _ := strconv.ParseFloat(strings.TrimSpace(t), 64)
		return f
	default:
		f, _ := strconv.ParseFloat(strings.TrimSpace(fmt.Sprint(v)), 64)
		return f
	}
}

func (a *App) sumBandNotionalInRange(symbol string, currentPrice, band float64, startTS, endTS int64) (float64, float64) {
	if currentPrice <= 0 || band <= 0 || endTS <= startTS {
		return 0, 0
	}
	rows, err := a.db.Query(`SELECT side, price, notional_usd FROM liquidation_events
		WHERE symbol=? AND event_ts>=? AND event_ts<?`, symbol, startTS, endTS)
	if err != nil {
		return 0, 0
	}
	defer rows.Close()
	up := 0.0
	down := 0.0
	for rows.Next() {
		var side string
		var price, notional float64
		if err := rows.Scan(&side, &price, &notional); err != nil {
			continue
		}
		if price <= 0 || notional <= 0 {
			continue
		}
		if math.Abs(price-currentPrice) > band {
			continue
		}
		side = strings.ToLower(strings.TrimSpace(side))
		switch side {
		case "short":
			if price >= currentPrice {
				up += notional
			}
		case "long":
			if price <= currentPrice {
				down += notional
			}
		default:
			if price >= currentPrice {
				up += notional
			}
			if price <= currentPrice {
				down += notional
			}
		}
	}
	return up, down
}

func (a *App) sumBandNotionalInRangeByExchange(symbol, exchange string, currentPrice, band float64, startTS, endTS int64) (float64, float64) {
	if currentPrice <= 0 || band <= 0 || endTS <= startTS {
		return 0, 0
	}
	rows, err := a.db.Query(`SELECT side, price, notional_usd FROM liquidation_events
		WHERE symbol=? AND LOWER(exchange)=? AND event_ts>=? AND event_ts<?`, symbol, strings.ToLower(strings.TrimSpace(exchange)), startTS, endTS)
	if err != nil {
		return 0, 0
	}
	defer rows.Close()
	up := 0.0
	down := 0.0
	for rows.Next() {
		var side string
		var price, notional float64
		if err := rows.Scan(&side, &price, &notional); err != nil {
			continue
		}
		if price <= 0 || notional <= 0 || math.Abs(price-currentPrice) > band {
			continue
		}
		side = strings.ToLower(strings.TrimSpace(side))
		switch side {
		case "short":
			if price >= currentPrice {
				up += notional
			}
		case "long":
			if price <= currentPrice {
				down += notional
			}
		default:
			if price >= currentPrice {
				up += notional
			}
			if price <= currentPrice {
				down += notional
			}
		}
	}
	return up, down
}

func (a *App) maxBandNotionalInRange(symbol string, currentPrice, band float64, startTS, endTS int64) float64 {
	if currentPrice <= 0 || band <= 0 || endTS <= startTS {
		return 0
	}
	rows, err := a.db.Query(`SELECT price, notional_usd FROM liquidation_events
		WHERE symbol=? AND event_ts>=? AND event_ts<?`, symbol, startTS, endTS)
	if err != nil {
		return 0
	}
	defer rows.Close()
	maxV := 0.0
	for rows.Next() {
		var price, notional float64
		if err := rows.Scan(&price, &notional); err != nil {
			continue
		}
		if price <= 0 || notional <= 0 {
			continue
		}
		if math.Abs(price-currentPrice) > band {
			continue
		}
		if notional > maxV {
			maxV = notional
		}
	}
	return maxV
}

func (a *App) avgFundingInRange(symbol string, startTS, endTS int64) float64 {
	if endTS <= startTS {
		return 0
	}
	var v sql.NullFloat64
	if err := a.db.QueryRow(`SELECT AVG(funding_rate) FROM oi_snapshots
		WHERE symbol=? AND updated_ts>=? AND updated_ts<? AND funding_rate IS NOT NULL`, symbol, startTS, endTS).Scan(&v); err != nil {
		return 0
	}
	if !v.Valid {
		return 0
	}
	return v.Float64
}

func (a *App) sumLiquidationNotionalSince(symbol string, startTS int64) float64 {
	var total sql.NullFloat64
	if err := a.db.QueryRow(`SELECT SUM(notional_usd) FROM liquidation_events WHERE symbol=? AND event_ts>=?`, symbol, startTS).Scan(&total); err != nil {
		return 0
	}
	if !total.Valid {
		return 0
	}
	return total.Float64
}

func (a *App) sumLiquidationNotionalByExchangeSince(symbol string, startTS int64) map[string]float64 {
	out := map[string]float64{
		"binance": 0,
		"bybit":   0,
		"okx":     0,
	}
	rows, err := a.db.Query(`SELECT LOWER(exchange), SUM(notional_usd) FROM liquidation_events
		WHERE symbol=? AND event_ts>=?
		GROUP BY LOWER(exchange)`, symbol, startTS)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var ex string
		var total sql.NullFloat64
		if err := rows.Scan(&ex, &total); err != nil {
			continue
		}
		if !total.Valid {
			continue
		}
		out[ex] = total.Float64
	}
	return out
}

func (a *App) computeExchangeContribution(symbol string, currentPrice, band float64, cutoff int64, states []MarketState) []ExchangeContribution {
	sums := map[string]float64{
		"binance": 0,
		"bybit":   0,
		"okx":     0,
	}
	rows, err := a.db.Query(`SELECT LOWER(exchange), price, notional_usd FROM liquidation_events
		WHERE symbol=? AND event_ts>=?`, symbol, cutoff)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var ex string
			var price, notional float64
			if err := rows.Scan(&ex, &price, &notional); err != nil {
				continue
			}
			if price <= 0 || notional <= 0 {
				continue
			}
			if math.Abs(price-currentPrice) > band {
				continue
			}
			sums[ex] += notional
		}
	}
	total := 0.0
	for _, v := range sums {
		total += v
	}
	out := make([]ExchangeContribution, 0, len(sums))
	for ex, v := range sums {
		share := 0.0
		if total > 0 {
			share = v / total
		}
		out = append(out, ExchangeContribution{
			Exchange:    normalizeExchangeName(ex),
			NotionalUSD: v,
			Share:       share,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].NotionalUSD == out[j].NotionalUSD {
			return out[i].Exchange < out[j].Exchange
		}
		return out[i].NotionalUSD > out[j].NotionalUSD
	})
	if total <= 0 {
		return []ExchangeContribution{}
	}
	return out
}

func (a *App) buildHeatZoneAnalytics(symbol string, currentPrice float64, states []MarketState, bands []BandRow, longestShort, longestLong []any, events []EventRow, cutoff int64) HeatZoneAnalytics {
	b20 := bandRowOrDefault(bands, currentPrice, 20)
	b50 := bandRowOrDefault(bands, currentPrice, 50)
	b100 := bandRowOrDefault(bands, currentPrice, 100)

	verdict20, ratio20 := classifyImbalance(b20.UpNotionalUSD, b20.DownNotionalUSD)
	verdict50, ratio50 := classifyImbalance(b50.UpNotionalUSD, b50.DownNotionalUSD)
	verdict100, ratio100 := classifyImbalance(b100.UpNotionalUSD, b100.DownNotionalUSD)

	imbalance := []ImbalanceStat{
		{Band: 20, UpNotionalUSD: b20.UpNotionalUSD, DownNotionalUSD: b20.DownNotionalUSD, Ratio: ratio20, Verdict: verdict20},
		{Band: 50, UpNotionalUSD: b50.UpNotionalUSD, DownNotionalUSD: b50.DownNotionalUSD, Ratio: ratio50, Verdict: verdict50},
		{Band: 100, UpNotionalUSD: b100.UpNotionalUSD, DownNotionalUSD: b100.DownNotionalUSD, Ratio: ratio100, Verdict: verdict100},
	}
	density := []DensityLayer{
		{Label: "0-20pt", UpNotionalUSD: math.Max(0, b20.UpNotionalUSD), DownNotionalUSD: math.Max(0, b20.DownNotionalUSD)},
		{Label: "20-50pt", UpNotionalUSD: math.Max(0, b50.UpNotionalUSD-b20.UpNotionalUSD), DownNotionalUSD: math.Max(0, b50.DownNotionalUSD-b20.DownNotionalUSD)},
		{Label: "50-100pt", UpNotionalUSD: math.Max(0, b100.UpNotionalUSD-b50.UpNotionalUSD), DownNotionalUSD: math.Max(0, b100.DownNotionalUSD-b50.DownNotionalUSD)},
	}

	market := MarketSummary{}
	fundingSum := 0.0
	fundingCnt := 0
	for _, s := range states {
		ex := strings.ToLower(strings.TrimSpace(s.Exchange))
		oi := 0.0
		if s.OIValueUSD != nil && *s.OIValueUSD > 0 {
			oi = *s.OIValueUSD
		}
		switch ex {
		case "binance":
			market.BinanceOIUSD = oi
		case "okx":
			market.OKXOIUSD = oi
		case "bybit":
			market.BybitOIUSD = oi
		}
		if s.FundingRate != nil {
			fundingSum += *s.FundingRate
			fundingCnt++
		}
	}
	if fundingCnt > 0 {
		market.AvgFunding = fundingSum / float64(fundingCnt)
	}
	market.AvgFundingVerdict = fundingVerdictFromStates(states)

	shortPrice := 0.0
	shortNotional := 0.0
	if len(longestShort) >= 2 {
		shortPrice = toFloatAny(longestShort[0])
		shortNotional = toFloatAny(longestShort[1])
	}
	longPrice := 0.0
	longNotional := 0.0
	if len(longestLong) >= 2 {
		longPrice = toFloatAny(longestLong[0])
		longNotional = toFloatAny(longestLong[1])
	}
	core := CoreZone{
		UpPrice:         shortPrice,
		UpNotionalUSD:   shortNotional,
		DownPrice:       longPrice,
		DownNotionalUSD: longNotional,
	}
	distUp := math.MaxFloat64
	if shortPrice > 0 {
		distUp = math.Abs(shortPrice - currentPrice)
	}
	distDown := math.MaxFloat64
	if longPrice > 0 {
		distDown = math.Abs(currentPrice - longPrice)
	}
	if distUp < distDown {
		core.NearestSide = "涓婃柟寮哄尯"
		core.NearestDistance = distUp
		core.NearestStrongPrice = shortPrice
	} else if distDown < math.MaxFloat64 {
		core.NearestSide = "涓嬫柟寮哄尯"
		core.NearestDistance = distDown
		core.NearestStrongPrice = longPrice
	}

	nowTS := time.Now().UnixMilli()
	slot := int64(5 * 60 * 1000)
	upNow, downNow := a.sumBandNotionalInRange(symbol, currentPrice, 20, nowTS-slot, nowTS)
	upPrev, downPrev := a.sumBandNotionalInRange(symbol, currentPrice, 20, nowTS-2*slot, nowTS-slot)
	maxNow := a.maxBandNotionalInRange(symbol, currentPrice, 100, nowTS-slot, nowTS)
	maxPrev := a.maxBandNotionalInRange(symbol, currentPrice, 100, nowTS-2*slot, nowTS-slot)
	fundingNow := a.avgFundingInRange(symbol, nowTS-slot, nowTS)
	fundingPrev := a.avgFundingInRange(symbol, nowTS-2*slot, nowTS-slot)
	tracking := ChangeTracking{
		Up20DeltaUSD:    upNow - upPrev,
		Down20DeltaUSD:  downNow - downPrev,
		LongestDeltaUSD: maxNow - maxPrev,
		FundingDelta:    fundingNow - fundingPrev,
	}

	contrib := a.computeExchangeContribution(symbol, currentPrice, 50, cutoff, states)
	dominant := "-"
	if len(contrib) > 0 {
		dominant = contrib[0].Exchange
	}

	alert := AlertSummary{
		Level:       "常规监控",
		Recent1mUSD: a.sumLiquidationNotionalSince(symbol, nowTS-60*1000),
		Suggestion:  fmt.Sprintf("%.1f / %.1f 关注上下关键价位", b20.UpPrice, b20.DownPrice),
	}
	recentByEx := a.sumLiquidationNotionalByExchangeSince(symbol, nowTS-60*1000)
	alert.Recent1mBinanceUSD = recentByEx["binance"]
	alert.Recent1mBybitUSD = recentByEx["bybit"]
	alert.Recent1mOKXUSD = recentByEx["okx"]
	if len(events) > 0 {
		e := events[0]
		alert.RecentExchange = normalizeExchangeName(e.Exchange)
		alert.RecentSide = strings.ToLower(strings.TrimSpace(e.Side))
		alert.RecentPrice = e.Price
	}
	if core.NearestDistance > 0 && core.NearestDistance <= 35 {
		alert.Level = "临近强区预警"
	} else if alert.Recent1mUSD >= 2_000_000 {
		alert.Level = "波动放大预警"
	}
	if verdict20 == "涓婃柟鍋忓己" {
		alert.Suggestion = fmt.Sprintf("%.1f / %.1f 警惕上下双向插针风险", b20.UpPrice, b20.DownPrice)
	}
	if verdict20 == "涓嬫柟鍋忓己" {
		alert.Suggestion = fmt.Sprintf("%.1f / %.1f 下方流动性风险更高", b20.UpPrice, b20.DownPrice)
	}

	return HeatZoneAnalytics{
		Top: TopConclusion{
			ShortBias:   inferShortBias(b20.UpNotionalUSD, b20.DownNotionalUSD),
			Bias20Delta: b20.UpNotionalUSD - b20.DownNotionalUSD,
			Bias50Label: verdict50,
		},
		Market:           market,
		ImbalanceStats:   imbalance,
		DensityLayers:    density,
		ChangeTracking:   tracking,
		CoreZone:         core,
		ExchangeContrib:  contrib,
		DominantExchange: dominant,
		Alert:            alert,
	}
}

func (a *App) loadMarketStates(symbol string) ([]MarketState, error) {
	rows, err := a.db.Query(`SELECT exchange, symbol, mark_price, oi_qty, oi_value_usd, funding_rate, long_short_ratio, updated_ts
		FROM market_state WHERE symbol=? ORDER BY exchange`, symbol)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []MarketState{}
	for rows.Next() {
		var s MarketState
		var oiQty, oiValue, funding, lsr sql.NullFloat64
		if err := rows.Scan(&s.Exchange, &s.Symbol, &s.MarkPrice, &oiQty, &oiValue, &funding, &lsr, &s.UpdatedTS); err != nil {
			return nil, err
		}
		if oiQty.Valid {
			s.OIQty = &oiQty.Float64
		}
		if oiValue.Valid {
			s.OIValueUSD = &oiValue.Float64
		}
		if funding.Valid {
			s.FundingRate = &funding.Float64
		}
		if lsr.Valid {
			s.LongShortRatio = &lsr.Float64
		}
		out = append(out, s)
	}
	return out, nil
}

func (a *App) buildModelLiquidationMap(symbol string, lookbackMin, bucketMin int, priceStep, priceRange float64, cfg ModelConfig) (map[string]any, error) {
	now := time.Now().UnixMilli()
	lookbackMS := int64(lookbackMin) * 60 * 1000
	start := now - lookbackMS
	states, err := a.loadMarketStates(symbol)
	if err != nil {
		return nil, err
	}
	current := averageMarkPrice(states)
	if current <= 0 {
		return map[string]any{
			"generated_at":   now,
			"current_price":  0,
			"lookback_min":   lookbackMin,
			"bucket_min":     bucketMin,
			"price_step":     priceStep,
			"price_range":    priceRange,
			"times":          []int64{},
			"prices":         []float64{},
			"intensity_grid": [][]float64{},
			"max_intensity":  0.0,
		}, nil
	}
	current = math.Round(current*10) / 10

	type snap struct {
		ex      string
		mark    float64
		oi      float64
		funding float64
		lsr     float64
		ts      int64
	}
	rows, err := a.db.Query(`SELECT LOWER(exchange), mark_price, oi_value_usd, funding_rate, long_short_ratio, updated_ts
		FROM oi_snapshots WHERE symbol=? AND updated_ts>=? ORDER BY exchange, updated_ts`, symbol, start)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	snaps := make([]snap, 0, 4096)
	for rows.Next() {
		var s snap
		var funding, lsr sql.NullFloat64
		if err := rows.Scan(&s.ex, &s.mark, &s.oi, &funding, &lsr, &s.ts); err != nil {
			continue
		}
		if funding.Valid {
			s.funding = funding.Float64
		}
		if lsr.Valid {
			s.lsr = lsr.Float64
		}
		if s.mark <= 0 || s.oi <= 0 {
			continue
		}
		snaps = append(snaps, s)
	}

	type contrib struct {
		ex        string
		ts        int64
		liqPrice  float64
		intensity float64
		side      string
	}
	contribs := make([]contrib, 0, len(snaps)*6)
	levs := parseCSVFloats(cfg.LeverageCSV)
	weights := parseCSVNonNegFloats(cfg.WeightCSV)
	if len(levs) == 0 {
		levs = []float64{20, 50, 100}
	}
	if len(weights) == 0 || len(weights) != len(levs) {
		weights = make([]float64, len(levs))
		for i := range weights {
			weights[i] = 1.0
		}
	}
	sumW := 0.0
	for _, w := range weights {
		sumW += w
	}
	if sumW <= 0 {
		for i := range weights {
			weights[i] = 1.0
		}
		sumW = float64(len(weights))
	}
	for i := range weights {
		weights[i] /= sumW
	}
	mmDefault := cfg.MaintMargin
	if mmDefault <= 0 {
		mmDefault = 0.005
	}
	fundingDefault := cfg.FundingScale
	if fundingDefault <= 0 {
		fundingDefault = 7000
	}
	mmList := parseCSVFloats(cfg.MaintMarginCSV)
	if len(mmList) == 1 && len(levs) > 1 {
		v := mmList[0]
		mmList = make([]float64, len(levs))
		for i := range mmList {
			mmList[i] = v
		}
	}
	if len(mmList) != len(levs) {
		mmList = make([]float64, len(levs))
		for i := range mmList {
			mmList[i] = mmDefault
		}
	}
	for i := range mmList {
		if mmList[i] <= 0 || mmList[i] > 0.02 {
			mmList[i] = mmDefault
		}
	}
	fundingList := parseCSVFloats(cfg.FundingScaleCSV)
	if len(fundingList) == 1 && len(levs) > 1 {
		v := fundingList[0]
		fundingList = make([]float64, len(levs))
		for i := range fundingList {
			fundingList[i] = v
		}
	}
	if len(fundingList) != len(levs) {
		fundingList = make([]float64, len(levs))
		for i := range fundingList {
			fundingList[i] = fundingDefault
		}
	}
	for i := range fundingList {
		if fundingList[i] < 1000 || fundingList[i] > 20000 {
			fundingList[i] = fundingDefault
		}
	}

	weightCache := map[string][]float64{}
	weightsFor := func(ex string) []float64 {
		ex = strings.ToLower(strings.TrimSpace(ex))
		if ex == "" {
			return weights
		}
		if v, ok := weightCache[ex]; ok {
			return v
		}
		raw := ""
		switch ex {
		case "binance":
			raw = cfg.WeightCSVBinance
		case "okx":
			raw = cfg.WeightCSVOKX
		case "bybit":
			raw = cfg.WeightCSVBybit
		}
		raw = strings.TrimSpace(raw)
		if raw == "" {
			weightCache[ex] = weights
			return weights
		}
		xs := parseCSVNonNegFloats(raw)
		if len(xs) != len(levs) {
			weightCache[ex] = weights
			return weights
		}
		sum := 0.0
		for _, v := range xs {
			sum += v
		}
		if !(sum > 0) {
			weightCache[ex] = weights
			return weights
		}
		for i := range xs {
			xs[i] /= sum
		}
		weightCache[ex] = xs
		return xs
	}

	mmCache := map[string][]float64{}
	mmFor := func(ex string) []float64 {
		ex = strings.ToLower(strings.TrimSpace(ex))
		if ex == "" {
			return mmList
		}
		if v, ok := mmCache[ex]; ok {
			return v
		}
		raw := ""
		switch ex {
		case "binance":
			raw = cfg.MaintMarginCSVBinance
		case "okx":
			raw = cfg.MaintMarginCSVOKX
		case "bybit":
			raw = cfg.MaintMarginCSVBybit
		}
		raw = strings.TrimSpace(raw)
		if raw == "" {
			mmCache[ex] = mmList
			return mmList
		}
		xs := parseCSVFloats(raw)
		if len(xs) == 1 && len(levs) > 1 {
			v := xs[0]
			xs = make([]float64, len(levs))
			for i := range xs {
				xs[i] = v
			}
		}
		if len(xs) != len(levs) {
			mmCache[ex] = mmList
			return mmList
		}
		for i := range xs {
			if xs[i] <= 0 || xs[i] > 0.02 {
				mmCache[ex] = mmList
				return mmList
			}
		}
		mmCache[ex] = xs
		return xs
	}

	decayK := cfg.DecayK
	if decayK <= 0 {
		decayK = 2.2
	}
	neighborShare := cfg.NeighborShare
	if neighborShare < 0 {
		neighborShare = 0
	}
	if neighborShare > 1 {
		neighborShare = 0.28
	}
	type deltaEvent struct {
		ex      string
		ts      int64
		mark    float64
		delta   float64
		funding float64
		lsr     float64
	}
	events := make([]deltaEvent, 0, len(snaps))
	prevOI := map[string]float64{}
	curOI := map[string]float64{}
	effOI := map[string]float64{}
	for _, s := range snaps {
		curOI[s.ex] = s.oi
		prev, ok := prevOI[s.ex]
		prevOI[s.ex] = s.oi
		if !ok {
			continue
		}
		delta := s.oi - prev
		if !(delta > 0) {
			continue
		}
		age := float64(now-s.ts) / float64(lookbackMS)
		decay := math.Exp(-decayK * age)
		effOI[s.ex] += delta * decay
		events = append(events, deltaEvent{
			ex:      s.ex,
			ts:      s.ts,
			mark:    s.mark,
			delta:   delta,
			funding: s.funding,
			lsr:     s.lsr,
		})
	}
	scaleByEx := map[string]float64{}
	for ex, cur := range curOI {
		base := effOI[ex]
		if base > 0 {
			scaleByEx[ex] = clamp(cur/base, 0.3, 3.0)
		} else {
			scaleByEx[ex] = 1.0
		}
	}
	for _, e := range events {
		delta := e.delta * scaleByEx[e.ex]
		wsEx := weightsFor(e.ex)
		mmEx := mmFor(e.ex)
		for i, lev := range levs {
			w := wsEx[i]
			baseLong := 0.5
			if e.lsr > 0 {
				baseLong = clamp(e.lsr/(1+e.lsr), 0.2, 0.8)
			}
			tilt := e.funding * fundingList[i]
			if e.lsr > 0 {
				tilt *= 0.35
			}
			longShare := clamp(baseLong+tilt, 0.2, 0.8)
			shortShare := 1 - longShare
			mm := mmEx[i]
			longAmt := delta * longShare * w
			shortAmt := delta * shortShare * w
			liqLong := e.mark * (1 - 1/lev + mm)
			liqShort := e.mark * (1 + 1/lev - mm)
			if liqLong > 0 && longAmt > 0 {
				contribs = append(contribs, contrib{ex: e.ex, ts: e.ts, liqPrice: liqLong, intensity: longAmt, side: "long"})
			}
			if liqShort > 0 && shortAmt > 0 {
				contribs = append(contribs, contrib{ex: e.ex, ts: e.ts, liqPrice: liqShort, intensity: shortAmt, side: "short"})
			}
		}
	}

	bucketMS := int64(bucketMin) * 60 * 1000
	baseStart := (start / bucketMS) * bucketMS
	cols := int((now-baseStart)/bucketMS) + 1
	if cols < 2 {
		cols = 2
	}
	times := make([]int64, cols)
	for i := 0; i < cols; i++ {
		times[i] = baseStart + int64(i)*bucketMS
	}

	pMin := math.Round((current-priceRange)/priceStep) * priceStep
	pMax := math.Round((current+priceRange)/priceStep) * priceStep
	if pMin <= 0 {
		pMin = priceStep
	}
	rowsN := int(math.Round((pMax-pMin)/priceStep)) + 1
	if rowsN < 20 {
		rowsN = 20
	}
	prices := make([]float64, rowsN)
	for i := 0; i < rowsN; i++ {
		prices[i] = pMin + float64(i)*priceStep
	}
	grid := make([][]float64, rowsN)
	for i := range grid {
		grid[i] = make([]float64, cols)
	}
	exGrid := map[string][][]float64{}
	for _, s := range snaps {
		if s.ex == "" {
			continue
		}
		if _, ok := exGrid[s.ex]; ok {
			continue
		}
		g := make([][]float64, rowsN)
		for i := range g {
			g[i] = make([]float64, cols)
		}
		exGrid[s.ex] = g
	}

	spreadSteps := 4
	spreadW := []float64{1.0, 0.72, 0.42, 0.22}
	spreadSum := 0.0
	for i := 0; i < spreadSteps && i < len(spreadW); i++ {
		spreadSum += spreadW[i]
	}
	if spreadSum <= 0 {
		spreadSum = 1
	}

	for _, c := range contribs {
		if c.ts < baseStart || c.ts > now {
			continue
		}
		// remove triggered zones
		if c.side == "long" && current <= c.liqPrice {
			continue
		}
		if c.side == "short" && current >= c.liqPrice {
			continue
		}
		col := int((c.ts - baseStart) / bucketMS)
		if col < 0 || col >= cols {
			continue
		}
		row := int(math.Round((c.liqPrice - pMin) / priceStep))
		if row < 0 || row >= rowsN {
			continue
		}
		age := float64(now-c.ts) / float64(lookbackMS)
		decay := math.Exp(-decayK * age)
		v := c.intensity * decay * cfg.IntensityScale
		grid[row][col] += v
		if g, ok := exGrid[c.ex]; ok {
			g[row][col] += v
		}
		if neighborShare > 0 {
			for k := 1; k <= spreadSteps; k++ {
				if k-1 >= len(spreadW) {
					break
				}
				vv := v * neighborShare * (spreadW[k-1] / spreadSum)
				if vv <= 0 {
					continue
				}
				if row-k >= 0 {
					grid[row-k][col] += vv
					if g, ok := exGrid[c.ex]; ok {
						g[row-k][col] += vv
					}
				}
				if row+k < rowsN {
					grid[row+k][col] += vv
					if g, ok := exGrid[c.ex]; ok {
						g[row+k][col] += vv
					}
				}
			}
		}
	}

	maxV := 0.0
	for i := range grid {
		for j := range grid[i] {
			if grid[i][j] > maxV {
				maxV = grid[i][j]
			}
		}
	}
	perExPrice := map[string][]float64{}
	for ex, g := range exGrid {
		out := make([]float64, rowsN)
		for ri := 0; ri < rowsN; ri++ {
			sum := 0.0
			for ci := 0; ci < cols; ci++ {
				if g[ri][ci] > 0 {
					sum += g[ri][ci]
				}
			}
			out[ri] = sum
		}
		perExPrice[ex] = out
	}
	return map[string]any{
		"generated_at":   now,
		"current_price":  current,
		"lookback_min":   lookbackMin,
		"bucket_min":     bucketMin,
		"price_step":     priceStep,
		"price_range":    priceRange,
		"times":          times,
		"prices":         prices,
		"intensity_grid": grid,
		"per_exchange":   perExPrice,
		"max_intensity":  maxV,
	}, nil
}

func (a *App) loadHeatSnapshot(symbol string, cutoff int64) ([]BandRow, []any, []any, error) {
	var latestTS int64
	if err := a.db.QueryRow(`SELECT report_ts FROM band_reports WHERE symbol=? AND report_ts>=? ORDER BY report_ts DESC LIMIT 1`, symbol, cutoff).Scan(&latestTS); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return []BandRow{}, []any{"-", 0}, []any{"-", 0}, nil
		}
		return nil, nil, nil, err
	}

	rows, err := a.db.Query(`SELECT band, AVG(up_price), AVG(up_notional_usd), AVG(down_price), AVG(down_notional_usd)
		FROM band_reports WHERE symbol=? AND report_ts=? GROUP BY band ORDER BY band`, symbol, latestTS)
	if err != nil {
		return nil, nil, nil, err
	}
	defer rows.Close()
	out := []BandRow{}
	for rows.Next() {
		var r BandRow
		if err := rows.Scan(&r.Band, &r.UpPrice, &r.UpNotionalUSD, &r.DownPrice, &r.DownNotionalUSD); err != nil {
			return nil, nil, nil, err
		}
		out = append(out, r)
	}
	short := a.loadLongestBarAt(symbol, latestTS, "short")
	long := a.loadLongestBarAt(symbol, latestTS, "long")
	return out, short, long, nil
}

func (a *App) loadLongestBarAt(symbol string, reportTS int64, side string) []any {
	row := a.db.QueryRow(`SELECT bucket_price, bucket_notional_usd FROM longest_bar_reports
		WHERE symbol=? AND side=? AND report_ts=? ORDER BY bucket_notional_usd DESC LIMIT 1`, symbol, side, reportTS)
	var price, notional sql.NullFloat64
	if err := row.Scan(&price, &notional); err != nil {
		return []any{"-", 0}
	}
	p := "-"
	if price.Valid {
		p = fmt.Sprintf("%.1f", price.Float64)
	}
	n := 0.0
	if notional.Valid {
		n = notional.Float64
	}
	return []any{p, n}
}

func (a *App) loadRecentEvents(symbol string, cutoff int64) []EventRow {
	rows, err := a.db.Query(`SELECT exchange, side, price, qty, notional_usd, event_ts FROM liquidation_events
		WHERE symbol=? AND event_ts>=? ORDER BY event_ts DESC LIMIT 30`, symbol, cutoff)
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := []EventRow{}
	for rows.Next() {
		var e EventRow
		if err := rows.Scan(&e.Exchange, &e.Side, &e.Price, &e.Qty, &e.NotionalUSD, &e.EventTS); err != nil {
			return nil
		}
		out = append(out, e)
	}
	return out
}

func (a *App) loadLiquidations(opts LiquidationListOptions) []EventRow {
	if opts.Limit <= 0 {
		opts.Limit = 25
	}
	if opts.Limit > 10000 {
		opts.Limit = 10000
	}
	if opts.Offset < 0 {
		opts.Offset = 0
	}
	symbol := normalizeLiquidationSymbolFilter(opts.Symbol)
	baseSQL := `SELECT exchange, symbol, side, price, qty, notional_usd, event_ts
		FROM liquidation_events WHERE 1=1`
	args := []any{}
	if symbol != "ALL" {
		baseSQL += ` AND symbol=?`
		args = append(args, symbol)
	}
	if opts.StartTS > 0 {
		baseSQL += ` AND event_ts >= ?`
		args = append(args, opts.StartTS)
	}
	if opts.EndTS > 0 {
		baseSQL += ` AND event_ts <= ?`
		args = append(args, opts.EndTS)
	}
	if opts.MinValue > 0 {
		switch strings.ToLower(strings.TrimSpace(opts.FilterField)) {
		case "qty":
			baseSQL += ` AND qty >= ?`
			args = append(args, opts.MinValue)
		case "amount", "notional", "notional_usd":
			baseSQL += ` AND notional_usd >= ?`
			args = append(args, opts.MinValue)
		}
	}
	baseSQL += ` ORDER BY event_ts DESC LIMIT ? OFFSET ?`
	args = append(args, opts.Limit, opts.Offset)
	rows, err := a.db.Query(baseSQL, args...)
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := make([]EventRow, 0, opts.Limit)
	for rows.Next() {
		var e EventRow
		if err := rows.Scan(&e.Exchange, &e.Symbol, &e.Side, &e.Price, &e.Qty, &e.NotionalUSD, &e.EventTS); err != nil {
			continue
		}
		out = append(out, e)
	}
	return out
}

func newOrderBookHub() *OrderBookHub {
	return &OrderBookHub{
		books: map[string]*OrderBook{
			"binance": {Exchange: "binance", Symbol: "ETHUSDT", Bids: map[string]float64{}, Asks: map[string]float64{}},
			"bybit":   {Exchange: "bybit", Symbol: "ETHUSDT", Bids: map[string]float64{}, Asks: map[string]float64{}},
			"okx":     {Exchange: "okx", Symbol: "ETH-USDT-SWAP", Bids: map[string]float64{}, Asks: map[string]float64{}},
		},
	}
}

func (h *OrderBookHub) get(exchange string) *OrderBook {
	return h.books[strings.ToLower(exchange)]
}

func normalizePriceKey(price float64) string {
	return strconv.FormatFloat(price, 'f', -1, 64)
}

func (b *OrderBook) applySide(side map[string]float64, price, qty float64) {
	if price <= 0 {
		return
	}
	key := normalizePriceKey(price)
	if qty <= 0 {
		delete(side, key)
		return
	}
	side[key] = qty
}

func parseAnyFloat(v any) float64 {
	switch t := v.(type) {
	case float64:
		return t
	case string:
		f, _ := strconv.ParseFloat(strings.TrimSpace(t), 64)
		return f
	case json.Number:
		f, _ := t.Float64()
		return f
	default:
		f, _ := strconv.ParseFloat(strings.TrimSpace(fmt.Sprint(v)), 64)
		return f
	}
}

func asPairs(v any) [][2]float64 {
	rows, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([][2]float64, 0, len(rows))
	for _, r := range rows {
		arr, ok := r.([]any)
		if !ok || len(arr) < 2 {
			continue
		}
		p := parseAnyFloat(arr[0])
		q := parseAnyFloat(arr[1])
		if p <= 0 {
			continue
		}
		out = append(out, [2]float64{p, q})
	}
	return out
}

func (b *OrderBook) snapshot(bids [][2]float64, asks [][2]float64, updateID int64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.Bids = map[string]float64{}
	b.Asks = map[string]float64{}
	for _, lv := range bids {
		if lv[1] > 0 {
			b.Bids[normalizePriceKey(lv[0])] = lv[1]
		}
	}
	for _, lv := range asks {
		if lv[1] > 0 {
			b.Asks[normalizePriceKey(lv[0])] = lv[1]
		}
	}
	b.LastUpdateID = updateID
	b.LastSeq = 0
	now := time.Now().UnixMilli()
	b.UpdatedTS = now
	b.LastSnapshot = now
}

func (b *OrderBook) applyDelta(bids [][2]float64, asks [][2]float64, updateID int64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, lv := range bids {
		b.applySide(b.Bids, lv[0], lv[1])
	}
	for _, lv := range asks {
		b.applySide(b.Asks, lv[0], lv[1])
	}
	if updateID > b.LastUpdateID {
		b.LastUpdateID = updateID
	}
	now := time.Now().UnixMilli()
	b.UpdatedTS = now
	b.LastWSEventTS = now
}

func sideTop(side map[string]float64, limit int, bid bool) []Level {
	out := make([]Level, 0, len(side))
	for k, q := range side {
		p, err := strconv.ParseFloat(k, 64)
		if err != nil || p <= 0 || q <= 0 {
			continue
		}
		out = append(out, Level{Price: p, Qty: q})
	}
	sort.Slice(out, func(i, j int) bool {
		if bid {
			return out[i].Price > out[j].Price
		}
		return out[i].Price < out[j].Price
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

func copySide(side map[string]float64) map[string]float64 {
	out := make(map[string]float64, len(side))
	for k, v := range side {
		out[k] = v
	}
	return out
}

func (b *OrderBook) view(limit int) map[string]any {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return map[string]any{
		"exchange":       b.Exchange,
		"symbol":         b.Symbol,
		"last_update_id": b.LastUpdateID,
		"last_seq":       b.LastSeq,
		"updated_ts":     b.UpdatedTS,
		"last_snapshot":  b.LastSnapshot,
		"last_ws_event":  b.LastWSEventTS,
		"bids":           sideTop(b.Bids, limit, true),
		"asks":           sideTop(b.Asks, limit, false),
	}
}

func (a *App) startOrderBookSync(ctx context.Context) {
	go a.syncBinanceOrderBook(ctx, "ETHUSDT")
	go a.syncBybitOrderBook(ctx, "ETHUSDT")
	go a.syncOKXOrderBook(ctx, "ETH-USDT-SWAP")
}

func normalizeLiquidationSide(raw string) string {
	s := strings.ToLower(strings.TrimSpace(raw))
	switch s {
	case "buy", "b", "short":
		return "short"
	case "sell", "s", "long":
		return "long"
	default:
		return s
	}
}

func invertLiquidationSide(raw string) string {
	switch normalizeLiquidationSide(raw) {
	case "long":
		return "short"
	case "short":
		return "long"
	default:
		return strings.ToLower(strings.TrimSpace(raw))
	}
}

func (a *App) insertLiquidationEvent(exchange, symbol, side, rawSide string, price, qty, markPrice float64, eventTS int64) {
	if price <= 0 || qty <= 0 {
		return
	}
	symbol = normalizeLiquidationEventSymbol(exchange, symbol)
	a.mergeLiquidationSymbols([]string{symbol})
	if markPrice <= 0 {
		// Try to get current mark price from market_state table
		var currentMarkPrice float64
		err := a.db.QueryRow(`SELECT mark_price FROM market_state WHERE exchange=? AND symbol=?`, exchange, symbol).Scan(&currentMarkPrice)
		if err == nil && currentMarkPrice > 0 {
			markPrice = currentMarkPrice
		} else {
			// Fallback to price if we can't get mark price
			markPrice = price
		}
	}
	if eventTS <= 0 {
		eventTS = time.Now().UnixMilli()
	}
	notional := price * qty
	normSide := normalizeLiquidationSide(side)
	if normSide == "" {
		normSide = normalizeLiquidationSide(rawSide)
	}
	_, _ = a.db.Exec(`INSERT OR IGNORE INTO liquidation_events(exchange, symbol, side, raw_side, qty, price, mark_price, notional_usd, event_ts, inserted_ts)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		exchange, symbol, normSide, rawSide, qty, price, markPrice, notional, eventTS, time.Now().UnixMilli())
}

func (a *App) startLiquidationSync(ctx context.Context) {
	okxCtVal := a.fetchOKXContractValue("ETH-USDT-SWAP")
	if okxCtVal <= 0 {
		okxCtVal = 1
	}
	go a.backfillOKXLiquidations(ctx, "ETH-USDT-SWAP", okxCtVal, 24)
	go a.syncBinanceAllLiquidations(ctx)
	go a.syncBybitAllLiquidations(ctx)
	go a.syncOKXAllLiquidations(ctx)
}

func (a *App) startModelMapSnapshotter(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		time.Sleep(2 * time.Second)
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
			cfg := a.loadModelConfig()
			windowDays := a.window()
			configRev := a.getSettingInt64("model_config_rev", 0) + modelAlgoRev*1_000_000
			resp, err := a.buildModelLiquidationMap(defaultSymbol, lookbackMinForWindow(time.Now(), windowDays), cfg.BucketMin, cfg.PriceStep, cfg.PriceRange, cfg)
			if err != nil {
				if a.debug {
					log.Printf("model liqmap snapshot err: %v", err)
				}
				continue
			}
			a.saveModelMapSnapshot(defaultSymbol, windowDays, configRev, resp)
		}
	}()
}

func (a *App) fetchOKXContractValue(instID string) float64 {
	var resp struct {
		Code string `json:"code"`
		Data []struct {
			CtVal string `json:"ctVal"`
		} `json:"data"`
	}
	if err := a.fetchJSON("https://www.okx.com/api/v5/public/instruments?instType=SWAP&instId="+instID, &resp); err != nil {
		return 1
	}
	if len(resp.Data) == 0 {
		return 1
	}
	v := parseFloat(resp.Data[0].CtVal)
	if v <= 0 {
		return 1
	}
	return v
}

func (a *App) backfillOKXLiquidations(ctx context.Context, instID string, ctVal float64, lookbackHours int) {
	var resp struct {
		Code string `json:"code"`
		Data []struct {
			Details []struct {
				BkPx    string `json:"bkPx"`
				Side    string `json:"side"`
				PosSide string `json:"posSide"`
				Sz      string `json:"sz"`
				TS      string `json:"ts"`
				Time    int64  `json:"time"`
			} `json:"details"`
		} `json:"data"`
	}
	if err := a.fetchJSON("https://www.okx.com/api/v5/public/liquidation-orders?instType=SWAP&state=filled&uly=ETH-USDT", &resp); err != nil {
		if a.debug {
			log.Printf("okx liquidation backfill err: %v", err)
		}
		return
	}
	cutoff := time.Now().Add(-time.Duration(lookbackHours) * time.Hour).UnixMilli()
	for _, grp := range resp.Data {
		for _, d := range grp.Details {
			select {
			case <-ctx.Done():
				return
			default:
			}
			ts := int64(parseAnyFloat(d.TS))
			if ts <= 0 {
				ts = d.Time
			}
			if ts < cutoff {
				continue
			}
			price := parseFloat(d.BkPx)
			qty := parseFloat(d.Sz) * ctVal
			side := d.PosSide
			if side == "" {
				side = d.Side
			}
			a.insertLiquidationEvent("okx", defaultSymbol, side, d.Side, price, qty, 0, ts)
		}
	}
}

func (a *App) syncBinanceLiquidations(ctx context.Context, symbol string) {
	wsURL := "wss://fstream.binance.com/market/ws/" + strings.ToLower(symbol) + "@forceOrder"
	for {
		if !a.waitExchangeRetry(ctx, "binance", 0) {
			return
		}
		conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
		if err != nil {
			if a.debug {
				log.Printf("binance liquidation ws dial err: %v", err)
			}
			if !a.waitExchangeRetry(ctx, "binance", 2*time.Second) {
				return
			}
			continue
		}
		for {
			select {
			case <-ctx.Done():
				_ = conn.Close()
				return
			default:
			}
			var msg map[string]any
			if err := conn.ReadJSON(&msg); err != nil {
				break
			}
			o, ok := msg["o"].(map[string]any)
			if !ok {
				continue
			}
			side := fmt.Sprint(o["S"])
			price := parseAnyFloat(o["ap"])
			if price <= 0 {
				price = parseAnyFloat(o["p"])
			}
			qty := parseAnyFloat(o["q"])
			if qty <= 0 {
				qty = parseAnyFloat(o["z"])
			}
			ts := int64(parseAnyFloat(o["T"]))
			a.insertLiquidationEvent("binance", symbol, side, side, price, qty, 0, ts)
		}
		_ = conn.Close()
		if !a.waitExchangeRetry(ctx, "binance", 2*time.Second) {
			return
		}
	}
}

func (a *App) syncBybitLiquidations(ctx context.Context, symbol string) {
	for {
		if !a.waitExchangeRetry(ctx, "bybit", 0) {
			return
		}
		conn, _, err := websocket.DefaultDialer.Dial("wss://stream.bybit.com/v5/public/linear", nil)
		if err != nil {
			if a.debug {
				log.Printf("bybit liquidation ws dial err: %v", err)
			}
			if !a.waitExchangeRetry(ctx, "bybit", 2*time.Second) {
				return
			}
			continue
		}
		sub := map[string]any{"op": "subscribe", "args": []string{"allLiquidation." + symbol}}
		if err := conn.WriteJSON(sub); err != nil {
			_ = conn.Close()
			if !a.waitExchangeRetry(ctx, "bybit", 2*time.Second) {
				return
			}
			continue
		}
		for {
			select {
			case <-ctx.Done():
				_ = conn.Close()
				return
			default:
			}
			var msg map[string]any
			if err := conn.ReadJSON(&msg); err != nil {
				break
			}
			dataArr, ok := msg["data"].([]any)
			if !ok {
				continue
			}
			for _, it := range dataArr {
				row, ok := it.(map[string]any)
				if !ok {
					continue
				}
				side := invertLiquidationSide(fmt.Sprint(row["S"]))
				price := parseAnyFloat(row["p"])
				qty := parseAnyFloat(row["v"])
				if qty <= 0 {
					qty = parseAnyFloat(row["q"])
				}
				ts := int64(parseAnyFloat(row["T"]))
				a.insertLiquidationEvent("bybit", symbol, side, side, price, qty, 0, ts)
			}
		}
		_ = conn.Close()
		if !a.waitExchangeRetry(ctx, "bybit", 2*time.Second) {
			return
		}
	}
}

func (a *App) syncOKXLiquidations(ctx context.Context, instID string, ctVal float64) {
	for {
		if !a.waitExchangeRetry(ctx, "okx", 0) {
			return
		}
		conn, _, err := websocket.DefaultDialer.Dial("wss://ws.okx.com:8443/ws/v5/public", nil)
		if err != nil {
			if a.debug {
				log.Printf("okx liquidation ws dial err: %v", err)
			}
			if !a.waitExchangeRetry(ctx, "okx", 2*time.Second) {
				return
			}
			continue
		}
		sub := map[string]any{"op": "subscribe", "args": []map[string]string{{"channel": "liquidation-orders", "instType": "SWAP", "instId": instID}}}
		if err := conn.WriteJSON(sub); err != nil {
			_ = conn.Close()
			if !a.waitExchangeRetry(ctx, "okx", 2*time.Second) {
				return
			}
			continue
		}
		for {
			select {
			case <-ctx.Done():
				_ = conn.Close()
				return
			default:
			}
			var msg map[string]any
			if err := conn.ReadJSON(&msg); err != nil {
				break
			}
			dataArr, ok := msg["data"].([]any)
			if !ok {
				continue
			}
			for _, it := range dataArr {
				row, ok := it.(map[string]any)
				if !ok {
					continue
				}
				side := fmt.Sprint(row["side"])
				price := parseAnyFloat(row["bkPx"])
				if price <= 0 {
					price = parseAnyFloat(row["px"])
				}
				qty := parseAnyFloat(row["sz"]) * ctVal
				ts := int64(parseAnyFloat(row["ts"]))
				posSide := fmt.Sprint(row["posSide"])
				if strings.TrimSpace(posSide) == "" {
					posSide = side
				}
				a.insertLiquidationEvent("okx", defaultSymbol, posSide, side, price, qty, 0, ts)
			}
		}
		_ = conn.Close()
		if !a.waitExchangeRetry(ctx, "okx", 2*time.Second) {
			return
		}
	}
}

func (a *App) syncBinanceOrderBook(ctx context.Context, symbol string) {
	book := a.ob.get("binance")
	if book == nil {
		return
	}
	for {
		if !a.waitExchangeRetry(ctx, "binance", 0) {
			return
		}
		if err := a.loadBinanceSnapshot(symbol); err != nil {
			if a.debug && !isExchangePausedError(err) {
				log.Printf("binance snapshot err: %v", err)
			}
			if !a.waitExchangeRetry(ctx, "binance", 2*time.Second) {
				return
			}
			continue
		}
		if !a.waitExchangeRetry(ctx, "binance", 0) {
			return
		}
		if err := a.runBinanceWS(ctx, symbol); err != nil && a.debug && !errors.Is(err, errResnapshot) {
			log.Printf("binance ws err: %v", err)
		}
		if !a.waitExchangeRetry(ctx, "binance", 2*time.Second) {
			return
		}
		_ = book
	}
}

func (a *App) loadBinanceSnapshot(symbol string) error {
	var resp struct {
		LastUpdateID int64   `json:"lastUpdateId"`
		Bids         [][]any `json:"bids"`
		Asks         [][]any `json:"asks"`
	}
	if err := a.fetchJSON("https://api.binance.com/api/v3/depth?symbol="+symbol+"&limit=200", &resp); err != nil {
		return err
	}
	b := a.ob.get("binance")
	bids := make([][2]float64, 0, len(resp.Bids))
	for _, r := range resp.Bids {
		if len(r) < 2 {
			continue
		}
		bids = append(bids, [2]float64{parseAnyFloat(r[0]), parseAnyFloat(r[1])})
	}
	asks := make([][2]float64, 0, len(resp.Asks))
	for _, r := range resp.Asks {
		if len(r) < 2 {
			continue
		}
		asks = append(asks, [2]float64{parseAnyFloat(r[0]), parseAnyFloat(r[1])})
	}
	b.snapshot(bids, asks, resp.LastUpdateID)
	return nil
}

func (a *App) runBinanceWS(ctx context.Context, symbol string) error {
	u := url.URL{Scheme: "wss", Host: "stream.binance.com:9443", Path: "/ws/" + strings.ToLower(symbol) + "@depth@100ms"}
	return a.runBinanceWSConn(ctx, u.String(), false)
}

func (a *App) runBinanceWSConn(ctx context.Context, wsURL string, checkPU bool) error {
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		return err
	}
	defer conn.Close()
	stopKeepalive := startWSKeepalive(ctx, conn, 90*time.Second, wsKeepalivePingInterval)
	defer stopKeepalive()
	book := a.ob.get("binance")
	book.mu.RLock()
	snapshotID := book.LastUpdateID
	book.mu.RUnlock()
	firstApplied := false
	nextResnapshot := time.Now().Add(5 * time.Minute)
	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}
		if time.Now().After(nextResnapshot) {
			return errResnapshot
		}
		var msg map[string]any
		if err := conn.ReadJSON(&msg); err != nil {
			return err
		}
		_ = conn.SetReadDeadline(time.Now().Add(90 * time.Second))
		uVal := int64(parseAnyFloat(msg["u"]))
		UVal := int64(parseAnyFloat(msg["U"]))
		puVal := int64(parseAnyFloat(msg["pu"]))
		if uVal <= 0 || UVal <= 0 {
			continue
		}
		if !firstApplied {
			if uVal < snapshotID {
				continue
			}
			if checkPU {
				if !(UVal <= snapshotID+1 && uVal >= snapshotID+1) {
					return fmt.Errorf("binance first delta not contiguous: snapshot=%d U=%d u=%d", snapshotID, UVal, uVal)
				}
			} else {
				if uVal <= snapshotID {
					continue
				}
			}
			firstApplied = true
		} else {
			book.mu.RLock()
			lastID := book.LastUpdateID
			book.mu.RUnlock()
			if checkPU && puVal > 0 && puVal != lastID {
				return fmt.Errorf("binance sequence broken: pu=%d last=%d", puVal, lastID)
			}
			if uVal <= lastID {
				continue
			}
		}
		bids := asPairs(msg["b"])
		asks := asPairs(msg["a"])
		book.applyDelta(bids, asks, uVal)
	}
}

func (a *App) syncBybitOrderBook(ctx context.Context, symbol string) {
	for {
		if !a.waitExchangeRetry(ctx, "bybit", 0) {
			return
		}
		if err := a.loadBybitSnapshot(symbol); err != nil {
			if a.debug && !isExchangePausedError(err) {
				log.Printf("bybit snapshot err: %v", err)
			}
			if !a.waitExchangeRetry(ctx, "bybit", 2*time.Second) {
				return
			}
			continue
		}
		if !a.waitExchangeRetry(ctx, "bybit", 0) {
			return
		}
		if err := a.runBybitWS(ctx, symbol); err != nil && a.debug && !errors.Is(err, errResnapshot) {
			log.Printf("bybit ws err: %v", err)
		}
		if !a.waitExchangeRetry(ctx, "bybit", 2*time.Second) {
			return
		}
	}
}

func (a *App) loadBybitSnapshot(symbol string) error {
	var resp struct {
		RetCode int    `json:"retCode"`
		RetMsg  string `json:"retMsg"`
		Result  struct {
			B [][]any `json:"b"`
			A [][]any `json:"a"`
			U int64   `json:"u"`
		} `json:"result"`
	}
	if err := a.fetchJSON("https://api.bybit.com/v5/market/orderbook?category=linear&symbol="+symbol+"&limit=200", &resp); err != nil {
		return err
	}
	if resp.RetCode != 0 {
		return fmt.Errorf("bybit retCode=%d msg=%s", resp.RetCode, resp.RetMsg)
	}
	bids := make([][2]float64, 0, len(resp.Result.B))
	for _, r := range resp.Result.B {
		if len(r) < 2 {
			continue
		}
		bids = append(bids, [2]float64{parseAnyFloat(r[0]), parseAnyFloat(r[1])})
	}
	asks := make([][2]float64, 0, len(resp.Result.A))
	for _, r := range resp.Result.A {
		if len(r) < 2 {
			continue
		}
		asks = append(asks, [2]float64{parseAnyFloat(r[0]), parseAnyFloat(r[1])})
	}
	a.ob.get("bybit").snapshot(bids, asks, resp.Result.U)
	return nil
}

func (a *App) runBybitWS(ctx context.Context, symbol string) error {
	conn, _, err := websocket.DefaultDialer.Dial("wss://stream.bybit.com/v5/public/linear", nil)
	if err != nil {
		return err
	}
	defer conn.Close()
	sub := map[string]any{"op": "subscribe", "args": []string{"orderbook.50." + symbol}}
	if err := conn.WriteJSON(sub); err != nil {
		return err
	}
	book := a.ob.get("bybit")
	nextResnapshot := time.Now().Add(5 * time.Minute)
	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}
		if time.Now().After(nextResnapshot) {
			return errResnapshot
		}
		var msg map[string]any
		if err := conn.ReadJSON(&msg); err != nil {
			return err
		}
		topic, _ := msg["topic"].(string)
		if !strings.Contains(topic, "orderbook") {
			continue
		}
		data, ok := msg["data"].(map[string]any)
		if !ok {
			continue
		}
		bids := asPairs(data["b"])
		asks := asPairs(data["a"])
		uVal := int64(parseAnyFloat(data["u"]))
		seqVal := int64(parseAnyFloat(data["seq"]))
		typ, _ := msg["type"].(string)
		if strings.EqualFold(typ, "snapshot") {
			book.snapshot(bids, asks, uVal)
			book.mu.Lock()
			book.LastSeq = seqVal
			book.LastWSEventTS = time.Now().UnixMilli()
			book.mu.Unlock()
			continue
		}
		book.mu.RLock()
		lastU := book.LastUpdateID
		lastSeq := book.LastSeq
		book.mu.RUnlock()
		if uVal <= lastU {
			continue
		}
		// Bybit's seq is not guaranteed to be contiguous per client stream; treat it as
		// a monotonic ordering hint and only resnapshot on rollback/out-of-order frames.
		if seqVal > 0 && lastSeq > 0 && seqVal < lastSeq {
			return fmt.Errorf("bybit sequence rollback: seq=%d last=%d", seqVal, lastSeq)
		}
		book.applyDelta(bids, asks, uVal)
		if seqVal > 0 {
			book.mu.Lock()
			if seqVal > book.LastSeq {
				book.LastSeq = seqVal
			}
			book.mu.Unlock()
		}
	}
}

func (a *App) syncOKXOrderBook(ctx context.Context, instID string) {
	for {
		if !a.waitExchangeRetry(ctx, "okx", 0) {
			return
		}
		if err := a.loadOKXSnapshot(instID); err != nil {
			if a.debug && !isExchangePausedError(err) {
				log.Printf("okx snapshot err: %v", err)
			}
			if !a.waitExchangeRetry(ctx, "okx", 2*time.Second) {
				return
			}
			continue
		}
		if !a.waitExchangeRetry(ctx, "okx", 0) {
			return
		}
		if err := a.runOKXWS(ctx, instID); err != nil && a.debug && !errors.Is(err, errResnapshot) {
			log.Printf("okx ws err: %v", err)
		}
		if !a.waitExchangeRetry(ctx, "okx", 2*time.Second) {
			return
		}
	}
}

func (a *App) loadOKXSnapshot(instID string) error {
	var resp struct {
		Code string `json:"code"`
		Data []struct {
			Bids [][]any `json:"bids"`
			Asks [][]any `json:"asks"`
			TS   string  `json:"ts"`
		} `json:"data"`
	}
	if err := a.fetchJSON("https://www.okx.com/api/v5/market/books?instId="+instID+"&sz=200", &resp); err != nil {
		return err
	}
	if len(resp.Data) == 0 {
		return errors.New("okx empty books")
	}
	d := resp.Data[0]
	bids := make([][2]float64, 0, len(d.Bids))
	for _, r := range d.Bids {
		if len(r) < 2 {
			continue
		}
		bids = append(bids, [2]float64{parseAnyFloat(r[0]), parseAnyFloat(r[1])})
	}
	asks := make([][2]float64, 0, len(d.Asks))
	for _, r := range d.Asks {
		if len(r) < 2 {
			continue
		}
		asks = append(asks, [2]float64{parseAnyFloat(r[0]), parseAnyFloat(r[1])})
	}
	u := int64(parseAnyFloat(d.TS))
	a.ob.get("okx").snapshot(bids, asks, u)
	return nil
}

func (a *App) runOKXWS(ctx context.Context, instID string) error {
	conn, _, err := websocket.DefaultDialer.Dial("wss://ws.okx.com:8443/ws/v5/public", nil)
	if err != nil {
		return err
	}
	defer conn.Close()
	sub := map[string]any{
		"op":   "subscribe",
		"args": []map[string]string{{"channel": "books", "instId": instID}},
	}
	if err := conn.WriteJSON(sub); err != nil {
		return err
	}
	book := a.ob.get("okx")
	nextResnapshot := time.Now().Add(5 * time.Minute)
	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}
		if time.Now().After(nextResnapshot) {
			return errResnapshot
		}
		var msg map[string]any
		if err := conn.ReadJSON(&msg); err != nil {
			return err
		}
		dataArr, ok := msg["data"].([]any)
		if !ok || len(dataArr) == 0 {
			continue
		}
		first, ok := dataArr[0].(map[string]any)
		if !ok {
			continue
		}
		bids := asPairs(first["bids"])
		asks := asPairs(first["asks"])
		ts := int64(parseAnyFloat(first["ts"]))
		action, _ := msg["action"].(string)
		if strings.EqualFold(action, "snapshot") {
			book.snapshot(bids, asks, ts)
			continue
		}
		book.applyDelta(bids, asks, ts)
	}
}

var (
	analysisHTMLFallback = loadPageHTML("internal/shared/pages/files/analysis_page_utf8.html", missingPageHTML("日内分析", "internal/shared/pages/files/analysis_page_utf8.html"))
	indexHTML            = loadPageHTML("internal/shared/pages/files/home_page_utf8.html", missingPageHTML("清算热区", "internal/shared/pages/files/home_page_utf8.html"))
	monitorHTML          = loadPageHTML("internal/shared/pages/files/monitor_page_utf8.html", missingPageHTML("雷区监控", "internal/shared/pages/files/monitor_page_utf8.html"))
	mapHTML              = loadPageHTML("internal/shared/pages/files/map_page_fixed.html", missingPageHTML("盘口汇总", "internal/shared/pages/files/map_page_fixed.html"))
	channelHTML          = loadPageHTML("internal/shared/pages/files/channel_page_utf8.html", missingPageHTML("消息通道", "internal/shared/pages/files/channel_page_utf8.html"))
	channelHTMLV2        = loadPageHTML("internal/shared/pages/files/channel_page_utf8.html", missingPageHTML("消息通道", "internal/shared/pages/files/channel_page_utf8.html"))
	liquidationsHTML     = loadPageHTML("internal/shared/pages/files/liquidations_page_fixed.html", missingPageHTML("强平清算", "internal/shared/pages/files/liquidations_page_fixed.html"))
	bubblesHTML          = loadPageHTML("internal/shared/pages/files/bubbles_page_fixed.html", missingPageHTML("气泡图", "internal/shared/pages/files/bubbles_page_fixed.html"))
	configHTML           = loadPageHTML("internal/shared/pages/files/config_page_fixed.html", missingPageHTML("模型配置", "internal/shared/pages/files/config_page_fixed.html"))
)
