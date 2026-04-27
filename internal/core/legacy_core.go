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
			msg := fmt.Sprintf("鏁版嵁缂哄け: %s", errText)
			a.recordTelegramSendHistory(sendMode, 1, "webdatasource-30d-image", "failed", msg)
			errs = append(errs, msg)
		} else {
			webImage, err := a.captureWebDataSourceScreenshotJPEG("30d")
			if err != nil {
				msg := fmt.Sprintf("鎴浘澶辫触: %v", err)
				a.recordTelegramSendHistory(sendMode, 1, "webdatasource-30d-image", "failed", msg)
				errs = append(errs, msg)
			} else if err := a.sendTelegramPhoto("", webImage); err != nil {
				msg := fmt.Sprintf("鍙戦€佸け璐? %v", err)
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
			msg := fmt.Sprintf("鎴浘澶辫触: %v", err)
			a.recordTelegramSendHistory(sendMode, 2, "monitor-30d-image", "failed", msg)
			errs = append(errs, msg)
		} else if err := a.sendTelegramPhoto("", monitorImage); err != nil {
			msg := fmt.Sprintf("鍙戦€佸け璐? %v", err)
			a.recordTelegramSendHistory(sendMode, 2, "monitor-30d-image", "failed", msg)
			errs = append(errs, msg)
		} else {
			a.recordTelegramSendHistory(sendMode, 2, "monitor-30d-image", "success", "")
		}

	}

	if settings.Group3Enabled {
		text := a.buildTelegramThirtyDayTextSafe(monitorReport, monitorBands, webMap)
		if err := a.sendTelegramText(text); err != nil {
			msg := fmt.Sprintf("鍙戦€佸け璐? %v", err)
			a.recordTelegramSendHistory(sendMode, 3, "monitor-30d-text", "failed", msg)
			errs = append(errs, msg)
		} else {
			a.recordTelegramSendHistory(sendMode, 3, "monitor-30d-text", "success", "")
		}

	}

	if settings.Group4Enabled {
		analysisImage, err := a.captureAnalysisScreenshotJPEG()
		if err != nil {
			msg := fmt.Sprintf("鎴浘澶辫触: %v", err)
			a.recordTelegramSendHistory(sendMode, 4, "analysis-image", "failed", msg)
			errs = append(errs, msg)
		} else if err := a.sendTelegramPhoto("", analysisImage); err != nil {
			msg := fmt.Sprintf("鍙戦€佸け璐? %v", err)
			a.recordTelegramSendHistory(sendMode, 4, "analysis-image", "failed", msg)
			errs = append(errs, msg)
		} else {
			a.recordTelegramSendHistory(sendMode, 4, "analysis-image", "success", "")
		}
	}

	if settings.Group5Enabled {
		snapshot, err := loadAnalysisSnapshot()
		if err != nil {
			msg := fmt.Sprintf("鐢熸垚鏃ュ唴鍒嗘瀽澶辫触: %v", err)
			a.recordTelegramSendHistory(sendMode, 5, "analysis-text", "failed", msg)
			errs = append(errs, msg)
		} else if err := a.sendTelegramText(a.buildAnalysisTelegramTextSafe(snapshot)); err != nil {
			msg := fmt.Sprintf("鍙戦€佸け璐? %v", err)
			a.recordTelegramSendHistory(sendMode, 5, "analysis-text", "failed", msg)
			errs = append(errs, msg)
		} else {
			a.recordTelegramSendHistory(sendMode, 5, "analysis-text", "success", "")
		}
	}

	if settings.Group6Enabled {
		structureImage, err := a.captureLiquidationsStructureScreenshotJPEG()
		if err != nil {
			msg := fmt.Sprintf("鍙浘澶辫触: %v", err)
			a.recordTelegramSendHistory(sendMode, 6, "liquidations-structure-image", "failed", msg)
			errs = append(errs, msg)
		} else if err := a.sendTelegramPhoto("", structureImage); err != nil {
			msg := fmt.Sprintf("鍙戦€佸け璐? %v", err)
			a.recordTelegramSendHistory(sendMode, 6, "liquidations-structure-image", "failed", msg)
			errs = append(errs, msg)
		} else {
			a.recordTelegramSendHistory(sendMode, 6, "liquidations-structure-image", "success", "")
		}
		if err := a.sendTelegramText(a.buildLiquidationPatternQuestionAttachment()); err != nil {
			msg := fmt.Sprintf("閸欐垿鈧礁銇戠拹? %v", err)
			a.recordTelegramSendHistory(sendMode, 6, "liquidations-pattern-text", "failed", msg)
			errs = append(errs, msg)
		} else {
			a.recordTelegramSendHistory(sendMode, 6, "liquidations-pattern-text", "success", "")
		}
	}

	if settings.Group7Enabled {
		bubblesImage, err := a.captureBubblesScreenshotJPEG()
		if err != nil {
			msg := fmt.Sprintf("閸欘亜娴樻径杈Е: %v", err)
			a.recordTelegramSendHistory(sendMode, 7, "bubbles-5m-24h-image", "failed", msg)
			errs = append(errs, msg)
		} else if err := a.sendTelegramPhoto("", bubblesImage); err != nil {
			msg := fmt.Sprintf("閸欐垿鈧礁銇戠拹? %v", err)
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
		rows.WriteString(fmt.Sprintf(`<tr class="%s"><td>%d閻愮懓鍞?/td><td>%.1f</td><td>%.2f</td><td>%.1f</td><td>%.2f</td><td class="diff-cell %s">%.2f</td></tr>`,
			cls, b.Band, b.DownPrice, b.DownNotionalUSD/1e8, b.UpPrice, b.UpNotionalUSD/1e8, diffClass, b.DiffUSD/1e8))
	}
	longestDiffClass := "diff-flat"
	if r.LongPeak.SingleUSD > r.ShortPeak.SingleUSD {
		longestDiffClass = "diff-long"
	} else if r.ShortPeak.SingleUSD > r.LongPeak.SingleUSD {
		longestDiffClass = "diff-short"
	}
	rows.WriteString(fmt.Sprintf(`<tr class="longest"><td>鏈€闀挎煴</td><td>%.1f</td><td>%.2f</td><td>%.1f</td><td>%.2f</td><td class="diff-cell %s">%.2f</td></tr>`,
		r.LongPeak.Price, r.LongPeak.SingleUSD/1e8, r.ShortPeak.Price, r.ShortPeak.SingleUSD/1e8, longestDiffClass, math.Abs(r.LongPeak.SingleUSD-r.ShortPeak.SingleUSD)/1e8))
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
		rows.WriteString(fmt.Sprintf(`<tr class="%s"><td>%d鐐瑰唴</td><td>%.1f</td><td>%.2f</td><td>%.1f</td><td>%.2f</td><td class="diff-cell %s">%.2f</td></tr>`,
			cls, b.Band, b.DownPrice, b.DownNotionalUSD/1e8, b.UpPrice, b.UpNotionalUSD/1e8, diffClass, b.DiffUSD/1e8))
	}
	longestDiffClass := "diff-flat"
	if r.LongPeak.SingleUSD > r.ShortPeak.SingleUSD {
		longestDiffClass = "diff-long"
	} else if r.ShortPeak.SingleUSD > r.LongPeak.SingleUSD {
		longestDiffClass = "diff-short"
	}
	rows.WriteString(fmt.Sprintf(`<tr class="longest"><td>鏈€闀挎煴</td><td>%.1f</td><td>%.2f</td><td>%.1f</td><td>%.2f</td><td class="diff-cell %s">%.2f</td></tr>`,
		r.LongPeak.Price, r.LongPeak.SingleUSD/1e8, r.ShortPeak.Price, r.ShortPeak.SingleUSD/1e8, longestDiffClass, math.Abs(r.LongPeak.SingleUSD-r.ShortPeak.SingleUSD)/1e8))
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
	pageURL := fmt.Sprintf("http://127.0.0.1%s/monitor", defaultServerAddr)
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
		if(!titleText || titleText==='鍔犺浇涓?..' || titleText==='鍔犺浇澶辫触') return false;
		if(!broadcastText || broadcastText==='鍔犺浇涓?..' || broadcastText==='鍔犺浇澶辫触') return false;
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
		"<b>%d鐐瑰唴</b> %s(%s%s浜?",
		b.Band,
		telegramImbalanceLabel(b.UpNotionalUSD, b.DownNotionalUSD),
		sign,
		formatYi2(math.Abs(b.DownNotionalUSD-b.UpNotionalUSD)),
	)
}

func (a *App) buildTelegramThirtyDayText(monitor HeatReportData) string {
	b20 := heatReportBandBySize(monitor, 20)
	b50 := heatReportBandBySize(monitor, 50)
	b80 := heatReportBandBySize(monitor, 80)
	b100 := heatReportBandBySize(monitor, 100)
	bias := func(up, down float64) string {
		switch {
		case down > up:
			return "娑撳鏌熼崑蹇擃樋"
		case up > down:
			return "娑撳﹥鏌熼崑蹇擃樋"
		default:
			return "閸╃儤婀伴崸鍥€€"
		}
	}
	percent := func(v, total float64) int {
		if total <= 0 {
			return 0
		}
		return int(math.Round(v / total * 100))
	}
	bandLine := func(b HeatReportBand) string {
		total := b.UpNotionalUSD + b.DownNotionalUSD
		return fmt.Sprintf("%dpt upper $%s yi (%d%%) / lower $%s yi (%d%%) | %s",
			b.Band,
			formatYi2(b.UpNotionalUSD),
			percent(b.UpNotionalUSD, total),
			formatYi2(b.DownNotionalUSD),
			percent(b.DownNotionalUSD, total),
			bias(b.UpNotionalUSD, b.DownNotionalUSD),
		)
	}
	lines := []string{
		fmt.Sprintf("ETH heat report | price $%.1f", monitor.CurrentPrice),
		fmt.Sprintf("Long total %s yi | Short total %s yi", formatYi1(monitor.LongTotalUSD), formatYi1(monitor.ShortTotalUSD)),
		"--------------------",
		"<b>[Longest Long Column]</b>",
		fmt.Sprintf("Price $%.1f | Distance %.0f pt", monitor.LongPeak.Price, monitor.LongPeak.Distance),
		fmt.Sprintf("Single %s yi | Cumulative %s yi", formatYi1(monitor.LongPeak.SingleUSD), formatYi1(monitor.LongPeak.CumulativeUSD)),
		"<b>[Longest Short Column]</b>",
		fmt.Sprintf("Price $%.1f | Distance %.0f pt", monitor.ShortPeak.Price, monitor.ShortPeak.Distance),
		fmt.Sprintf("Single %s yi | Cumulative %s yi", formatYi1(monitor.ShortPeak.SingleUSD), formatYi1(monitor.ShortPeak.CumulativeUSD)),
		"<b>[Imbalance Summary]</b>",
		bandLine(b20),
		bandLine(b50),
		bandLine(b80),
		bandLine(b100),
	}
	return strings.Join(lines, "\n")
}

func (a *App) buildTelegramThirtyDayTextV2(monitor HeatReportData, monitorBands []HeatReportBand, webMap WebDataSourceMapResponse) string {
	monitor = a.alignTelegramTextHeatReport(monitor, webMap)
	bandBySize := func(band int) HeatReportBand {
		if len(monitorBands) > 0 {
			return heatBandBySize(monitorBands, band)
		}
		return heatReportBandBySize(monitor, band)
	}
	b20 := bandBySize(20)
	b50 := bandBySize(50)
	b80 := bandBySize(80)
	b100 := bandBySize(100)
	b200 := bandBySize(200)
	b300 := bandBySize(300)

	imbalanceLine := func(b HeatReportBand) string {
		side := "鍩烘湰鍧囪　"
		amount := math.Abs(b.DiffUSD)
		switch {
		case b.DownNotionalUSD > b.UpNotionalUSD:
			side = "澶氬崟鍋忓"
		case b.UpNotionalUSD > b.DownNotionalUSD:
			side = "绌哄崟鍋忓"
		}
		return fmt.Sprintf("%d鐐瑰唴 %s(%s浜?", b.Band, side, formatYi2(amount))
	}

	lines := []string{
		fmt.Sprintf("<b>ETH 闆峰尯閫熸姤 | 鐜颁环$%.1f</b>", monitor.CurrentPrice),
		fmt.Sprintf("鎬诲鍗?b>%s浜?/b> | 鎬荤┖鍗?b>%s浜?/b>", formatYi1(monitor.LongTotalUSD), formatYi1(monitor.ShortTotalUSD)),
		"",
		"<b>澶氬崟鏈€闀挎煴</b>",
		fmt.Sprintf("浠锋牸$%.1f | 璺濈幇浠?b>%.0f鐐?/b>", monitor.LongPeak.Price, monitor.LongPeak.Distance),
		fmt.Sprintf("鍗曟煴%s浜?| 绱<b>%s浜?/b>", formatYi1(monitor.LongPeak.SingleUSD), formatYi1(monitor.LongPeak.CumulativeUSD)),
		"",
		"<b>绌哄崟鏈€闀挎煴</b>",
		fmt.Sprintf("浠锋牸 $%.1f | 璺濈幇浠?b>%.0f鐐?/b>", monitor.ShortPeak.Price, monitor.ShortPeak.Distance),
		fmt.Sprintf("鍗曟煴%s浜?| 绱<b>%s浜?/b>", formatYi1(monitor.ShortPeak.SingleUSD), formatYi1(monitor.ShortPeak.CumulativeUSD)),
		"",
		"<b>澶氱┖澶辫　姒傝</b>",
		strings.Replace(imbalanceLine(b20), "20点内", "<b>20点</b>内", 1),
		strings.Replace(imbalanceLine(b50), "50点内", "<b>50点</b>内", 1),
		strings.Replace(imbalanceLine(b80), "80点内", "<b>80点</b>内", 1),
		strings.Replace(imbalanceLine(b100), "100点内", "<b>100点</b>内", 1),
		strings.Replace(imbalanceLine(b200), "200点内", "<b>200点</b>内", 1),
		strings.Replace(imbalanceLine(b300), "300点内", "<b>300点</b>内", 1),
	}
	return strings.Join(lines, "\n")
}

func (a *App) buildTelegramThirtyDayTextV3(monitor HeatReportData, monitorBands []HeatReportBand, webMap WebDataSourceMapResponse) string {
	monitor = a.alignTelegramTextHeatReport(monitor, webMap)
	bandBySize := func(band int) HeatReportBand {
		if len(monitorBands) > 0 {
			return heatBandBySize(monitorBands, band)
		}
		return heatReportBandBySize(monitor, band)
	}
	b20 := bandBySize(20)
	b50 := bandBySize(50)
	b80 := bandBySize(80)
	b100 := bandBySize(100)
	b200 := bandBySize(200)
	b300 := bandBySize(300)
	longestVerdict := telegramPeakVerdict(monitor.LongPeak, monitor.ShortPeak)

	lines := []string{
		fmt.Sprintf("<b>ETH 闆峰尯閫熸姤 | 鐜颁环$%s</b>", formatPrice1(monitor.CurrentPrice)),
		fmt.Sprintf("涓嬫柟澶氬崟鎬婚噺<b>%s浜?/b> | 涓婃柟绌哄崟鎬婚噺<b>%s浜?/b>", formatYi1(monitor.LongTotalUSD), formatYi1(monitor.ShortTotalUSD)),
		"",
		"<b>涓嬫柟澶氬崟鏈€闀挎煴</b>",
		fmt.Sprintf("浠锋牸$%s | 璺濈幇浠?b>%s鐐?/b>", formatPrice1(monitor.LongPeak.Price), formatPointDistance(monitor.LongPeak.Distance)),
		fmt.Sprintf("鍗曟煴%s浜?| 浠庣幇浠峰埌璇ユ煴绱<b>%s浜?/b>", formatYi1(monitor.LongPeak.SingleUSD), formatYi1(monitor.LongPeak.CumulativeUSD)),
		"",
		"<b>涓婃柟绌哄崟鏈€闀挎煴</b>",
		fmt.Sprintf("浠锋牸$%s | 璺濈幇浠?b>%s鐐?/b>", formatPrice1(monitor.ShortPeak.Price), formatPointDistance(monitor.ShortPeak.Distance)),
		fmt.Sprintf("鍗曟煴%s浜?| 浠庣幇浠峰埌璇ユ煴绱<b>%s浜?/b>", formatYi1(monitor.ShortPeak.SingleUSD), formatYi1(monitor.ShortPeak.CumulativeUSD)),
		fmt.Sprintf("鏈€闀挎煴宸€?b>%s浜?/b> | %s", formatYi2(math.Abs(monitor.LongPeak.SingleUSD-monitor.ShortPeak.SingleUSD)), longestVerdict),
		"",
		"<b>澶氱┖澶辫　瑙ｈ</b>",
		buildTelegramImbalanceLine(b20),
		buildTelegramImbalanceLine(b50),
		buildTelegramImbalanceLine(b80),
		buildTelegramImbalanceLine(b100),
		buildTelegramImbalanceLine(b200),
		buildTelegramImbalanceLine(b300),
	}
	return strings.Join(lines, "\n")
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
		Source:     "?????(Coinglass)",
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
		Source:     "????",
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
				side := fmt.Sprint(row["S"])
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

const analysisHTMLFallback = `<!doctype html><html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>鏃ュ唴鍒嗘瀽</title></head><body style="font-family:Segoe UI,Microsoft YaHei,sans-serif;padding:24px"><h2>鏃ュ唴鍒嗘瀽椤甸潰鏂囦欢缂哄け</h2><p>璇风‘璁?<code>internal/shared/pages/files/analysis_page_fixed.html</code> 瀛樺湪锛岀劧鍚庡埛鏂伴〉闈€?/p></body></html>`

const indexHTML = `<!doctype html>
<html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>ETH Liquidation Map</title>
<style>
:root{--bg:#e8edf4;--nav:#0b1220;--nav-border:#243145;--panel:#ffffff;--line:#dfe6ee;--left:#c2cad7;--left-head:#b7c0cf;--right:#efe9cf;--right-head:#e7dfbe;--text:#3b4c63;--muted:#67778d;--accent:#d3873e;--ctl-bg:#fff;--ctl-border:#b6c4d5}
[data-theme="dark"]{--bg:#000;--nav:#000;--nav-border:#111827;--panel:#000;--line:#1f2937;--left:#0f172a;--left-head:#0b1220;--right:#111827;--right-head:#0b1220;--text:#e5e7eb;--muted:#94a3b8;--accent:#fbbf24;--ctl-bg:#000;--ctl-border:#334155}
body{margin:0;background:var(--bg);color:var(--text);font-family:Inter,system-ui,Segoe UI,Arial,sans-serif}
.nav{height:56px;background:var(--nav);border-bottom:1px solid var(--nav-border);display:flex;align-items:center;justify-content:space-between;padding:0 20px;position:sticky;top:0;z-index:10}
.nav-left,.nav-right{display:flex;align-items:center;gap:20px}.brand{font-size:18px;font-weight:700;color:#eef3f9}
.menu a{color:#d6deea;text-decoration:none;font-size:16px;margin-right:18px}.menu a.active{color:#fff;font-weight:700}
.upgrade{color:#fff;font-weight:700;text-decoration:none}.wrap{max-width:none;width:100%;margin:0 auto;padding:16px}
.theme-toggle{display:inline-flex;align-items:center;gap:6px;font-size:13px}.theme-toggle button{height:30px;padding:0 10px;border-radius:999px;border:1px solid rgba(148,163,184,0.45);background:transparent;color:#e5e7eb;cursor:pointer}.theme-toggle button.label{cursor:default;opacity:.92}.theme-toggle button.active{background:rgba(255,255,255,0.12);border-color:rgba(255,255,255,0.18);color:#fff}
.top{display:flex;justify-content:space-between;align-items:center;gap:12px;flex-wrap:wrap}
.panel{border:1px solid var(--line);background:var(--panel);margin:14px 0;padding:16px;border-radius:10px;box-shadow:0 1px 2px rgba(15,23,42,.03)}
#status{background:var(--nav);color:#eef3f9;padding:10px 12px;border-radius:8px;font-weight:700;text-align:center}
.btns button{margin-right:8px;background:var(--ctl-bg);color:var(--text);border:1px solid var(--ctl-border);padding:8px 14px;border-radius:8px;cursor:pointer}
.btns button.active{background:var(--nav);color:#fff;border-color:var(--nav)} table{width:100%;border-collapse:collapse}
th,td{border-bottom:1px solid var(--line);padding:8px 10px;text-align:center}
.grid{display:grid;grid-template-columns:minmax(0,1fr);gap:14px}.hint{color:var(--muted);font-size:12px}.mono{font-family:ui-monospace,SFMono-Regular,Menlo,Consolas,monospace}
.table-wrap{width:100%;overflow-x:auto}
.table-wrap table{min-width:640px}
.heat-table thead tr:first-child th{background:#d5dce6;font-size:18px}
.heat-table .col-threshold{background:#d8d9dc}
.heat-table .col-up-price,.heat-table .col-up-size{background:var(--left)}
.heat-table .col-down-price,.heat-table .col-down-size{background:var(--right)}
.heat-table thead .col-up-price,.heat-table thead .col-up-size{background:var(--left-head)}
.heat-table thead .col-down-price,.heat-table thead .col-down-size{background:var(--right-head)}
.heat-table .col-up-size,.heat-table .col-down-size{color:var(--accent);font-weight:700}
.panel h3,.top h2,.top .hint{text-align:center}
.heatmap-wrap{border:1px solid var(--line);border-radius:10px;background:var(--panel);padding:10px}
#liqHeatMap{width:100%;height:320px;display:block;border:1px solid var(--line);border-radius:8px;background:var(--panel)}
.heatmap-wrap{position:relative}
.liq-tip{position:absolute;display:none;min-width:180px;max-width:280px;background:rgba(15,23,42,.96);color:#e2e8f0;border:1px solid rgba(148,163,184,.25);border-radius:10px;padding:10px 12px;font-size:12px;line-height:1.5;box-shadow:0 10px 28px rgba(2,6,23,.35);pointer-events:none}
.liq-tip .t{font-weight:800;margin-bottom:6px}
.liq-tip .row{display:flex;justify-content:space-between;gap:10px}
.liq-dot{display:inline-block;width:8px;height:8px;border-radius:2px;margin-right:6px;vertical-align:-1px}
.liq-dot.binance{background:rgba(249,115,22,.9)}.liq-dot.okx{background:rgba(234,179,8,.9)}.liq-dot.bybit{background:rgba(103,232,249,.9)}
.desc{font-size:12px;color:var(--muted);line-height:1.5;margin-top:8px}
.upgrade-modal{position:fixed;inset:0;background:rgba(2,6,23,.55);display:none;align-items:center;justify-content:center;z-index:9999}.upgrade-modal.show{display:flex}.upgrade-card{width:min(880px,92vw);max-height:82vh;background:#0b1220;color:#e2e8f0;border:1px solid #334155;border-radius:10px;box-shadow:0 10px 30px rgba(2,6,23,.45);overflow:hidden}.upgrade-head{display:flex;align-items:center;justify-content:space-between;padding:10px 12px;border-bottom:1px solid #334155}.upgrade-title{font-size:14px;font-weight:700}.upgrade-close{background:transparent;border:1px solid #475569;color:#e2e8f0;border-radius:6px;padding:4px 8px;cursor:pointer}.upgrade-log{margin:0;padding:12px;white-space:pre-wrap;overflow:auto;max-height:62vh;font-family:ui-monospace,SFMono-Regular,Menlo,Consolas,monospace;font-size:12px;line-height:1.45}.upgrade-foot{padding:8px 12px;border-top:1px solid #334155;font-size:12px;color:#94a3b8}.footer{margin:18px auto 0 auto;max-width:1200px;padding:10px 12px;font-size:12px;color:#64748b;text-align:center}</style></head><body>
<div class="nav"><div class="nav-left"><div class="brand">ETH Liquidation Map</div><div class="menu"><a href="/" class="active">娓呯畻鐑尯</a><a href="/config">妯″瀷閰嶇疆</a><a href="/monitor">闆峰尯鐩戞帶</a><a href="/map">鐩樺彛姹囨€?/a><a href="/liquidations">寮哄钩娓呯畻</a><a href="/bubbles">姘旀场鍥?/a><a href="/webdatasource">椤甸潰鏁版嵁婧?/a><a href="/channel">娑堟伅閫氶亾</a><a href="/analysis">鏃ュ唴鍒嗘瀽</a></div></div><div class="nav-right"><div class="theme-toggle"><button class="label" type="button">涓婚</button><button id="themeDark" onclick="setTheme('dark')">娣辫壊</button><button id="themeLight" onclick="setTheme('light')">娴呰壊</button></div><a href="#" class="upgrade" onclick="return doUpgrade(event)">&#21319;&#32423;</a></div></div>
<div class="wrap"><div class="panel top"><div><h2 style="margin:0 0 6px 0">ETH &#28165;&#31639;&#28909;&#21306;</h2><div class="hint">&#25353; <span class="mono">0</span> / <span class="mono">1</span> / <span class="mono">7</span> / <span class="mono">3</span> &#20999;&#25442; &#26085;&#20869; / 1&#22825; / 7&#22825; / 30&#22825;</div></div><div class="btns"><button data-days="0">&#26085;&#20869;</button><button data-days="1">1&#22825;</button><button data-days="7">7&#22825;</button><button data-days="30">30&#22825;</button></div></div>
<div class="panel"><div id="status">loading...</div></div>
<div class="grid"><div class="panel"><h3>ETH 娓呯畻鍦板浘锛圤I 澧為噺妯″瀷锛?/h3><div class="heatmap-wrap"><canvas id="liqHeatMap" width="1400" height="320"></canvas><div id="liqTip" class="liq-tip"></div><div class="hint">婊氳疆缂╂斁锛孲hift+婊氳疆宸﹀彸骞崇Щ锛屾寜浣忛紶鏍囧乏閿乏鍙虫嫋鍔ㄣ€俋杞达細浠锋牸 | 宸﹁酱锛氬崟浠蜂綅娓呯畻閲戦 | 鍙宠酱锛氫粠褰撳墠浠风疮璁℃竻绠楅噾棰?/div><div id="liqDesc" class="desc"></div></div></div></div></div>
<script>
let currentDays=1;
let liqMapData=null;
let modelConfig=null;
let liqHoverMeta=null;
let liqViewMin=null, liqViewMax=null, liqDrag=false, liqLastX=0;
const exOrder=['binance','okx','bybit'];
const exColor={binance:'rgba(249,115,22,0.78)',okx:'rgba(234,179,8,0.76)',bybit:'rgba(103,232,249,0.76)'};
function setTheme(t){const theme=(t==='dark')?'dark':'light';document.documentElement.setAttribute('data-theme',theme);try{localStorage.setItem('theme',theme);}catch(_){}const bd=document.getElementById('themeDark'),bl=document.getElementById('themeLight');if(bd)bd.classList.toggle('active',theme==='dark');if(bl)bl.classList.toggle('active',theme==='light');}
function initTheme(){let t='light';try{t=localStorage.getItem('theme')||'light';}catch(_){}setTheme(t);}
function windowLabel(v){return Number(v)===0?'\u65e5\u5185':(Number(v)+'\u5929')}
async function setWindow(days){currentDays=days;await fetch('/api/window',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({days})});renderActive();load();}
function renderActive(){document.querySelectorAll('button[data-days]').forEach(b=>b.classList.toggle('active',Number(b.dataset.days)===currentDays));}
function fmtPrice(n){return Number(n).toLocaleString('zh-CN',{minimumFractionDigits:1,maximumFractionDigits:1})}
function fmtAmount(n){n=Number(n);if(!isFinite(n))return '-';const a=Math.abs(n);if(a>=1e8)return (n/1e8).toFixed(2)+'\u4ebf';if(a>=1e6)return (n/1e6).toFixed(2)+'\u767e\u4e07';return (n/1e4).toFixed(2)+'\u4e07';}
function fmt4(n){return Number(n).toFixed(8)}
function fitCanvas(id){const c=document.getElementById(id);if(!c)return null;const dpr=window.devicePixelRatio||1;const rect=c.getBoundingClientRect();const W=Math.max(320,Math.floor(rect.width));const H=Math.max(220,Math.floor(rect.height));const rw=Math.floor(W*dpr),rh=Math.floor(H*dpr);if(c.width!==rw||c.height!==rh){c.width=rw;c.height=rh;}const x=c.getContext('2d');x.setTransform(dpr,0,0,dpr,0,0);return {c,x,W,H};}
function hideLiqTip(){const tip=document.getElementById('liqTip');if(tip)tip.style.display='none';}
function showLiqTip(meta,ev){const tip=document.getElementById('liqTip');if(!tip||!meta||!meta.pts||!meta.pts.length)return;const rect=meta.c.getBoundingClientRect();const mx=ev.clientX-rect.left;let best=null,bd=1e18;for(const it of meta.pts){const d=Math.abs(mx-it.px);if(d<bd){bd=d;best=it;}}const hitW=Math.max(meta.hitW||0,meta.barW||0);if(!best||bd>(hitW*0.5)){hideLiqTip();return;}const hasCum=Number(best.cumTotal||0)>0;const lines=[];lines.push('<div class=\"t\">浠锋牸 '+fmtPrice(best.p)+'</div>');lines.push('<div class=\"row\"><span>鍗曚环浣嶆竻绠楅</span><span>'+fmtAmount(best.total)+'</span></div>');if(hasCum)lines.push('<div class=\"row\"><span>浠庡綋鍓嶄环绱</span><span>'+fmtAmount(best.cumTotal)+'</span></div>');for(const ex of exOrder){const exact=Number((best.ex||{})[ex]||0);const cum=Number((best.cumEx||{})[ex]||0);const v=hasCum?cum:exact;if(!(v>0))continue;lines.push('<div class=\"row\"><span><span class=\"liq-dot '+ex+'\"></span>'+ex.toUpperCase()+(hasCum?' 绱':'')+'</span><span>'+fmtAmount(v)+'</span></div>');}tip.innerHTML=lines.join('');tip.style.display='block';const wrap=document.querySelector('.heatmap-wrap');const wr=wrap?wrap.getBoundingClientRect():rect;let left=(ev.clientX-wr.left)+14,top=(ev.clientY-wr.top)+14;tip.style.left='0px';tip.style.top='0px';const tw=tip.offsetWidth||220,th=tip.offsetHeight||90;if(left+tw>wr.width-10)left=Math.max(8,wr.width-10-tw);if(top+th>wr.height-10)top=Math.max(8,wr.height-10-th);tip.style.left=left+'px';tip.style.top=top+'px';}
function clamp(v,a,b){return Math.max(a,Math.min(b,v));}
function bindLiqHover(){
  const c=document.getElementById('liqHeatMap');
  if(!c||c.dataset.bound==='1')return;
  c.dataset.bound='1';
  c.addEventListener('mousemove',e=>{
    if(liqDrag&&liqHoverMeta&&liqViewMin!=null&&liqViewMax!=null){
      const rect=c.getBoundingClientRect();
      const dx=e.clientX-liqLastX;
      liqLastX=e.clientX;
      const s=liqHoverMeta;
      const pw=s.pw||Math.max(1,rect.width-86);
      const span=(liqViewMax-liqViewMin);
      const dp=-(dx/pw)*span;
      liqViewMin+=dp;
      liqViewMax+=dp;
      const baseMin=s.baseMin,baseMax=s.baseMax;
      const w=liqViewMax-liqViewMin;
      if(liqViewMin<baseMin){liqViewMin=baseMin;liqViewMax=baseMin+w;}
      if(liqViewMax>baseMax){liqViewMax=baseMax;liqViewMin=baseMax-w;}
      drawLiqHeat();
      return;
    }
    if(!liqHoverMeta)return;
    showLiqTip(liqHoverMeta,e);
  });
  c.addEventListener('mouseleave',()=>{liqDrag=false;hideLiqTip();});
  c.addEventListener('mousedown',e=>{if(e.button!==0)return;liqDrag=true;liqLastX=e.clientX;});
  window.addEventListener('mouseup',()=>{liqDrag=false;});

  c.addEventListener('wheel',e=>{
    if(!liqHoverMeta)return;
    e.preventDefault();
    const s=liqHoverMeta;
    const rect=c.getBoundingClientRect();
    const mx=e.clientX-rect.left;
    const pw=s.pw||Math.max(1,rect.width-86);
    const ratio=clamp((mx-(s.padL||66))/pw,0,1);
    const baseMin=s.baseMin,baseMax=s.baseMax;
    const curMin=(liqViewMin==null?baseMin:liqViewMin);
    const curMax=(liqViewMax==null?baseMax:liqViewMax);
    let span=curMax-curMin;
    if(!(span>0))span=baseMax-baseMin;
    const focus=curMin+span*ratio;

    const isPan=Math.abs(e.deltaX)>Math.abs(e.deltaY)||e.shiftKey;
    if(isPan){
      const d=(Math.abs(e.deltaX)>0?e.deltaX:e.deltaY);
      const pan=-(d/pw)*span;
      let vMin=curMin+pan;
      let vMax=curMax+pan;
      if(vMin<baseMin){vMin=baseMin;vMax=baseMin+span;}
      if(vMax>baseMax){vMax=baseMax;vMin=baseMax-span;}
      liqViewMin=vMin;
      liqViewMax=vMax;
    }else{
      const factor=(e.deltaY<0?0.88:1.14);
      const full=(baseMax-baseMin);
      const nextSpan=clamp(span*factor,full*0.08,full);
      let vMin=focus-nextSpan*ratio;
      let vMax=vMin+nextSpan;
      if(vMin<baseMin){vMin=baseMin;vMax=baseMin+nextSpan;}
      if(vMax>baseMax){vMax=baseMax;vMin=baseMax-nextSpan;}
      liqViewMin=vMin;
      liqViewMax=vMax;
    }
    drawLiqHeat();
  },{passive:false});
}
function heatColor(v,max){if(!(max>0)||!(v>0))return 'rgb(248,250,252)';let t=Math.max(0,Math.min(1,v/max));t=Math.pow(t,0.55);let r,g,b;if(t<0.5){const k=t/0.5;r=Math.round(15+(46-15)*k);g=Math.round(23+(163-23)*k);b=Math.round(42+(242-42)*k);}else{const k=(t-0.5)/0.5;r=Math.round(46+(250-46)*k);g=Math.round(163+(204-163)*k);b=Math.round(242+(21-242)*k);}return 'rgb('+r+','+g+','+b+')';}
function drawLiqHeat(){
  const v=fitCanvas('liqHeatMap');
  if(!v) return;
  const {c,x,W,H}=v;
  x.clearRect(0,0,W,H);
  x.fillStyle='#fff';
  x.fillRect(0,0,W,H);

  const d=liqMapData;
  if(!d||!d.prices||!d.intensity_grid||!d.prices.length){
    hideLiqTip();liqHoverMeta=null;
    x.fillStyle='#64748b';x.font='13px sans-serif';
    x.fillText('鏆傛棤娓呯畻鍦板浘鏁版嵁',16,24);
    return;
  }

  const rows=d.prices.length;
  const cols=((d.intensity_grid&&d.intensity_grid[0])?d.intensity_grid[0].length:0);
  if(rows<2||cols<1){
    hideLiqTip();liqHoverMeta=null;
    x.fillStyle='#64748b';x.font='13px sans-serif';
    x.fillText('鏆傛棤娓呯畻鍦板浘鏁版嵁',16,24);
    return;
  }

  const per=(d.per_exchange)||{};
  const all=[];
  for(let ri=0;ri<rows;ri++){
    const p=Number(d.prices[ri]||0);
    if(!(p>0)) continue;
    const exVals={};
    let total=0;
    for(const ex of exOrder){
      const arr=per[ex]||per[ex.toUpperCase()]||null;
      const vv=Number((arr&&arr[ri])||0);
      if(vv>0){exVals[ex]=vv;total+=vv;}
    }
    if(!(total>0)){
      let s=0;
      for(let ci=0;ci<cols;ci++) s+=Math.max(0,Number((d.intensity_grid[ri]||[])[ci]||0));
      if(s>0){total=s;exVals.unknown=s;}
    }
    if(total>0) all.push({ri:ri,p:p,total:total,ex:exVals});
  }
  if(!all.length){
    hideLiqTip();liqHoverMeta=null;
    x.fillStyle='#64748b';x.font='13px sans-serif';
    x.fillText('鏆傛棤娓呯畻鍦板浘鏁版嵁',16,24);
    return;
  }

  const cp=Number(d.current_price||0);
  const cumShortAll=[];
  const cumLongAll=[];
  if(cp>0){
    let runS=0;
    const runSEx={binance:0,okx:0,bybit:0};
    for(const it of all){
      if(it.p<cp) continue;
      runS+=it.total;
      const exTotals={};
      for(const ex of exOrder){
        runSEx[ex]+=Number((it.ex||{})[ex]||0);
        if(runSEx[ex]>0) exTotals[ex]=runSEx[ex];
      }
      it.cumShortTotal=runS;
      it.cumShortEx=exTotals;
      cumShortAll.push({p:it.p,v:runS});
    }
    let runL=0;
    const runLEx={binance:0,okx:0,bybit:0};
    for(let i=all.length-1;i>=0;i--){
      const it=all[i];
      if(it.p>cp) continue;
      runL+=it.total;
      const exTotals={};
      for(const ex of exOrder){
        runLEx[ex]+=Number((it.ex||{})[ex]||0);
        if(runLEx[ex]>0) exTotals[ex]=runLEx[ex];
      }
      it.cumLongTotal=runL;
      it.cumLongEx=exTotals;
      cumLongAll.push({p:it.p,v:runL});
    }
    cumLongAll.reverse();
  }

  const baseMin=all[0].p,baseMax=all[all.length-1].p;
  const padL=66,padR=60,padT=16,padB=42;
  const pw=W-padL-padR,ph=H-padT-padB,by=padT+ph;
  const fullSpan=Math.max(1e-6,baseMax-baseMin);
  if(liqViewMin==null||liqViewMax==null||liqViewMin>=liqViewMax||liqViewMin<baseMin||liqViewMax>baseMax){
    liqViewMin=baseMin;liqViewMax=baseMax;
  }
  const minP=liqViewMin,maxP=liqViewMax,span=Math.max(1e-6,maxP-minP);
  const pts=all.filter(it=>it.p>=minP&&it.p<=maxP);
  const maxV=Math.max(1,...pts.map(it=>it.total));
  const sx=v=>padL+((v-minP)/span)*pw;
  const sy=v=>by-(v/maxV)*ph;

  x.strokeStyle='#e2e8f0';
  x.strokeRect(padL,padT,pw,ph);
  x.font='12px sans-serif';
  for(let i=0;i<=4;i++){
    const y=padT+ph*(i/4),val=maxV*(1-i/4);
    x.strokeStyle='#e5e7eb';
    x.beginPath();x.moveTo(padL,y);x.lineTo(W-padR,y);x.stroke();
    x.fillStyle='#475569';
    x.fillText(fmtAmount(val),6,y+4);
  }

  const minLabelGap=12;
  const approxLabelW=Math.max(36,x.measureText(fmtPrice(minP)).width,x.measureText(fmtPrice(maxP)).width);
  const maxTicks=Math.max(2,Math.floor(pw/(approxLabelW+minLabelGap)));
  const tickCount=Math.max(2,Math.min(10,maxTicks));
  for(let i=0;i<=tickCount;i++){
    const p=minP+span*(i/tickCount),px=sx(p);
    x.strokeStyle='#e5e7eb';
    x.beginPath();x.moveTo(px,by);x.lineTo(px,by+4);x.stroke();
    const label=fmtPrice(p);
    const lw=x.measureText(label).width;
    const tx=Math.max(padL,Math.min(px-lw/2,W-padR-lw));
    x.fillStyle='#64748b';
    x.fillText(label,tx,by+18);
  }

  const barW=Math.max(2,Math.min(12,pw/Math.max(40,pts.length)));

  for(const it of pts){
    const px=sx(it.p);
    let acc=0;
    for(const ex of exOrder){
      const vv=Number((it.ex||{})[ex]||0);
      if(!(vv>0)) continue;
      const y0=sy(acc),y1=sy(acc+vv);
      x.fillStyle=exColor[ex]||'rgba(148,163,184,0.6)';
      x.fillRect(px-barW/2,y1,barW,Math.max(1,y0-y1));
      acc+=vv;
    }
    if(acc<=0){
      const y=sy(it.total);
      x.fillStyle=(cp>0?(it.p<=cp):(it.p<=(minP+maxP)/2))?'rgba(249,115,22,0.78)':'rgba(125,211,252,0.78)';
      x.fillRect(px-barW/2,y,barW,by-y);
    }
  }

  // Cumulative curves (Coinglass-style): right axis.
  const cumShort=cumShortAll.filter(it=>it.p>=minP&&it.p<=maxP);
  const cumLong=cumLongAll.filter(it=>it.p>=minP&&it.p<=maxP);
  const maxCum=Math.max(0,...(cumShort.length?cumShort.map(it=>it.v):[0]),...(cumLong.length?cumLong.map(it=>it.v):[0]));
  if(maxCum>0){
    const scy=v=>by-(v/maxCum)*ph;
    x.fillStyle='#475569';
    x.fillText('绱',W-padR+8,padT-4);
    for(let i=0;i<=4;i++){
      const y=padT+ph*(i/4),val=maxCum*(1-i/4);
      x.fillStyle='#64748b';
      x.fillText(fmtAmount(val),W-padR+8,y+4);
    }
    const drawLine=(arr,color,fillColor)=>{
      if(!arr||arr.length<2) return;
      x.lineWidth=2;
      x.strokeStyle=color;
      x.beginPath();
      for(let i=0;i<arr.length;i++){
        const px=sx(arr[i].p);
        const py=scy(arr[i].v);
        if(i===0) x.moveTo(px,py); else x.lineTo(px,py);
      }
      x.stroke();
      if(fillColor){
        x.lineTo(sx(arr[arr.length-1].p),by);
        x.lineTo(sx(arr[0].p),by);
        x.closePath();
        x.fillStyle=fillColor;
        x.fill();
      }
    };
    drawLine(cumShort,'rgba(16,185,129,0.95)','rgba(16,185,129,0.10)');
    drawLine(cumLong,'rgba(239,68,68,0.95)','rgba(239,68,68,0.10)');
  }

  x.lineWidth=1;
  if(cp>=minP&&cp<=maxP){
    const cpX=sx(cp);
    x.strokeStyle='rgba(220,38,38,0.9)';
    x.setLineDash([5,4]);
    x.beginPath();x.moveTo(cpX,padT);x.lineTo(cpX,by);x.stroke();
    x.setLineDash([]);
    x.fillStyle='#111827';
    const txt='瑜版挸澧犳禒?'+fmtPrice(cp);
    const tw=x.measureText(txt).width;
    x.fillText(txt,Math.max(padL,Math.min(cpX+4,W-padR-tw)),padT+12);
  }
  x.fillStyle='#475569';
  x.fillText('娓呯畻閲戦',padL,padT-4);
  const xt='浠锋牸';
  const xtw=x.measureText(xt).width;
  x.fillText(xt,padL+pw/2-xtw/2,H-8);
  liqHoverMeta={c:c,pts:pts.map(it=>({p:it.p,total:it.total,ex:it.ex,cumTotal:(it.p>=cp?Number(it.cumShortTotal||0):Number(it.cumLongTotal||0)),cumEx:(it.p>=cp?(it.cumShortEx||{}):(it.cumLongEx||{})),px:sx(it.p)})),barW:barW,hitW:barW,pw:pw,padL:padL,baseMin:baseMin,baseMax:baseMax,fullSpan:fullSpan};
}
function renderLiqDesc(cfg,dash){
  if(!cfg){const el=document.getElementById('liqDesc');if(el)el.textContent='';return;}
  const levs=String(cfg.leverage_csv||cfg.LeverageCSV||cfg.leverage_levels||cfg.leverage||'10,25,50,100');
  const ws=String(cfg.weight_csv||cfg.WeightCSV||cfg.leverage_weights||cfg.weights||'0.25,0.25,0.25,0.25');
  const mm=Number(cfg.maint_margin||cfg.MaintMargin||cfg.mm||0.005);
  const mmCSV=String(cfg.maint_margin_csv||cfg.MaintMarginCSV||cfg.mm_csv||'').trim();
  const fs=Number(cfg.funding_scale||cfg.FundingScale||7000);
  const dk=Number(cfg.decay_k||cfg.DecayK||2.2);
  const ns=Number(cfg.neighbor_share||cfg.NeighborShare||0.28);
  const bucket=Number(cfg.bucket_min||cfg.BucketMin||5);
  const step=Number(cfg.price_step||cfg.PriceStep||5);
  const range=Number(cfg.price_range||cfg.PriceRange||400);
  const wd=Number((dash&&dash.window_days));
  const cutoff=Number((dash&&dash.window_cutoff));
  let windowText='1澶?;
  if(wd===0){
    windowText='鏃ュ唴锛?8:00 璧凤級';
  }else if(wd===7||wd===30||wd===1){
    windowText=wd+'澶?;
  }
  let minsText='';
  if(wd===0 && cutoff>0){
    const mins=Math.max(60,Math.ceil((Date.now()-cutoff)/60000));
    minsText='锛堢害 '+mins+' 鍒嗛挓锛?;
  }
  const el=document.getElementById('liqDesc');
  if(el){
    const mmText = mmCSV ? ('mm锛堟寜妗ｄ綅锛?'+mmCSV) : ('mm='+mm.toFixed(4));
    el.textContent='鏁版嵁婧愶細Binance / Bybit / OKX 鍏紑鎺ュ彛銆傛ā鍨嬶細鎸?OI 澧為噺銆佹潬鏉嗘潈閲嶃€佽祫閲戣垂鐜囦笌缁存姢淇濊瘉閲戜及绠楁竻绠楀己搴︺€傛潬鏉嗘。浣?'+levs+
      '锛屾潈閲?'+ws+'锛屽绌烘瘮渚嬩娇鐢?clamp(base + funding * '+fs+', 0.2, 0.8)锛屾竻绠椾环浣跨敤 mark*(1卤1/lev鈭搈m)锛?+mmText+'锛涙椂闂磋“鍑?exp(-'+dk.toFixed(2)+'*age)锛岄偦杩戞墿鏁?'+ns.toFixed(2)+
      '銆傚弬鏁帮細鍥炵湅 '+windowText+minsText+'锛屾椂闂存《 '+bucket+' 鍒嗛挓锛屼环鏍兼闀?'+step+'锛岃寖鍥?卤'+range+'銆傞厤缃叆鍙ｏ細/config銆?;
  }
}
function renderTable(rows,headers){if(!rows||!rows.length)return '<div class="hint">\u6682\u65e0\u6570\u636e</div>';let html='<table><thead><tr>'+headers.map(h=>'<th>'+h+'</th>').join('')+'</tr></thead><tbody>';for(const r of rows) html+='<tr>'+r.map(c=>'<td>'+c+'</td>').join('')+'</tr>';return html+'</tbody></table>';}
function buildHeatBandsFromModel(model){
  if(!model||!model.prices||!model.intensity_grid||!model.prices.length) return [];
  const rows=model.prices.length, cols=((model.intensity_grid&&model.intensity_grid[0])?model.intensity_grid[0].length:0);
  if(rows<1||cols<1) return [];
  const cp=Number(model.current_price||0);
  if(!(cp>0)) return [];
  const pts=[];
  for(let ri=0;ri<rows;ri++){
    const p=Number(model.prices[ri]||0);
    if(!(p>0)) continue;
    let s=0;
    for(let ci=0;ci<cols;ci++) s+=Math.max(0,Number((model.intensity_grid[ri]||[])[ci]||0));
    if(s>0) pts.push({price:p,notional:s});
  }
  if(!pts.length) return [];
  const showBands=[10,20,30,40,50,60,80,100,150];
  return showBands.map(band=>{
    let upPrice=0,upNotional=0,upMax=0,downPrice=0,downNotional=0,downMax=0;
    for(const pt of pts){
      const dist=pt.price-cp;
      if(dist>=0&&dist<=band){
        upNotional+=pt.notional;
        if(pt.notional>upMax){upMax=pt.notional;upPrice=pt.price;}
      }
      if(dist<=0&&Math.abs(dist)<=band){
        downNotional+=pt.notional;
        if(pt.notional>downMax){downMax=pt.notional;downPrice=pt.price;}
      }
    }
    return {band,up_price:upPrice,up_notional_usd:upNotional,down_price:downPrice,down_notional_usd:downNotional};
  });
}
function renderHeatReport(d){
  const bands = buildHeatBandsFromModel(liqMapData);
  if(!bands.length) return '<div class="hint">\u6682\u65e0\u6570\u636e</div>';
  let html = '<table class="heat-table"><thead>' +
    '<tr><th rowspan="2" class="col-threshold">\u70b9\u6570\u9608\u503c</th><th colspan="2">\u4e0b\u65b9\u591a\u5355</th><th colspan="2">\u4e0a\u65b9\u7a7a\u5355</th></tr>' +
    '<tr><th class="col-down-price">\u6e05\u7b97\u4ef7\u683c</th><th class="col-down-size">\u6e05\u7b97\u89c4\u6a21</th><th class="col-up-price">\u6e05\u7b97\u4ef7\u683c</th><th class="col-up-size">\u6e05\u7b97\u89c4\u6a21</th></tr>' +
    '</thead><tbody>';
  const toScale = n => {n=Number(n||0);const a=Math.abs(n);if(a>=1e8)return (n/1e8).toFixed(2)+'娴?;if(a>=1e4)return (n/1e4).toFixed(2)+'娑?;return n.toFixed(2);};
  for(const b of bands){
    html += '<tr>' +
      '<td class="col-threshold">'+b.band+'\u70b9\u5185</td>' +
      '<td class="col-down-price">'+(Number(b.down_price)>0?fmtPrice(b.down_price):'-')+'</td>' +
      '<td class="col-down-size">'+toScale(b.down_notional_usd)+'</td>' +
      '<td class="col-up-price">'+(Number(b.up_price)>0?fmtPrice(b.up_price):'-')+'</td>' +
      '<td class="col-up-size">'+toScale(b.up_notional_usd)+'</td>' +
      '</tr>';
  }
  html += '</tbody></table>';
  return html;
}
async function load(){
  const cfg=await fetch('/api/model-config').then(r=>r.json()).catch(()=>null);
  modelConfig=cfg;
  const bucket=Number(cfg&&cfg.BucketMin||cfg&&cfg.bucket_min||5);
  const step=Number(cfg&&cfg.PriceStep||cfg&&cfg.price_step||5);
  const range=Number(cfg&&cfg.PriceRange||cfg&&cfg.price_range||400);
  const d=await fetch('/api/dashboard').then(r=>r.json());
  currentDays=(d.window_days===0||d.window_days)?d.window_days:currentDays;
  renderActive();
  renderLiqDesc(cfg,d);
  const mapUrl='/api/model/liquidation-map?bucket_min='+bucket+'&price_step='+step+'&price_range='+range;
  liqMapData=await fetch(mapUrl).then(m=>m.json()).catch(()=>null);
  document.getElementById('status').textContent='\u5f53\u524d\u4ef7: '+fmtPrice(d.current_price)+' | \u5468\u671f: '+windowLabel(d.window_days)+' | \u66f4\u65b0\u65f6\u95f4: '+new Date(d.generated_at).toLocaleString();
  const marketEl=document.getElementById('market');
  if(marketEl){
    marketEl.innerHTML=renderTable((d.states||[]).map(s=>[s.exchange,fmtPrice(s.mark_price),s.oi_qty?fmtAmount(s.oi_qty*s.mark_price):'-',s.oi_value_usd?fmtAmount(s.oi_value_usd):'-',s.funding_rate==null?'-':fmt4(s.funding_rate)]),['\u4ea4\u6613\u6240','\u6807\u8bb0\u4ef7','OI\u6570\u91cf','OI\u4ef7\u503cUSD','Funding']);
  }
  const bandsEl=document.getElementById('bands');
  if(bandsEl){
    bandsEl.innerHTML=renderHeatReport(d);
  }
  drawLiqHeat();
}
async function openUpgradeModal(){const m=document.getElementById('upgradeModal'),logEl=document.getElementById('upgradeLog'),foot=document.getElementById('upgradeFoot');if(!m||!logEl||!foot)return;m.classList.add('show');logEl.textContent='';foot.textContent='姝ｅ湪瑙﹀彂鍗囩骇...';const r=await fetch('/api/upgrade/pull',{method:'POST'});const d=await r.json().catch(()=>({error:'response parse failed',output:''}));if(d.error){logEl.textContent=String(d.output||'');foot.textContent='瑙﹀彂澶辫触: '+d.error;return;}foot.textContent='宸茶Е鍙戯紝姝ｅ湪鎵ц...';let stable=0;for(let i=0;i<180;i++){await new Promise(res=>setTimeout(res,1000));const pr=await fetch('/api/upgrade/progress').then(x=>x.json()).catch(()=>null);if(!pr)continue;logEl.textContent=String(pr.log||'');logEl.scrollTop=logEl.scrollHeight;if(pr.done){foot.textContent=(String(pr.exit_code||'')==='0')?'鍗囩骇瀹屾垚骞跺凡閲嶅惎':'閸楀洨楠囩€瑰本鍨氶敍宀勨偓鈧崙铏圭垳 '+String(pr.exit_code||'?');return;}if(!pr.running)stable++;else stable=0;if(stable>=3){foot.textContent='閸楀洨楠囨潻娑氣柤瀹歌尙绮ㄩ弶鐕傜礄閻樿埖鈧焦婀惌銉礆閿涘矁顕Λ鈧弻銉︽）韫?;return;}}foot.textContent='閸楀洨楠囨禒宥呮躬鏉╂稖顢戦敍宀冾嚞缁嬪秴鎮楅崘宥囨箙';}
function closeUpgradeModal(){const m=document.getElementById('upgradeModal');if(m)m.classList.remove('show');}
async function doUpgrade(event){if(event)event.preventDefault();openUpgradeModal();return false;}
initTheme();document.querySelectorAll('button[data-days]').forEach(b=>b.onclick=()=>setWindow(Number(b.dataset.days)));document.addEventListener('keydown',e=>{if(e.key==='0')setWindow(0);if(e.key==='1')setWindow(1);if(e.key==='7')setWindow(7);if(e.key==='3')setWindow(30);});bindLiqHover();window.addEventListener('resize',()=>drawLiqHeat());setInterval(load,5000);load();(async()=>{try{const r=await fetch('/api/version');const v=await r.json();const el=document.getElementById('globalFooter');if(el)el.textContent='Code by Yuhao@jiansutech.com - '+(v.commit_time||'-')+' - '+(v.commit_id||'-')+' - '+(v.branch||'-');}catch(_){const el=document.getElementById('globalFooter');if(el)el.textContent='Code by Yuhao@jiansutech.com - - - -';}})();
</script><div id="upgradeModal" class="upgrade-modal"><div class="upgrade-card"><div class="upgrade-head"><div class="upgrade-title">鍗囩骇杩囩▼</div><button class="upgrade-close" onclick="closeUpgradeModal()">鍏抽棴</button></div><pre id="upgradeLog" class="upgrade-log"></pre><div id="upgradeFoot" class="upgrade-foot">绛夊緟寮€濮?..</div></div></div><div id="globalFooter" class="footer">Code by Yuhao@jiansutech.com - loading - loading - loading</div></body></html>`

const monitorHTML = `<!doctype html>
<html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>ETH 娓呯畻闆峰尯鐩戞帶</title>
<style>
:root{--bg:#e8edf4;--nav:#0b1220;--nav-border:#243145;--panel:#ffffff;--line:#d8e0ea;--text:#20324a;--muted:#6d7c93;--accent:#39557c;--good:#169b62;--warn:#e58a17;--danger:#dc3d3d}
[data-theme="dark"]{--bg:#000;--nav:#000;--nav-border:#111827;--panel:#000;--line:#1f2937;--text:#e5e7eb;--muted:#94a3b8;--accent:#93c5fd;--good:#22c55e;--warn:#f59e0b;--danger:#ef4444}
body{margin:0;background:var(--bg);color:var(--text);font-family:Inter,system-ui,Segoe UI,Arial,sans-serif}
.nav{height:56px;background:var(--nav);border-bottom:1px solid var(--nav-border);display:flex;align-items:center;justify-content:space-between;padding:0 20px;position:sticky;top:0;z-index:10}
.nav-left,.nav-right{display:flex;align-items:center;gap:20px}.brand{font-size:18px;font-weight:700;color:#eef3f9}
.menu a{color:#d6deea;text-decoration:none;font-size:16px;margin-right:18px}.menu a.active{color:#fff;font-weight:700}
.upgrade{color:#fff;font-weight:700;text-decoration:none}.wrap{max-width:1280px;margin:0 auto;padding:14px}
.theme-toggle{display:inline-flex;align-items:center;gap:6px;font-size:13px}.theme-toggle button{height:30px;padding:0 10px;border-radius:999px;border:1px solid rgba(148,163,184,0.45);background:transparent;color:#e5e7eb;cursor:pointer}.theme-toggle button.label{cursor:default;opacity:.92}.theme-toggle button.active{background:rgba(255,255,255,0.12);border-color:rgba(255,255,255,0.18);color:#fff}
.hero{background:linear-gradient(135deg,#274160,#3d5d86);color:#fff;border-radius:12px;padding:14px 18px;display:flex;justify-content:space-between;align-items:center;gap:16px}
.hero-title{font-size:18px;font-weight:800}.hero-meta{font-size:14px;font-weight:700}
.panel{border:1px solid var(--line);background:var(--panel);margin:10px 0;padding:10px 12px;border-radius:10px;box-shadow:0 1px 2px rgba(15,23,42,.03)}
.topbar{display:flex;justify-content:space-between;align-items:center;gap:12px;flex-wrap:wrap}
.hint{font-size:12px;color:var(--muted)} .btns button{margin-right:8px;background:#fff;color:var(--text);border:1px solid #b6c4d5;padding:8px 12px;border-radius:8px;cursor:pointer}
.btns button.active{background:var(--accent);color:#fff;border-color:var(--accent)}
 .grid{display:grid;grid-template-columns:1.45fr .9fr;gap:10px}.subgrid{display:grid;grid-template-columns:repeat(4,1fr);gap:0;border:1px solid var(--line);border-radius:10px;overflow:hidden}
.card{padding:14px;background:#fff;border-right:1px solid var(--line)}.card:last-child{border-right:0}.card .label{font-size:12px;color:var(--muted);margin-bottom:10px}.card .val{font-size:22px;font-weight:800;line-height:1.05}
.good{color:var(--good)}.warn{color:var(--warn)}.danger{color:var(--danger)}
.sidebox{padding:10px 12px}.sidebox h3,.panel h3{margin:0 0 10px 0}
.state-list{display:grid;gap:8px;font-weight:700}.state-line{display:flex;gap:10px;flex-wrap:wrap;align-items:baseline}.state-line .mini{font-size:16px;color:var(--text);font-weight:700}.state-line .funding-tag{font-size:14px;color:var(--muted);font-weight:700}
.main-grid{display:grid;grid-template-columns:1.45fr .9fr;gap:10px}.stack{display:grid;gap:10px}
table{width:100%;border-collapse:collapse}th,td{border-bottom:1px solid var(--line);padding:7px 8px;text-align:center}thead th{background:#5d7291;color:#fff;font-size:13px}
.heat-report-table{table-layout:fixed;font-weight:800;color:#34445a;font-size:16px}.heat-report-table th,.heat-report-table td{border:1px solid #f4f6f8;padding:8px 6px;font-weight:800}.heat-report-table .title th{background:#394c66;color:#fff;font-size:20px;padding:12px 6px}.heat-report-table .spot th{background:#394c66;color:#fff;text-align:left;padding-left:46px;font-size:18px}.heat-report-table .spot .price{text-align:left;padding-left:36px}.heat-report-table .group th{background:#d8dde5;color:#34445a}.heat-report-table .group .down{background:#f3e9c3;color:#c96f34}.heat-report-table .sub th{background:#d8dde5;color:#34445a}.heat-report-table .sub .down{background:#f5edc8;color:#c96f34}.heat-report-table tbody tr{background:#eeeeee}.heat-report-table tbody tr.alt{background:#e0e0e0}.heat-report-table tbody tr.hot{background:#d4d4d4}.heat-report-table tbody tr.longest{background:#e6e6e6}.heat-report-table .down-cell,.heat-report-table .diff-cell.diff-long{color:#cf7436}.heat-report-table .diff-cell.diff-short{color:#34445a}.heat-report-table .diff-cell.diff-flat{color:#34445a}.heat-report-table .up-cell{color:#34445a}
.metric{display:flex;justify-content:space-between;gap:12px;padding:6px 0;border-bottom:1px solid var(--line)}.metric:last-child{border-bottom:0}
.bar-row{display:grid;grid-template-columns:72px 1fr 54px;gap:10px;align-items:center;margin:8px 0}.bar{height:10px;background:#e7edf5;border-radius:999px;overflow:hidden}.fill{height:100%;background:linear-gradient(90deg,#56b6d7,#3f7fb1)}
.triple{display:grid;grid-template-columns:repeat(3,1fr);gap:10px}
.mini{font-size:12px;color:var(--muted)} .big{font-size:16px;font-weight:800}
.upgrade-modal{position:fixed;inset:0;background:rgba(2,6,23,.55);display:none;align-items:center;justify-content:center;z-index:9999}.upgrade-modal.show{display:flex}.upgrade-card{width:min(880px,92vw);max-height:82vh;background:#0b1220;color:#e2e8f0;border:1px solid #334155;border-radius:10px;box-shadow:0 10px 30px rgba(2,6,23,.45);overflow:hidden}.upgrade-head{display:flex;align-items:center;justify-content:space-between;padding:10px 12px;border-bottom:1px solid #334155}.upgrade-title{font-size:14px;font-weight:700}.upgrade-close{background:transparent;border:1px solid #475569;color:#e2e8f0;border-radius:6px;padding:4px 8px;cursor:pointer}.upgrade-log{margin:0;padding:12px;white-space:pre-wrap;overflow:auto;max-height:62vh;font-family:ui-monospace,SFMono-Regular,Menlo,Consolas,monospace;font-size:12px;line-height:1.45}.upgrade-foot{padding:8px 12px;border-top:1px solid #334155;font-size:12px;color:#94a3b8}.footer{margin:18px auto 0 auto;max-width:1200px;padding:10px 12px;font-size:12px;color:#64748b;text-align:center}
@media (max-width:980px){.grid,.main-grid,.triple{grid-template-columns:1fr}.subgrid{grid-template-columns:1fr 1fr}.card:nth-child(2){border-right:0}.hero{align-items:flex-start;flex-direction:column}}
</style></head><body>
<div class="nav"><div class="nav-left"><div class="brand">ETH Liquidation Map</div><div class="menu"><a href="/">娓呯畻鐑尯</a><a href="/config">妯″瀷閰嶇疆</a><a href="/monitor" class="active">闆峰尯鐩戞帶</a><a href="/map">鐩樺彛姹囨€?/a><a href="/liquidations">寮哄钩娓呯畻</a><a href="/bubbles">姘旀场鍥?/a><a href="/webdatasource">椤甸潰鏁版嵁婧?/a><a href="/channel">娑堟伅閫氶亾</a><a href="/analysis">鏃ュ唴鍒嗘瀽</a></div></div><div class="nav-right"><div class="theme-toggle"><button class="label" type="button">涓婚</button><button id="themeDark" onclick="setTheme('dark')">娣辫壊</button><button id="themeLight" onclick="setTheme('light')">娴呰壊</button></div><a href="#" class="upgrade" onclick="return doUpgrade(event)">&#21319;&#32423;</a></div></div>
<div class="wrap">
<div class="hero"><div class="hero-title">ETH 娓呯畻闆峰尯鐩戞帶锛圔eta锛?/div><div id="heroTime" class="hero-meta">--</div></div>
<div class="panel topbar"><div class="hint">鎸?0 / 1 / 7 / 3 蹇嵎鍒囨崲 鏃ュ唴 / 1澶?/ 7澶?/ 30澶?/div><div class="btns"><button data-days="0">鏃ュ唴</button><button data-days="1">1澶?/button><button data-days="7">7澶?/button><button data-days="30">30澶?/button></div></div>
<div class="grid">
<div class="panel"><h3>椤堕儴缁撹鍗?/h3><div id="topCards" class="subgrid"></div></div>
<div class="panel sidebox"><h3>甯傚満鐘舵€?/h3><div id="marketState" class="state-list"></div></div>
</div>
<div class="main-grid">
<div class="panel"><h3>娓呯畻鐑尯閫熸姤</h3><div id="heatReport"></div></div>
<div class="stack"><div class="panel"><h3>澶辫　缁熻</h3><div id="imbalanceStats"></div></div><div class="panel"><h3>瀵嗗害鍒嗗眰</h3><div id="densityLayers"></div></div><div class="panel"><h3>鍙樺寲璺熻釜锛堣緝涓婁竴鏃剁偣锛?/h3><div id="changeTrack"></div></div></div>
</div>
<div class="triple">
<div class="panel"><h3>鏈€闀挎煴 / 鏍稿績纾佸尯</h3><div id="coreZone"></div></div>
<div class="panel"><h3>浜ゆ槗鎵€璐＄尞鎷嗗垎锛?0鐐瑰唴锛?/h3><div id="exchangeContrib"></div></div>
<div class="panel"><h3>棰勮涓庡凡鍙戠敓娓呯畻</h3><div id="alerts"></div></div>
</div>
</div>
<script>
let currentDays=30;
let monitorLiqMapData=null;
let monitorWebMap=null;
let monitorLatestClose=0;
let monitorDisplayPrice=0;
let monitorLoadSeq=0;
let monitorLoadState={pending:false,done:false,days:currentDays,error:'',heatReady:false,seq:0};

function normalizeWindowDays(value){
  const n=Number(value);
  if(n===0||n===1||n===7||n===30) return n;
  return null;
}

const sharedWindowDaysKey='preferred_window_days';

function saveSharedWindowDays(days){
  const n=normalizeWindowDays(days);
  if(n==null) return;
  try{localStorage.setItem(sharedWindowDaysKey,String(n));}catch(_){}
}

function loadSharedWindowDays(){
  try{
    return normalizeWindowDays(localStorage.getItem(sharedWindowDaysKey));
  }catch(_){}
  return null;
}

function loadRequestedWindowDays(){
  try{
    const raw=new URLSearchParams(window.location.search).get('days');
    const n=normalizeWindowDays(raw);
    if(n!=null) return n;
  }catch(_){}
  return 30;
}

function updateMonitorLoadState(patch){
  monitorLoadState=Object.assign(monitorLoadState,patch||{});
  window.__monitorLoadState=monitorLoadState;
  return monitorLoadState;
}

window.addEventListener('error',function(event){
  const msg=String((event&&event.message)||'page error');
  updateMonitorLoadState({pending:false,done:true,error:msg,heatReady:false});
});

function setTheme(t){
  const theme=(t==='dark')?'dark':'light';
  document.documentElement.setAttribute('data-theme',theme);
  try{localStorage.setItem('theme',theme);}catch(_){}
  const bd=document.getElementById('themeDark'),bl=document.getElementById('themeLight');
  if(bd)bd.classList.toggle('active',theme==='dark');
  if(bl)bl.classList.toggle('active',theme==='light');
}

function initTheme(){
  let t='light';
  try{t=localStorage.getItem('theme')||'light';}catch(_){}
  setTheme(t);
}

function fmtPrice(n){
  const v=Number(n);
  if(!isFinite(v)) return '-';
  return v.toLocaleString('zh-CN',{minimumFractionDigits:1,maximumFractionDigits:1});
}

function fmtHeatPrice(n){
  const v=Number(n);
  if(!isFinite(v)) return '-';
  return v.toLocaleString('zh-CN',{minimumFractionDigits:1,maximumFractionDigits:1});
}

function fmtAmount(n){
  const v=Number(n);
  if(!isFinite(v)) return '-';
  const a=Math.abs(v);
  if(a>=1e8) return (v/1e8).toFixed(2)+'浜?;
  if(a>=1e6) return (v/1e6).toFixed(2)+'鐧句竾';
  return (v/1e4).toFixed(1)+'涓?;
}

function fmtFunding(n){
  const v=Number(n);
  if(!isFinite(v)) return '-';
  return v.toFixed(8);
}

function amountYi(n){
  const v=Number(n);
  if(!isFinite(v)) return '-';
  return (v/1e8).toFixed(2);
}

function biasClassByText(v){
  const s=String(v||'');
  if(s.includes('绌?)) return 'warn';
  if(s.includes('澶?)) return 'good';
  return '';
}

function deltaClass(v){
  const n=Number(v);
  if(!isFinite(n)) return '';
  if(n>0) return 'warn';
  if(n<0) return 'good';
  return '';
}

function renderActive(){
  document.querySelectorAll('button[data-days]').forEach(function(b){
    b.classList.toggle('active',Number(b.dataset.days)===currentDays);
  });
}

async function setWindow(days){
  currentDays=days;
  saveSharedWindowDays(days);
  updateMonitorLoadState({pending:true,done:false,days:days,error:'',heatReady:false});
  const resp=await fetch('/api/window',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({days:days})});
  if(!resp.ok){
    const msg=String(await resp.text().catch(function(){return resp.statusText||'set window failed';})||resp.statusText||'set window failed').trim();
    updateMonitorLoadState({pending:false,done:true,days:days,error:msg,heatReady:false});
    throw new Error(msg);
  }
  renderActive();
  await load({days:days});
}

function metricRow(label,val,cls){
  return '<div class="metric"><span>'+label+'</span><strong class="'+(cls||'')+'">'+val+'</strong></div>';
}

function fundingDirectionLabel(v){
  const n=Number(v);
  if(!isFinite(n)) return '-';
  if(n>0) return '澶氫粯绌?;
  if(n<0) return '绌轰粯澶?;
  return '涓€?;
}

function roundMonitorPrice1(v){
  const n=Number(v);
  if(!(isFinite(n)&&n>0)) return 0;
  return Math.round(n*10)/10;
}

function resolveMonitorDisplayPrice(fallback){
  const webPrice=roundMonitorPrice1(monitorWebMap&&monitorWebMap.current_price);
  if(webPrice>0) return webPrice;
  const dashPrice=roundMonitorPrice1(fallback);
  if(dashPrice>0) return dashPrice;
  return roundMonitorPrice1(monitorLiqMapData&&monitorLiqMapData.current_price);
}

function buildMonitorModelPoints(model){
  if(!model||!Array.isArray(model.prices)||!Array.isArray(model.intensity_grid)||!model.prices.length) return [];
  const rows=model.prices.length;
  const cols=((model.intensity_grid&&model.intensity_grid[0])?model.intensity_grid[0].length:0);
  if(rows<1||cols<1) return [];
  const pts=[];
  for(let ri=0;ri<rows;ri++){
    const p=Number(model.prices[ri]||0);
    if(!(p>0)) continue;
    let sum=0;
    for(let ci=0;ci<cols;ci++) sum+=Math.max(0,Number((model.intensity_grid[ri]||[])[ci]||0));
    if(sum>0) pts.push({price:p,total:sum});
  }
  return pts;
}

function buildMonitorWebGroups(map){
  if(!map||!map.has_data||!Array.isArray(map.points)||!map.points.length) return [];
  const groups=new Map();
  for(const pt of map.points){
    const price=Number(pt&&pt.price||0);
    const val=Number(pt&&pt.liq_value||0);
    if(!(price>0&&val>0)) continue;
    const side=String(pt&&pt.side||'').toLowerCase()==='short'?'short':'long';
    const key=side+'|'+price.toFixed(4);
    let g=groups.get(key);
    if(!g){
      g={side:side,price:price,total:0};
      groups.set(key,g);
    }
    g.total+=val;
  }
  return Array.from(groups.values()).sort(function(a,b){return Number(a.price||0)-Number(b.price||0);});
}

function chooseMonitorPeak(points,anchorPrice,side){
  const cp=roundMonitorPrice1(anchorPrice);
  if(!(cp>0)||!Array.isArray(points)||!points.length) return {price:0,v:0,cum:0};
  const dir=side==='up'?'up':'down';
  const peak={price:0,v:0,cum:0};
  for(const pt of points){
    const price=Number(pt&&pt.price||0);
    const total=Number((pt&&(pt.total!=null?pt.total:pt.notional))||0);
    if(!(price>0&&total>0)) continue;
    if(dir==='up'&&price<cp) continue;
    if(dir==='down'&&price>cp) continue;
    if(total>peak.v){
      peak.price=price;
      peak.v=total;
      continue;
    }
    if(total===peak.v&&peak.price>0){
      const priceDist=Math.abs(price-cp);
      const peakDist=Math.abs(peak.price-cp);
      if(priceDist<peakDist||(priceDist===peakDist&&price<peak.price)){
        peak.price=price;
      }
    }
  }
  if(!(peak.price>0&&peak.v>0)) return peak;
  for(const pt of points){
    const price=Number(pt&&pt.price||0);
    const total=Number((pt&&(pt.total!=null?pt.total:pt.notional))||0);
    if(!(price>0&&total>0)) continue;
    if(dir==='up'&&price>=cp&&price<=peak.price) peak.cum+=total;
    if(dir==='down'&&price<=cp&&price>=peak.price) peak.cum+=total;
  }
  if(peak.cum<peak.v) peak.cum=peak.v;
  return peak;
}

function buildHeatBandsFromModel(model,anchorPrice){
  const pts=buildMonitorModelPoints(model);
  if(!pts.length) return [];
  const cp=roundMonitorPrice1(anchorPrice||(model&&model.current_price));
  if(!(cp>0)) return [];
  const desired=[10,20,30,40,50,60,70,80,90,100,150,200,250,300];
  return desired.map(function(band){
    let upNotional=0,downNotional=0;
    for(const pt of pts){
      const dist=pt.price-cp;
      if(dist>=0&&dist<=band) upNotional+=pt.total;
      if(dist<=0&&Math.abs(dist)<=band) downNotional+=pt.total;
    }
    return {
      band:band,
      up_price:cp+band,
      up_notional_usd:upNotional,
      down_price:cp-band,
      down_notional_usd:downNotional
    };
  });
}

function buildHeatBandsFromWebMap(map,anchorPrice){
  if(!map||!map.has_data||!Array.isArray(map.points)||!map.points.length) return [];
  const cp=roundMonitorPrice1(anchorPrice||(map&&map.current_price));
  if(!(cp>0)) return [];
  const groups=buildMonitorWebGroups(map);
  if(!groups.length) return [];
  const desired=[10,20,30,40,50,60,70,80,90,100,150,200,250,300];
  return desired.map(function(band){
    let upNotional=0,downNotional=0;
    const upBound=cp+band;
    const downBound=cp-band;
    for(const g of groups){
      const price=Number(g&&g.price||0);
      const total=Number(g&&g.total||0);
      const side=String(g&&g.side||'').toLowerCase()==='short'?'short':'long';
      if(!(price>0&&total>0)) continue;
      if(side==='short'&&price>=cp&&price<=upBound) upNotional+=total;
      if(side==='long'&&price<=cp&&price>=downBound) downNotional+=total;
    }
    return {
      band:band,
      up_price:cp+band,
      up_notional_usd:upNotional,
      down_price:cp-band,
      down_notional_usd:downNotional
    };
  });
}

function windowKeyForDays(days){
  const n=Number(days);
  if(n===30) return '30d';
  if(n===7) return '7d';
  if(n===1) return '1d';
  return '';
}

function buildPeakSummaryFromModel(model,anchorPrice){
  const pts=buildMonitorModelPoints(model);
  const cp=roundMonitorPrice1(anchorPrice||(model&&model.current_price));
  if(!(cp>0)||!pts.length) return null;
  return {
    source:'model',
    up:chooseMonitorPeak(pts,cp,'up'),
    down:chooseMonitorPeak(pts,cp,'down')
  };
}

function buildPeakSummaryFromWebMap(map,anchorPrice){
  if(!map||!map.has_data||!Array.isArray(map.points)||!map.points.length) return null;
  const cp=roundMonitorPrice1(anchorPrice);
  if(!(cp>0)) return null;
  const groups=new Map();
  for(const pt of map.points){
    const price=Number(pt.price||0),val=Number(pt.liq_value||0);
    if(!(price>0&&val>0)) continue;
    const side=String(pt.side||'').toLowerCase()==='short'?'short':'long';
    const key=side+'|'+price.toFixed(4);
    let g=groups.get(key);
    if(!g){
      g={side:side,price:price,total:0};
      groups.set(key,g);
    }
    g.total+=val;
  }
  const upPoints=[];
  const downPoints=[];
  for(const g of groups.values()){
    if(g.side==='short') upPoints.push(g);
    if(g.side==='long') downPoints.push(g);
  }
  return {
    source:'webdatasource',
    up:chooseMonitorPeak(upPoints,cp,'up'),
    down:chooseMonitorPeak(downPoints,cp,'down')
  };
}

function resolveMonitorPeaks(anchorPrice){
  const cp=roundMonitorPrice1(anchorPrice||monitorDisplayPrice||(monitorLiqMapData&&monitorLiqMapData.current_price));
  return buildPeakSummaryFromWebMap(monitorWebMap,cp)||buildPeakSummaryFromModel(monitorLiqMapData,cp)||{source:'none',up:{price:0,v:0,cum:0},down:{price:0,v:0,cum:0}};
}

function renderTopCards(d){
  const analytics=d.analytics||{};
  const top=analytics.top||{};
  const market=analytics.market||{};
  const cards=[
    {label:'ETH鐜颁环',val:fmtPrice(d.current_price)},
    {label:'鐭嚎鍊惧悜',val:top.short_bias||'-',cls:biasClassByText(top.short_bias)},
    {label:'20鐐瑰唴澶辫　',val:fmtAmount(Math.abs(Number(top.bias20_delta||0))),cls:deltaClass(top.bias20_delta)},
    {label:'50鐐瑰唴鍒ゆ柇',val:top.bias50_label||'-',cls:biasClassByText(top.bias50_label)}
  ];
  const topCards=document.getElementById('topCards');
  if(topCards) topCards.innerHTML=cards.map(function(c){
    return '<div class="card"><div class="label">'+c.label+'</div><div class="val '+(c.cls||'')+'">'+c.val+'</div></div>';
  }).join('');

  const states=Array.isArray(d.states)?d.states:[];
  const byEx={};
  for(const s of states){
    const ex=String(s.exchange||'').toLowerCase();
    if(ex) byEx[ex]=s;
  }
  function exLine(name,key,oi){
    const st=byEx[key]||{};
    const fr=st.funding_rate==null?'-':fmtFunding(st.funding_rate);
    const dir=st.funding_rate==null?'-':fundingDirectionLabel(st.funding_rate);
    return '<div class="state-line"><span>'+name+' OI</span><strong>'+fmtAmount(oi)+'</strong><span class="mini">璐圭巼 '+fr+'</span><span class="funding-tag">'+dir+'</span></div>';
  }
  const marketState=document.getElementById('marketState');
  if(marketState) marketState.innerHTML=
    exLine('Binance','binance',market.binance_oi_usd)+
    exLine('Bybit','bybit',market.bybit_oi_usd)+
    exLine('OKX','okx',market.okx_oi_usd)+
    '<div class="state-line"><span>骞冲潎璐圭巼</span><strong>'+fmtFunding(market.avg_funding)+'</strong><span class="funding-tag">'+(market.avg_funding_verdict||'-')+'</span></div>';
}

function renderHeatReport(d){
  const wrap=document.getElementById('heatReport');
  if(!wrap) return;
  const anchorPrice=Number(d.current_price||monitorDisplayPrice||0);
  const webBands=buildHeatBandsFromWebMap(monitorWebMap,anchorPrice);
  const bands=webBands.length?webBands:buildHeatBandsFromModel(monitorLiqMapData,anchorPrice);
  if(!bands.length){
    wrap.innerHTML='<div class="hint">鏆傛棤鏁版嵁</div>';
    return;
  }
  const peaks=resolveMonitorPeaks(anchorPrice);
  const topUp=(peaks&&peaks.up)||{price:0,v:0,cum:0};
  const topDown=(peaks&&peaks.down)||{price:0,v:0,cum:0};
  const hot={20:true,50:true,80:true,100:true,200:true,300:true};
  const dateText=new Date(d.generated_at||Date.now()).toLocaleDateString('zh-CN').split('/').join('.');
  let html='<table class="heat-report-table"><thead>'+
    '<tr class="title"><th colspan="6">ETH 娓呯畻闆峰尯閫熸姤锛?+dateText+'锛?/th></tr>'+
    '<tr class="spot"><th>ETH鐜颁环</th><th colspan="5" class="price">'+fmtHeatPrice(d.current_price)+'</th></tr>'+
    '<tr class="group"><th rowspan="2">鑼冨洿</th><th colspan="2" class="down">涓嬫柟澶氬崟</th><th colspan="2">涓婃柟绌哄崟</th><th rowspan="2">宸€硷紙浜匡級</th></tr>'+
    '<tr class="sub"><th class="down">浠锋牸</th><th class="down">瑙勬ā锛堜嚎锛?/th><th>浠锋牸</th><th>瑙勬ā锛堜嚎锛?/th></tr>'+
    '</thead><tbody>';
  for(let i=0;i<bands.length;i++){
    const b=bands[i];
    const up=Number(b.up_notional_usd||0);
    const down=Number(b.down_notional_usd||0);
    const cls=(i%2?'alt ':'')+(hot[b.band]?'hot':'');
    const diffClass=down>up?'diff-long':(up>down?'diff-short':'diff-flat');
    html+='<tr class="'+cls+'"><td>'+b.band+'鐐瑰唴</td><td class="down-cell">'+fmtHeatPrice(b.down_price)+'</td><td class="down-cell">'+amountYi(down)+'</td><td class="up-cell">'+fmtHeatPrice(b.up_price)+'</td><td class="up-cell">'+amountYi(up)+'</td><td class="diff-cell '+diffClass+'">'+amountYi(Math.abs(down-up))+'</td></tr>';
  }
  const topUpCum=Number(topUp.cum||0);
  const topDownCum=Number(topDown.cum||0);
  const longestDiffClass=topDownCum>topUpCum?'diff-long':(topUpCum>topDownCum?'diff-short':'diff-flat');
  html+='<tr class="longest"><td>鏈€闀挎煴鍐呮€婚噺</td><td class="down-cell">'+fmtHeatPrice(topDown.price)+'</td><td class="down-cell">'+amountYi(topDownCum)+'</td><td class="up-cell">'+fmtHeatPrice(topUp.price)+'</td><td class="up-cell">'+amountYi(topUpCum)+'</td><td class="diff-cell '+longestDiffClass+'">'+amountYi(Math.abs(topDownCum-topUpCum))+'</td></tr></tbody></table>';
  wrap.innerHTML=html;
}

function renderImbalance(d){
  const rows=((d.analytics||{}).imbalance_stats)||[];
  const el=document.getElementById('imbalanceStats');
  if(!el) return;
  if(!rows.length){
    el.innerHTML='<div class="hint">鏆傛棤鏁版嵁</div>';
    return;
  }
  el.innerHTML=rows.map(function(r){
    return metricRow(
      String(r.band||'-')+'鐐瑰唴',
      '涓婃柟 '+fmtAmount(r.up_notional_usd)+' / 涓嬫柟 '+fmtAmount(r.down_notional_usd)+' / '+String(r.verdict||'-'),
      biasClassByText(r.verdict)
    );
  }).join('');
}

function renderDensity(d){
  const rows=((d.analytics||{}).density_layers)||[];
  const el=document.getElementById('densityLayers');
  if(!el) return;
  if(!rows.length){
    el.innerHTML='<div class="hint">鏆傛棤鏁版嵁</div>';
    return;
  }
  let html='<table><thead><tr><th>鍖洪棿</th><th>涓婃柟绌哄崟</th><th>涓嬫柟澶氬崟</th></tr></thead><tbody>';
  for(const r of rows){
    html+='<tr><td>'+String(r.label||'-')+'</td><td>'+fmtAmount(r.up_notional_usd)+'</td><td>'+fmtAmount(r.down_notional_usd)+'</td></tr>';
  }
  html+='</tbody></table>';
  el.innerHTML=html;
}

function renderTrack(d){
  const t=((d.analytics||{}).change_tracking)||{};
  const el=document.getElementById('changeTrack');
  if(!el) return;
  el.innerHTML=
    metricRow('20鐐瑰唴涓婃柟绌哄崟',fmtAmount(t.up20_delta_usd),deltaClass(t.up20_delta_usd))+
    metricRow('20鐐瑰唴涓嬫柟澶氬崟',fmtAmount(t.down20_delta_usd),deltaClass(t.down20_delta_usd))+
    metricRow('鏈€闀挎煴鍙樺寲',fmtAmount(t.longest_delta_usd),deltaClass(t.longest_delta_usd))+
    metricRow('Funding鍙樺寲',fmtFunding(t.funding_delta),deltaClass(t.funding_delta));
}

function renderCore(d){
  const c=((d.analytics||{}).core_zone)||{};
  const el=document.getElementById('coreZone');
  if(!el) return;
  const peaks=resolveMonitorPeaks(Number(d.current_price||monitorDisplayPrice||0));
  const cp=Number(d.current_price||0);
  const up=(peaks&&peaks.up&&peaks.up.v>0)?peaks.up:{price:Number(c.up_price||0),v:Number(c.up_notional_usd||0)};
  const down=(peaks&&peaks.down&&peaks.down.v>0)?peaks.down:{price:Number(c.down_price||0),v:Number(c.down_notional_usd||0)};
  let nearestSide=String(c.nearest_side||'-');
  let nearestPrice=Number(c.nearest_strong_price||0);
  let nearestDist=Number(c.nearest_distance||0);
  const upDist=(cp>0&&up.price>0)?Math.abs(up.price-cp):Infinity;
  const downDist=(cp>0&&down.price>0)?Math.abs(cp-down.price):Infinity;
  if(upDist<downDist){
    nearestSide='涓婃柟寮哄尯';
    nearestPrice=up.price;
    nearestDist=upDist;
  }else if(downDist<Infinity){
    nearestSide='涓嬫柟寮哄尯';
    nearestPrice=down.price;
    nearestDist=downDist;
  }
  el.innerHTML=
    metricRow('涓婃柟鏈€闀挎煴',fmtPrice(up.price)+' / '+fmtAmount(up.v),'warn')+
    metricRow('涓嬫柟鏈€闀挎煴',fmtPrice(down.price)+' / '+fmtAmount(down.v),'good')+
    metricRow('鏈€杩戝己鍖?,fmtPrice(nearestPrice)+' / '+(isFinite(Number(nearestDist))?Number(nearestDist).toFixed(1):'-')+'鐐?,biasClassByText(nearestSide))+
    metricRow('鏂瑰悜',String(nearestSide||'-'));
}

function renderContrib(d){
  const rows=((d.analytics||{}).exchange_contrib)||[];
  const el=document.getElementById('exchangeContrib');
  if(!el) return;
  if(!rows.length){
    el.innerHTML='<div class="hint">鏆傛棤鏁版嵁</div>';
    return;
  }
  el.innerHTML=rows.map(function(r){
    const share=Math.max(0,Number(r.share||0));
    return '<div class="bar-row"><div>'+String(r.exchange||'-')+'</div><div class="bar"><div class="fill" style="width:'+Math.max(4,share*100)+'%"></div></div><div>'+(share*100).toFixed(0)+'%</div></div>';
  }).join('')+'<div class="mini">涓昏础鐚氦鏄撴墍锛?+String(((d.analytics||{}).dominant_exchange)||'-')+'</div>';
}

function renderAlerts(d){
  const a=((d.analytics||{}).alert)||{};
  const el=document.getElementById('alerts');
  if(!el) return;
  el.innerHTML=
    metricRow('褰撳墠棰勮',String(a.level||'-'),String(a.level||'').includes('棰勮')?'danger':'')+
    metricRow('鏈€杩戜簨浠?,[a.recent_exchange||'-',a.recent_side||'-',a.recent_price?fmtPrice(a.recent_price):'-'].join(' / '))+
    metricRow('杩?鍒嗛挓娓呯畻',fmtAmount(a.recent_1m_usd),'warn')+
    metricRow('Binance / Bybit / OKX',fmtAmount(a.recent_1m_binance_usd)+' / '+fmtAmount(a.recent_1m_bybit_usd)+' / '+fmtAmount(a.recent_1m_okx_usd))+
    metricRow('寤鸿',String(a.suggestion||'-'));
}

async function load(opts){
  const seq=++monitorLoadSeq;
  const requestedDays=normalizeWindowDays(opts&&opts.days);
  const loadDays=requestedDays!=null?requestedDays:(normalizeWindowDays(currentDays)!=null?currentDays:1);
  updateMonitorLoadState({pending:true,done:false,days:loadDays,error:'',heatReady:false,seq:seq});
  try{
    const cfg=await fetch('/api/model-config').then(function(r){return r.json();}).catch(function(){return null;});
    const bucket=Number((cfg&&cfg.BucketMin)||(cfg&&cfg.bucket_min)||5);
    const step=Number((cfg&&cfg.PriceStep)||(cfg&&cfg.price_step)||5);
    const range=Number((cfg&&cfg.PriceRange)||(cfg&&cfg.price_range)||400);
    const queryDays='days='+encodeURIComponent(String(loadDays));
    const responses=await Promise.all([
      fetch('/api/dashboard?'+queryDays),
      fetch('/api/model/liquidation-map?'+queryDays+'&bucket_min='+bucket+'&price_step='+step+'&price_range='+range)
    ]);
    const dashResp=responses[0], mapResp=responses[1];
    if(!dashResp.ok) throw new Error('load dashboard failed: '+dashResp.status);
    if(!mapResp.ok) throw new Error('load model map failed: '+mapResp.status);
    const d=await dashResp.json();
    const mapData=await mapResp.json().catch(function(){return null;});
    if(seq!==monitorLoadSeq) return false;
    monitorLiqMapData=mapData;
    currentDays=normalizeWindowDays(d&&d.window_days);
    if(currentDays==null) currentDays=loadDays;
    const webWindow=windowKeyForDays(currentDays);
    const auxResponses=await Promise.all([
      webWindow?fetch('/api/webdatasource/map?window='+encodeURIComponent(webWindow)).then(function(r){return r.ok?r.json():null;}).catch(function(){return null;}):Promise.resolve(null)
    ]);
    if(seq!==monitorLoadSeq) return false;
    monitorWebMap=auxResponses[0];
    monitorLatestClose=0;
    monitorDisplayPrice=resolveMonitorDisplayPrice(d&&d.current_price);
    if(monitorDisplayPrice>0) d.current_price=monitorDisplayPrice;
    renderActive();
    const heroTime=document.getElementById('heroTime');
    if(heroTime) heroTime.textContent=new Date(d.generated_at||Date.now()).toLocaleString('zh-CN',{hour12:false});
    renderTopCards(d);
    renderHeatReport(d);
    renderImbalance(d);
    renderDensity(d);
    renderTrack(d);
    renderCore(d);
    renderContrib(d);
    renderAlerts(d);
    const wrap=document.getElementById('heatReport');
    const heatReady=!!(wrap&&(wrap.querySelector('table')||String(wrap.textContent||'').trim().length>0));
    updateMonitorLoadState({pending:false,done:true,days:currentDays,error:'',heatReady:heatReady,seq:seq,generatedAt:Number(d.generated_at||0)});
    return true;
  }catch(err){
    if(seq!==monitorLoadSeq) return false;
    const msg=String((err&&err.message)||err||'load failed').trim();
    const wrap=document.getElementById('heatReport');
    if(wrap&&!String(wrap.textContent||'').trim()) wrap.innerHTML='<div class="hint">鍔犺浇澶辫触</div>';
    updateMonitorLoadState({pending:false,done:true,days:currentDays,error:msg,heatReady:false,seq:seq});
    throw err;
  }
}

async function openUpgradeModal(){
  const m=document.getElementById('upgradeModal'),logEl=document.getElementById('upgradeLog'),foot=document.getElementById('upgradeFoot');
  if(!m||!logEl||!foot) return;
  m.classList.add('show');
  logEl.textContent='';
  foot.textContent='姝ｅ湪瑙﹀彂鍗囩骇...';
  const r=await fetch('/api/upgrade/pull',{method:'POST'});
  const d=await r.json().catch(function(){return {error:'response parse failed',output:''};});
  if(d.error){
    logEl.textContent=String(d.output||'');
    foot.textContent='瑙﹀彂澶辫触: '+d.error;
    return;
  }
  foot.textContent='宸茶Е鍙戯紝姝ｅ湪鎵ц...';
  let stable=0;
  for(let i=0;i<180;i++){
    await new Promise(function(res){setTimeout(res,1000);});
    const pr=await fetch('/api/upgrade/progress').then(function(x){return x.json();}).catch(function(){return null;});
    if(!pr) continue;
    logEl.textContent=String(pr.log||'');
    logEl.scrollTop=logEl.scrollHeight;
    if(pr.done){
      foot.textContent=(String(pr.exit_code||'')==='0')?'鍗囩骇瀹屾垚骞跺凡閲嶅惎':'鍗囩骇瀹屾垚锛岄€€鍑虹爜 '+String(pr.exit_code||'?');
      return;
    }
    if(!pr.running) stable++; else stable=0;
    if(stable>=3){
      foot.textContent='鍗囩骇杩涚▼宸茬粨鏉燂紝浣嗙姸鎬佹湭鐭ワ紝璇锋鏌ユ棩蹇?;
      return;
    }
  }
  foot.textContent='鍗囩骇浠嶅湪杩涜锛岃绋嶅悗鍐嶇湅';
}

function closeUpgradeModal(){
  const m=document.getElementById('upgradeModal');
  if(m) m.classList.remove('show');
}

async function doUpgrade(event){
  if(event) event.preventDefault();
  openUpgradeModal();
  return false;
}

updateMonitorLoadState({});
initTheme();
document.querySelectorAll('button[data-days]').forEach(function(b){b.onclick=function(){setWindow(Number(b.dataset.days)).catch(function(){});};});
document.addEventListener('keydown',function(e){
  if(e.key==='0') setWindow(0).catch(function(){});
  if(e.key==='1') setWindow(1).catch(function(){});
  if(e.key==='7') setWindow(7).catch(function(){});
  if(e.key==='3') setWindow(30).catch(function(){});
});
setInterval(function(){load().catch(function(){});},5000);
const preferredMonitorDays=loadRequestedWindowDays();
if(preferredMonitorDays!=null&&preferredMonitorDays!==currentDays){
  saveSharedWindowDays(preferredMonitorDays);
  setWindow(preferredMonitorDays).catch(function(){load().catch(function(){});});
}else{
  load({days:preferredMonitorDays!=null?preferredMonitorDays:currentDays}).catch(function(){});
}
(async function(){
  try{
    const r=await fetch('/api/version');
    const v=await r.json();
    const el=document.getElementById('globalFooter');
    if(el) el.textContent='Code by Yuhao@jiansutech.com - '+(v.commit_time||'-')+' - '+(v.commit_id||'-')+' - '+(v.branch||'-');
  }catch(_){
    const el=document.getElementById('globalFooter');
    if(el) el.textContent='Code by Yuhao@jiansutech.com - - - -';
  }
})();
</script><div id="upgradeModal" class="upgrade-modal"><div class="upgrade-card"><div class="upgrade-head"><div class="upgrade-title">鍗囩骇杩囩▼</div><button class="upgrade-close" onclick="closeUpgradeModal()">鍏抽棴</button></div><pre id="upgradeLog" class="upgrade-log"></pre><div id="upgradeFoot" class="upgrade-foot">绛夊緟寮€濮?..</div></div></div><div id="globalFooter" class="footer">Code by Yuhao@jiansutech.com - loading - loading - loading</div></body></html>`

const mapHTML = `<!doctype html>
<html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>鐩樺彛姹囨€?title>
<style>
body{margin:0;background:#f5f7fb;color:#1f2937;font-family:Inter,system-ui,Segoe UI,Arial,sans-serif}
.nav{height:56px;background:#0b1220;border-bottom:1px solid #243145;display:flex;align-items:center;justify-content:space-between;padding:0 20px;position:sticky;top:0;z-index:10}
.nav-left,.nav-right{display:flex;align-items:center;gap:20px}.brand{font-size:18px;font-weight:700;color:#eef3f9}
.menu a{color:#d6deea;text-decoration:none;font-size:16px;margin-right:18px}.menu a.active{color:#fff;font-weight:700}
.upgrade{color:#fff;font-weight:700;text-decoration:none}.wrap{width:100%;max-width:none;margin:0;padding:12px}
.theme-toggle{display:inline-flex;align-items:center;gap:6px;font-size:13px}.theme-toggle button{height:30px;padding:0 10px;border-radius:999px;border:1px solid rgba(148,163,184,0.45);background:transparent;color:#e5e7eb;cursor:pointer}.theme-toggle button.label{cursor:default;opacity:.92}.theme-toggle button.active{background:rgba(255,255,255,0.12);border-color:rgba(255,255,255,0.18);color:#fff}
.panel{border:1px solid #dce3ec;background:#fff;margin:14px 0;padding:16px;border-radius:10px;box-shadow:0 1px 2px rgba(15,23,42,.04)}
.row{display:flex;gap:12px;align-items:center;flex-wrap:wrap;justify-content:space-between}
.btns button,.mode button{background:#fff;color:#111827;border:1px solid #cbd5e1;padding:8px 14px;border-radius:8px;cursor:pointer}
.btns button.active,.mode button.active{background:#22c55e;color:#fff;border-color:#22c55e}
.small{font-size:12px;color:#6b7280}.legend{display:flex;gap:14px;align-items:center;flex-wrap:wrap;font-size:12px;color:#6b7280;margin-top:8px}
.tune{display:flex;gap:10px;align-items:center;flex-wrap:wrap;margin-top:8px}
.tune label{font-size:12px;color:#475569}
.tune select{height:30px;border:1px solid #cbd5e1;border-radius:6px;background:#fff;color:#111827;padding:0 8px}
.swatch{display:inline-block;width:10px;height:10px;border-radius:2px;margin-right:6px}
.meta{font-size:12px;color:#4b5563}.weights{font-size:12px;color:#334155;font-weight:700}.wsline{font-size:13px;color:#0f172a;font-weight:700;margin-top:2px}
.chart-wrap{display:flex;gap:14px;align-items:stretch}.chart-main{flex:1;min-width:0}
canvas{width:100%;height:760px;display:block;border:1px solid #e5e7eb;border-radius:10px;background:#fff}
.merge-grid{display:grid;grid-template-columns:repeat(3,minmax(180px,1fr));gap:10px;margin-bottom:10px}
.merge-card{border:1px solid #e2e8f0;border-radius:8px;padding:10px;background:#f8fafc}.merge-label{font-size:12px;color:#64748b}.merge-val{font-size:20px;font-weight:700;color:#0f172a}
#depthChart{height:312px}
.event-wrap{margin-top:12px;border:1px solid #e2e8f0;border-radius:10px;background:#f8fafc;padding:10px}
.event-title{font-size:16px;font-weight:700;color:#0f172a;margin-bottom:8px}
.event-grid{display:grid;grid-template-columns:1fr 1fr;gap:12px}
.event-card{border:1px solid #dbe3ef;border-radius:8px;background:#fff;padding:8px;max-height:360px;overflow:auto}
.event-card h4{margin:0 0 6px 0;font-size:14px}
.event-card table{width:100%;border-collapse:collapse}
.event-card th,.event-card td{font-size:12px;border-top:1px solid #eef2f7;padding:4px 6px;text-align:center}
.event-card thead th{border-top:0;color:#64748b}
[data-theme="dark"] body{background:#000;color:#e5e7eb}
[data-theme="dark"] .nav{background:#000;border-bottom-color:#111827}
[data-theme="dark"] .panel{background:#000;border-color:#1f2937}
[data-theme="dark"] .small,[data-theme="dark"] .legend,[data-theme="dark"] .meta,[data-theme="dark"] .merge-label,[data-theme="dark"] .event-card thead th{color:#94a3b8}
[data-theme="dark"] .tune label{color:#cbd5e1}
[data-theme="dark"] .btns button,[data-theme="dark"] .mode button,[data-theme="dark"] .tune select,[data-theme="dark"] .toggle{background:#000;color:#e5e7eb;border-color:#334155}
[data-theme="dark"] canvas{background:#000;border-color:#1f2937}
[data-theme="dark"] .merge-card,[data-theme="dark"] .event-wrap{background:#000;border-color:#1f2937}
[data-theme="dark"] .event-card{background:#000;border-color:#1f2937}
[data-theme="dark"] .wsline,[data-theme="dark"] .merge-val,[data-theme="dark"] .event-title,[data-theme="dark"] .event-card h4,[data-theme="dark"] #mergeTitle{color:#e5e7eb}
.upgrade-modal{position:fixed;inset:0;background:rgba(2,6,23,.55);display:none;align-items:center;justify-content:center;z-index:9999}.upgrade-modal.show{display:flex}.upgrade-card{width:min(880px,92vw);max-height:82vh;background:#0b1220;color:#e2e8f0;border:1px solid #334155;border-radius:10px;box-shadow:0 10px 30px rgba(2,6,23,.45);overflow:hidden}.upgrade-head{display:flex;align-items:center;justify-content:space-between;padding:10px 12px;border-bottom:1px solid #334155}.upgrade-title{font-size:14px;font-weight:700}.upgrade-close{background:transparent;border:1px solid #475569;color:#e2e8f0;border-radius:6px;padding:4px 8px;cursor:pointer}.upgrade-log{margin:0;padding:12px;white-space:pre-wrap;overflow:auto;max-height:62vh;font-family:ui-monospace,SFMono-Regular,Menlo,Consolas,monospace;font-size:12px;line-height:1.45}.upgrade-foot{padding:8px 12px;border-top:1px solid #334155;font-size:12px;color:#94a3b8}.footer{margin:18px auto 0 auto;max-width:1200px;padding:10px 12px;font-size:12px;color:#64748b;text-align:center}</style></head><body>
<div class="nav"><div class="nav-left"><div class="brand">ETH Liquidation Map</div><div class="menu"><a href="/">娓呯畻鐑尯</a><a href="/config">妯″瀷閰嶇疆</a><a href="/monitor">闆峰尯鐩戞帶</a><a href="/map" class="active">鐩樺彛姹囨€?/a><a href="/liquidations">寮哄钩娓呯畻</a><a href="/bubbles">姘旀场鍥?/a><a href="/webdatasource">椤甸潰鏁版嵁婧?/a><a href="/channel">娑堟伅閫氶亾</a><a href="/analysis">鏃ュ唴鍒嗘瀽</a></div></div><div class="nav-right"><div class="theme-toggle"><button class="label" type="button">涓婚</button><button id="themeDark" onclick="setTheme('dark')">娣辫壊</button><button id="themeLight" onclick="setTheme('light')">娴呰壊</button></div><a href="#" class="upgrade" onclick="return doUpgrade(event)">&#21319;&#32423;</a></div></div>
<div class="wrap">
  <div class="panel">
    <div class="row">
      <div>
        <h2 style="margin:0;color:#111827">鐩樺彛姹囨€?h2>
        <div class="small">鐟曞棛娲?Binance / Bybit / OKX 閻╂ê褰涙稉搴㈢ウ閸斻劍鈧?/div>
      </div>
      <div class="row"><div class="mode"><button data-mode="weighted">鍔犳潈妯″紡</button><button data-mode="merged">鍚堝苟妯″紡</button></div></div>
    </div>
    <div id="meta" class="meta"></div>
    <div id="weights" class="weights"></div>
    <div id="wsStatus" class="wsline"></div>
    <div class="legend"><span><i class="swatch" style="background:#8b5cf6"></i>Binance</span><span><i class="swatch" style="background:#eab308"></i>OKX</span><span><i class="swatch" style="background:#67e8f9"></i>Bybit</span></div>
    <div class="tune">
      <label>閸楀﹨鈥滈張?
        <select id="halfLifeSel">
          <option value="60">60缁?/option>
          <option value="120">120缁?/option>
          <option value="180">180缁?/option>
        </select>
      </label>
      <label>濠婃艾濮╃粣妤€褰?        <select id="rollWinSel">
          <option value="3">3鍒嗛挓</option>
          <option value="5">5鍒嗛挓</option>
          <option value="10">10鍒嗛挓</option>
        </select>
      </label>
    </div>
  </div>
  <div class="panel">
    <div class="row" style="justify-content:flex-start"><h3 id="mergeTitle" style="margin:0">鍚堝苟鐩樺彛缁熻</h3><div id="mergeHint" class="small">閸╄桨绨幋鎰唉娑?OI 娴兼媽顓?/div></div>
    <div id="mergeStats" class="merge-grid"></div>
    <canvas id="depthChart" width="1600" height="312"></canvas>
    <div class="event-wrap">
      <div class="event-title">浠锋牸浜嬩欢</div>
      <div class="event-grid">
        <div class="event-card">
          <div class="row" style="justify-content:space-between"><h4 style="color:#16a34a;margin:0">涔扮洏浜嬩欢</h4><div class="small"><button id="bidPrev" class="toggle" onclick="evtPrev('bid')">娑撳﹣绔存い?/button><button id="bidNext" class="toggle" onclick="evtNext('bid')">娑撳绔存い?/button><span id="bidPage" style="margin-left:8px"></span></div></div>
          <div id="bidEvents"></div>
        </div>
        <div class="event-card">
          <div class="row" style="justify-content:space-between"><h4 style="color:#dc2626;margin:0">鍗栫洏浜嬩欢</h4><div class="small"><button id="askPrev" class="toggle" onclick="evtPrev('ask')">娑撳﹣绔存い?/button><button id="askNext" class="toggle" onclick="evtNext('ask')">娑撳绔存い?/button><span id="askPage" style="margin-left:8px"></span></div></div>
          <div id="askEvents"></div>
        </div>
      </div>
    </div>
  </div>
</div>
<script>
let currentMode='weighted',dashboard=null,orderbook=null,depthState=null,isDraggingDepth=false,depthLastX=0,persistedEvents={bid:{rows:[],page:1,total:0},ask:{rows:[],page:1,total:0}};
function setTheme(t){const theme=(t==='dark')?'dark':'light';document.documentElement.setAttribute('data-theme',theme);try{localStorage.setItem('theme',theme);}catch(_){}const bd=document.getElementById('themeDark'),bl=document.getElementById('themeLight');if(bd)bd.classList.toggle('active',theme==='dark');if(bl)bl.classList.toggle('active',theme==='light');}
function initTheme(){let t='light';try{t=localStorage.getItem('theme')||'light';}catch(_){}setTheme(t);}
const depthMemory={halfLifeMs:120000,rollingWindowMs:5*60*1000,bidGhost:{},askGhost:{},bidMax:{},askMax:{},lastUpdate:0};
const depthEvents={minPersistMs:3000,linkFactor:0.6,bidTrack:{},askTrack:{},bidList:[],askList:[]};
function fmtPrice(n){return Number(n).toLocaleString('zh-CN',{minimumFractionDigits:1,maximumFractionDigits:1})}
function fmtAmount(n){n=Number(n);if(!isFinite(n))return '-';const a=Math.abs(n);if(a>=1e8)return (n/1e8).toFixed(2)+'娴?;if(a>=1e6)return (n/1e6).toFixed(2)+'閻у彞绔?;return (n/1e4).toFixed(2)+'娑?;}
function fmtQty(n){n=Number(n);if(!isFinite(n))return '-';return n.toLocaleString('zh-CN',{maximumFractionDigits:2})}
function windowLabel(v){return Number(v)===0?'鏃ュ唴':(Number(v)+'婢?)}
function syncTuneUI(){const h=document.getElementById('halfLifeSel'),r=document.getElementById('rollWinSel');if(h)h.value=String(Math.round(depthMemory.halfLifeMs/1000));if(r)r.value=String(Math.round(depthMemory.rollingWindowMs/60000));}
function applyTuneFromUI(){const h=document.getElementById('halfLifeSel'),r=document.getElementById('rollWinSel');const hs=Number(h&&h.value||120),rm=Number(r&&r.value||5);depthMemory.halfLifeMs=Math.max(1000,hs*1000);depthMemory.rollingWindowMs=Math.max(60000,rm*60000);try{localStorage.setItem('depth_tune',JSON.stringify({half_life_sec:hs,rolling_min:rm}));}catch(_){ }drawDepth();}
function loadTuneFromStorage(){try{const raw=localStorage.getItem('depth_tune');if(!raw)return;const t=JSON.parse(raw);const hs=Number(t&&t.half_life_sec||0),rm=Number(t&&t.rolling_min||0);if(hs===60||hs===120||hs===180)depthMemory.halfLifeMs=hs*1000;if(rm===3||rm===5||rm===10)depthMemory.rollingWindowMs=rm*60000;}catch(_){ }}
function priceKey(v){return Number(v).toFixed(1)}
function levelMap(levels){const out={};for(const lv of (levels||[])){const p=Number(lv.price||0),q=Number(lv.qty||0);if(!(p>0&&q>0))continue;out[priceKey(p)]=p*q;}return out;}
function decayGhost(mapObj,f){for(const k of Object.keys(mapObj)){mapObj[k]*=f;if(mapObj[k]<1e-3)delete mapObj[k];}}
function updateRollingMax(maxObj,curObj,now,windowMs){for(const k of Object.keys(maxObj)){if((now-maxObj[k].ts)>windowMs)delete maxObj[k];}for(const [k,v] of Object.entries(curObj)){const n=Number(v||0);if(!(n>0))continue;const old=maxObj[k];if(!old||n>=old.v)maxObj[k]={v:n,ts:now};}}
function wallThreshold(levelMapObj){const vals=Object.values(levelMapObj).map(v=>Number(v||0)).filter(v=>v>0).sort((a,b)=>a-b);if(!vals.length)return 1e12;const p=Math.floor(vals.length*0.85);const base=vals[Math.max(0,Math.min(vals.length-1,p))]||0;return Math.max(2.5e5,base);}
function hasLinkedNeighbor(levelMapObj,key,th){const p=Number(key),step=0.1,l=priceKey(p-step),r=priceKey(p+step),lv=Number(levelMapObj[l]||0),rv=Number(levelMapObj[r]||0);return lv>=th*depthEvents.linkFactor||rv>=th*depthEvents.linkFactor;}
function trackWalls(side,levelNow,th,now){const track=side==='bid'?depthEvents.bidTrack:depthEvents.askTrack;const live={};for(const [k,vRaw] of Object.entries(levelNow)){const v=Number(vRaw||0);if(v<th)continue;const linked=hasLinkedNeighbor(levelNow,k,th);const old=track[k];if(!old){track[k]={start:now,last:now,peak:v,duration:0,linkedEver:linked,emitted:false};}else{const gap=Math.max(0,now-Number(old.last||now));old.last=now;old.duration=(gap<=15000)?(Number(old.duration||0)+gap):0;old.peak=Math.max(Number(old.peak||0),v);old.linkedEver=Boolean(old.linkedEver||linked);}live[k]=true;}for(const k of Object.keys(track)){if(!live[k]&&now-Number(track[k].last||0)>15000)delete track[k];}}
async function postPriceEvent(side,item){try{await fetch('/api/price-events',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({side:side,price:item.price,peak:item.peak,duration_ms:item.dur_ms,event_ts:item.ts,mode:currentMode})});}catch(_){ }}
function pushPriceEvent(side,price,peak,durMs,ts){const item={price:Number(price),peak:Number(peak),dur_ms:Number(durMs),ts:Number(ts)};const list=side==='bid'?depthEvents.bidList:depthEvents.askList;list.unshift(item);if(list.length>18)list.length=18;postPriceEvent(side,item);}
function emitQualifiedEvents(side,now){const track=side==='bid'?depthEvents.bidTrack:depthEvents.askTrack;for(const [k,t] of Object.entries(track)){if(t.emitted)continue;if(Number(t.duration||0)<depthEvents.minPersistMs)continue;if(!t.linkedEver)continue;t.emitted=true;pushPriceEvent(side,Number(k),Number(t.peak||0),Number(t.duration||0),now);}}
function computeFilteredMap(side,levelNow,th){const out={};const track=side==='bid'?depthEvents.bidTrack:depthEvents.askTrack;for(const [k,vRaw] of Object.entries(levelNow)){const v=Number(vRaw||0);if(!(v>0))continue;const t=track[k];const strong=Boolean(t&&t.linkedEver&&Number(t.duration||0)>=depthEvents.minPersistMs);const linked=hasLinkedNeighbor(levelNow,k,th);const w=strong?1:(linked?0.35:0.1);out[k]=v*w;}return out;}
function updateDepthMemory(){const m=orderbook&&orderbook.merged;if(!m)return;const now=Date.now();if(depthMemory.lastUpdate===0)depthMemory.lastUpdate=now;const dt=Math.max(0,now-depthMemory.lastUpdate);depthMemory.lastUpdate=now;const decay=Math.pow(0.5,dt/depthMemory.halfLifeMs);decayGhost(depthMemory.bidGhost,decay);decayGhost(depthMemory.askGhost,decay);const bidNow=levelMap(m.bids),askNow=levelMap(m.asks),bidTh=wallThreshold(bidNow),askTh=wallThreshold(askNow);trackWalls('bid',bidNow,bidTh,now);trackWalls('ask',askNow,askTh,now);emitQualifiedEvents('bid',now);emitQualifiedEvents('ask',now);const bidFiltered=computeFilteredMap('bid',bidNow,bidTh),askFiltered=computeFilteredMap('ask',askNow,askTh);for(const [k,v] of Object.entries(bidFiltered)){depthMemory.bidGhost[k]=Math.max(Number(depthMemory.bidGhost[k]||0),Number(v||0));}for(const [k,v] of Object.entries(askFiltered)){depthMemory.askGhost[k]=Math.max(Number(depthMemory.askGhost[k]||0),Number(v||0));}updateRollingMax(depthMemory.bidMax,bidFiltered,now,depthMemory.rollingWindowMs);updateRollingMax(depthMemory.askMax,askFiltered,now,depthMemory.rollingWindowMs);}
function renderActive(){document.querySelectorAll('button[data-mode]').forEach(b=>b.classList.toggle('active',(b.dataset.mode||'')===currentMode));}
function resetDepthMemory(){depthMemory.bidGhost={};depthMemory.askGhost={};depthMemory.bidMax={};depthMemory.askMax={};depthMemory.lastUpdate=0;depthState=null;depthEvents.bidTrack={};depthEvents.askTrack={};depthEvents.bidList=[];depthEvents.askList=[];persistedEvents={bid:{rows:[],page:1,total:0},ask:{rows:[],page:1,total:0}};}
function setMode(mode){const m=(mode==='merged')?'merged':'weighted';if(currentMode===m)return;currentMode=m;resetDepthMemory();try{localStorage.setItem('orderbook_mode',currentMode);}catch(_){ }renderActive();load();}
function loadModeFromStorage(){try{const m=localStorage.getItem('orderbook_mode');if(m==='weighted'||m==='merged')currentMode=m;}catch(_){ }}
function renderMergedPanel(){const el=document.getElementById('mergeStats');const m=orderbook&&orderbook.merged;const t=document.getElementById('mergeTitle');const h=document.getElementById('mergeHint');if(t)t.textContent=currentMode==='weighted'?'鍔犳潈鐩樺彛缁熻':'鍚堝苟鐩樺彛缁熻';if(h)h.textContent=currentMode==='weighted'?'閸╄桨绨幋鎰唉娑?OI 娴兼媽顓?:'婢舵矮姘﹂弰鎾村閸氬牆鑻熼惄妯哄經娑撳簼鐜?;if(!m){el.innerHTML='<div class=\"small\">鏆傛棤鐩樺彛鏁版嵁</div>';return;}const spread=(Number(m.best_ask||0)-Number(m.best_bid||0));el.innerHTML='<div class=\"merge-card\"><div class=\"merge-label\">Best Bid</div><div class=\"merge-val\">'+fmtPrice(m.best_bid)+'</div></div>'+'<div class=\"merge-card\"><div class=\"merge-label\">Best Ask</div><div class=\"merge-val\">'+fmtPrice(m.best_ask)+'</div></div>'+'<div class=\"merge-card\"><div class=\"merge-label\">Mid / Spread</div><div class=\"merge-val\">'+fmtPrice(m.mid)+' <span style=\"font-size:12px;color:#64748b\">('+fmtPrice(spread)+')</span></div></div>';}
function fmtEventTime(ts){try{return new Date(ts).toLocaleTimeString('zh-CN',{hour12:false});}catch(_){return '-';}}
function fmtDur(ms){const s=Math.max(0,Math.round(Number(ms||0)/1000));return s+'s';}
function eventTable(list){if(!list||!list.length)return '<div class=\"small\">鏆傛棤浜嬩欢</div>';let h='<table><thead><tr><th>浠锋牸</th><th>瀹勬澘鈧?/th><th>鎸佺画鏃堕棿</th><th>鏃堕棿</th></tr></thead><tbody>';for(const e of list.slice(0,8)){h+='<tr><td>'+fmtPrice(e.price)+'</td><td>'+fmtAmount(e.peak)+'</td><td>'+fmtDur(e.dur_ms)+'</td><td>'+fmtEventTime(e.ts)+'</td></tr>';}return h+'</tbody></table>';}
function renderEventTables(){const be=document.getElementById('bidEvents'),ae=document.getElementById('askEvents');if(be)be.innerHTML=eventTable((persistedEvents.bid&&persistedEvents.bid.rows)||[]);if(ae)ae.innerHTML=eventTable((persistedEvents.ask&&persistedEvents.ask.rows)||[]);renderEvtPager('bid');renderEvtPager('ask');}
function renderEvtPager(side){const st=persistedEvents[side];const pageEl=document.getElementById(side+'Page');const prev=document.getElementById(side+'Prev');const next=document.getElementById(side+'Next');const page=Number((st&&st.page)||1);const total=Number((st&&st.total)||0);const pageSize=25;const pages=Math.max(1,Math.ceil(total/pageSize));if(pageEl)pageEl.textContent='缁?'+page+' / '+pages+' 妞?璺?'+total+' 閺?;if(prev)prev.disabled=page<=1;if(next)next.disabled=page>=pages;}
async function loadPriceEvents(side,page){const pageSize=25;const url='/api/price-events?side='+encodeURIComponent(side)+'&page='+encodeURIComponent(String(page||1))+'&limit='+encodeURIComponent(String(pageSize))+'&minutes=30';const r=await fetch(url);const d=await r.json().catch(()=>null);if(!d||!d.rows){persistedEvents[side]={rows:[],page:1,total:0};return;}const rows=[];for(const it of (d.rows||[])){rows.push({price:Number(it.price||0),peak:Number(it.peak||0),dur_ms:Number(it.duration_ms||0),ts:Number(it.event_ts||0)});}persistedEvents[side]={rows:rows,page:Number(d.page||1),total:Number(d.total||0)};}
async function evtPrev(side){const st=persistedEvents[side];const page=Math.max(1,Number((st&&st.page)||1)-1);await loadPriceEvents(side,page);renderEventTables();}
async function evtNext(side){const st=persistedEvents[side];const page=Math.max(1,Number((st&&st.page)||1)+1);await loadPriceEvents(side,page);renderEventTables();}
function syncDepthCanvas(c){const rect=c.getBoundingClientRect();const dpr=window.devicePixelRatio||1;const W=Math.max(320,Math.floor(rect.width));const H=Math.max(240,Math.floor(rect.height));const rw=Math.floor(W*dpr),rh=Math.floor(H*dpr);if(c.width!==rw||c.height!==rh){c.width=rw;c.height=rh;}const x=c.getContext('2d');x.setTransform(dpr,0,0,dpr,0,0);return{x,W,H};}
function clampDepthView(minP,maxP){if(!depthState||!(depthState.fullMax>depthState.fullMin))return[minP,maxP];const fullMin=depthState.fullMin,fullMax=depthState.fullMax,fullSpan=Math.max(1e-6,fullMax-fullMin);const minSpan=Math.max(2,fullSpan*0.05);let span=Math.max(minSpan,maxP-minP);span=Math.min(fullSpan,span);let vMin=minP,vMax=vMin+span;if(vMin<fullMin){vMin=fullMin;vMax=vMin+span;}if(vMax>fullMax){vMax=fullMax;vMin=vMax-span;}return[vMin,vMax];}
function initDepthView(allPrices,cur){const rawMin=Math.min(...allPrices,cur),rawMax=Math.max(...allPrices,cur);const rawSpan=Math.max(2,rawMax-rawMin);const pad=Math.max(1,rawSpan*0.06);const dataMin=rawMin-pad,dataMax=rawMax+pad;if(!depthState||!(depthState.fullMax>depthState.fullMin)){depthState={fullMin:dataMin,fullMax:dataMax,viewMin:dataMin,viewMax:dataMax,padL:64,padR:24,padT:20,padB:44,W:0,H:0};[depthState.viewMin,depthState.viewMax]=clampDepthView(depthState.viewMin,depthState.viewMax);return;}depthState.fullMin=dataMin;depthState.fullMax=dataMax;const span=(depthState.viewMax>depthState.viewMin)?(depthState.viewMax-depthState.viewMin):(dataMax-dataMin);let vMin=cur-span/2,vMax=cur+span/2;[vMin,vMax]=clampDepthView(vMin,vMax);depthState.viewMin=vMin;depthState.viewMax=vMax;}
function drawDepth(){const c=document.getElementById('depthChart');const v=syncDepthCanvas(c),x=v.x,W=v.W,H=v.H;x.clearRect(0,0,W,H);x.fillStyle='#fff';x.fillRect(0,0,W,H);const m=orderbook&&orderbook.merged;const bids=(m&&m.bids)||[];const asks=(m&&m.asks)||[];const cur=Number((m&&m.mid)||0)||Number((dashboard&&dashboard.current_price)||0);if(!(cur>0)||(!bids.length&&!asks.length)){x.fillStyle='#6b7280';x.font='14px sans-serif';x.fillText('鏆傛棤鐩樺彛鏁版嵁',20,24);return;}const all=bids.concat(asks).map(v=>({p:Number(v.price||0),n:Number(v.qty||0)*Number(v.price||0)})).filter(v=>v.p>0&&v.n>0);if(!all.length){x.fillStyle='#6b7280';x.font='14px sans-serif';x.fillText('鏆傛棤鐩樺彛鏁版嵁',20,24);return;}initDepthView(all.map(v=>v.p),cur);const padL=64,padR=24,padT=20,padB=44,pw=W-padL-padR,ph=H-padT-padB,by=padT+ph;depthState.padL=padL;depthState.padR=padR;depthState.padT=padT;depthState.padB=padB;depthState.W=W;depthState.H=H;const minP=depthState.viewMin,maxP=depthState.viewMax,span=Math.max(1e-6,maxP-minP);const bidNow=levelMap(bids),askNow=levelMap(asks);const visible=all.filter(v=>v.p>=minP&&v.p<=maxP);const ghostVals=[];for(const [k,vv] of Object.entries(depthMemory.bidGhost)){const p=Number(k);if(p>=minP&&p<=maxP&&vv>0)ghostVals.push(vv);}for(const [k,vv] of Object.entries(depthMemory.askGhost)){const p=Number(k);if(p>=minP&&p<=maxP&&vv>0)ghostVals.push(vv);}const maxVals=[];for(const [k,o] of Object.entries(depthMemory.bidMax)){const p=Number(k);if(p>=minP&&p<=maxP&&o&&o.v>0)maxVals.push(o.v);}for(const [k,o] of Object.entries(depthMemory.askMax)){const p=Number(k);if(p>=minP&&p<=maxP&&o&&o.v>0)maxVals.push(o.v);}const maxN=Math.max(1,...(visible.length?visible.map(v=>v.n):all.map(v=>v.n)),...(ghostVals.length?ghostVals:[1]),...(maxVals.length?maxVals:[1]));const sx=v=>padL+((v-minP)/span)*pw,sy=v=>by-(v/maxN)*ph;x.strokeStyle='#e5e7eb';x.lineWidth=1;x.font='12px sans-serif';for(let i=0;i<=4;i++){const y=padT+ph*(i/4),val=maxN*(1-i/4);x.beginPath();x.moveTo(padL,y);x.lineTo(W-padR,y);x.stroke();x.fillStyle='#475569';x.fillText(fmtAmount(val),6,y+4);}const minLabelGap=12;const approxLabelW=Math.max(36,x.measureText(fmtPrice(minP)).width,x.measureText(fmtPrice(maxP)).width);const maxTicks=Math.max(2,Math.floor(pw/(approxLabelW+minLabelGap)));const tickCount=Math.max(2,Math.min(10,maxTicks));for(let i=0;i<=tickCount;i++){const p=minP+span*(i/tickCount),px=sx(p);x.strokeStyle='#e5e7eb';x.beginPath();x.moveTo(px,by);x.lineTo(px,by+4);x.stroke();const label=fmtPrice(p);const lw=x.measureText(label).width;const tx=Math.max(padL,Math.min(px-lw/2,W-padR-lw));x.fillStyle='#64748b';x.fillText(label,tx,by+18);}if(cur>=minP&&cur<=maxP){const cp=sx(cur);x.strokeStyle='#dc2626';x.setLineDash([6,4]);x.beginPath();x.moveTo(cp,padT);x.lineTo(cp,by);x.stroke();x.setLineDash([]);x.fillStyle='#111827';x.fillText('瑜版挸澧犳禒?'+fmtPrice(cur),Math.max(padL,Math.min(cp-34,W-150)),padT-4);}const barCount=Math.max(1,visible.length);const barW=Math.max(1,Math.min(12,pw/Math.max(50,barCount)));for(const [k,n] of Object.entries(depthMemory.bidGhost)){const p=Number(k);if(!(p>=minP&&p<=maxP&&n>0&&p<=cur))continue;const px=sx(p),y=sy(n);x.fillStyle='rgba(22,163,74,0.18)';x.fillRect(px-barW/2,y,barW,by-y);}for(const [k,n] of Object.entries(depthMemory.askGhost)){const p=Number(k);if(!(p>=minP&&p<=maxP&&n>0&&p>=cur))continue;const px=sx(p),y=sy(n);x.fillStyle='rgba(220,38,38,0.16)';x.fillRect(px-barW/2,y,barW,by-y);}for(const [k,o] of Object.entries(depthMemory.bidMax)){const p=Number(k),n=Number(o&&o.v||0);if(!(p>=minP&&p<=maxP&&n>0&&p<=cur))continue;const px=sx(p),y=sy(n);x.strokeStyle='rgba(22,163,74,0.85)';x.lineWidth=1.3;x.beginPath();x.moveTo(px-barW/2,y);x.lineTo(px+barW/2,y);x.stroke();}for(const [k,o] of Object.entries(depthMemory.askMax)){const p=Number(k),n=Number(o&&o.v||0);if(!(p>=minP&&p<=maxP&&n>0&&p>=cur))continue;const px=sx(p),y=sy(n);x.strokeStyle='rgba(220,38,38,0.85)';x.lineWidth=1.3;x.beginPath();x.moveTo(px-barW/2,y);x.lineTo(px+barW/2,y);x.stroke();}x.lineWidth=1;
const exColors={binance:'rgba(139,92,246,0.75)',okx:'rgba(234,179,8,0.72)',bybit:'rgba(103,232,249,0.72)'};
if(currentMode==='weighted'&&m&&m.per_exchange){const order=['binance','okx','bybit'];for(const side of ['bids','asks']){for(const ex of order){const ls=((m.per_exchange[ex]||{})[side])||[];for(const lv of ls){const p=Number(lv.price||0),n=Number(lv.qty||0)*p;if(!(p>=minP&&p<=maxP&&n>0&&((side==='bids'&&p<=cur)||(side==='asks'&&p>=cur))))continue;const px=sx(p),y=sy(n);x.fillStyle=exColors[ex];x.fillRect(px-barW/2,y,barW,by-y);}}}}
else {for(const b of bids){const p=Number(b.price||0),n=Number(b.qty||0)*p;if(!(p>=minP&&p<=maxP&&n>0&&p<=cur))continue;const px=sx(p),y=sy(n);x.fillStyle='rgba(22,163,74,0.75)';x.fillRect(px-barW/2,y,barW,by-y);}for(const a of asks){const p=Number(a.price||0),n=Number(a.qty||0)*p;if(!(p>=minP&&p<=maxP&&n>0&&p>=cur))continue;const px=sx(p),y=sy(n);x.fillStyle='rgba(220,38,38,0.72)';x.fillRect(px-barW/2,y,barW,by-y);}}
x.fillStyle='#16a34a';x.fillText('Bid 娣卞害',padL,14);x.fillStyle='#dc2626';x.fillText('Ask 娣卞害',padL+100,14);x.fillStyle='#64748b';x.fillText('閸楀﹨鈥滈張?'+Math.round(depthMemory.halfLifeMs/1000)+'s  婊氬姩绐楀彛 '+Math.round(depthMemory.rollingWindowMs/60000)+' 鍒嗛挓',padL+220,14);x.fillText('X 鏉炵繝鐜弽?/ Y 鏉炲瓨瀵曢崡鏇㈠櫨妫?,W-190,14);}
function bindDepthInteraction(){const c=document.getElementById('depthChart');if(!c||c.dataset.bound==='1')return;c.dataset.bound='1';c.addEventListener('wheel',e=>{if(!depthState||!(depthState.viewMax>depthState.viewMin))return;e.preventDefault();const rect=c.getBoundingClientRect();const xPos=e.clientX-rect.left;const s=depthState,pw=s.W-s.padL-s.padR;if(pw<=0)return;const ratio=Math.max(0,Math.min(1,(xPos-s.padL)/pw));const focus=s.viewMin+(s.viewMax-s.viewMin)*ratio;const factor=e.deltaY<0?0.88:1.14;let vMin=focus-(focus-s.viewMin)*factor,vMax=vMin+(s.viewMax-s.viewMin)*factor;[vMin,vMax]=clampDepthView(vMin,vMax);s.viewMin=vMin;s.viewMax=vMax;drawDepth();},{passive:false});c.addEventListener('mousedown',e=>{if(e.button!==0||!depthState)return;isDraggingDepth=true;depthLastX=e.clientX;});window.addEventListener('mousemove',e=>{if(!isDraggingDepth||!depthState)return;const s=depthState,pw=s.W-s.padL-s.padR;if(pw<=0)return;const dx=e.clientX-depthLastX;depthLastX=e.clientX;const dp=(dx/pw)*(s.viewMax-s.viewMin);let vMin=s.viewMin-dp,vMax=s.viewMax-dp;[vMin,vMax]=clampDepthView(vMin,vMax);s.viewMin=vMin;s.viewMax=vMax;drawDepth();});window.addEventListener('mouseup',()=>{isDraggingDepth=false;});window.addEventListener('mouseleave',()=>{isDraggingDepth=false;});window.addEventListener('resize',()=>drawDepth());}
function buildShares(states){const order=['binance','okx','bybit'];const out={};let total=0;for(const ex of order){const s=(states||[]).find(v=>(v.exchange||'').toLowerCase()===ex);const oi=Number((s&&s.oi_value_usd)||0);out[ex]=oi;total+=oi;}if(total<=0){for(const ex of order)out[ex]=1/order.length;}else{for(const ex of order)out[ex]/=total;}return out;}
function exchangeWsHealthy(ex){const ob=orderbook&&orderbook[ex];if(!ob)return false;const now=Date.now();const last=Math.max(Number(ob.last_ws_event||0),Number(ob.last_snapshot||0),Number(ob.updated_ts||0));if(!(last>0))return false;return (now-last)<=20000;}
function renderWsStatus(){const el=document.getElementById('wsStatus');if(!el)return;const show=(name,ex)=>name+' '+(exchangeWsHealthy(ex)?'瀹歌尪绻涢幒?:'閺堫亣绻涢幒?);el.textContent=[show('Binance','binance'),show('OKX','okx'),show('Bybit','bybit')].join('  ');}
function renderMeta(){if(!dashboard)return;const t=new Date(dashboard.generated_at).toLocaleString();document.getElementById('meta').textContent='瑕嗙洊 Binance / Bybit / OKX | 鏇存柊鏃堕棿 '+t+' | 褰撳墠妯″紡 '+currentMode;const s=buildShares(dashboard.states);document.getElementById('weights').textContent=currentMode==='weighted'?('OI 鏉冮噸 Binance '+(s.binance*100).toFixed(1)+'% | Bybit '+(s.bybit*100).toFixed(1)+'% | OKX '+(s.okx*100).toFixed(1)+'%'):'閸氬牆鑻熷Ο鈥崇础娑撳娲块幒銉ㄤ粵閸氬牆顦挎禍銈嗘閹碘偓閻╂ê褰?;}
async function load(){const [r1,r2]=await Promise.all([fetch('/api/dashboard'),fetch('/api/orderbook?limit=60&mode='+encodeURIComponent(currentMode))]);dashboard=await r1.json();orderbook=await r2.json();updateDepthMemory();await Promise.all([loadPriceEvents('bid',1),loadPriceEvents('ask',1)]);renderActive();renderMeta();renderWsStatus();renderMergedPanel();drawDepth();renderEventTables();}
async function openUpgradeModal(){const m=document.getElementById('upgradeModal'),logEl=document.getElementById('upgradeLog'),foot=document.getElementById('upgradeFoot');if(!m||!logEl||!foot)return;m.classList.add('show');logEl.textContent='';foot.textContent='姝ｅ湪瑙﹀彂鍗囩骇...';const r=await fetch('/api/upgrade/pull',{method:'POST'});const d=await r.json().catch(()=>({error:'response parse failed',output:''}));if(d.error){logEl.textContent=String(d.output||'');foot.textContent='瑙﹀彂澶辫触: '+d.error;return;}foot.textContent='宸茶Е鍙戯紝姝ｅ湪鎵ц...';let stable=0;for(let i=0;i<180;i++){await new Promise(res=>setTimeout(res,1000));const pr=await fetch('/api/upgrade/progress').then(x=>x.json()).catch(()=>null);if(!pr)continue;logEl.textContent=String(pr.log||'');logEl.scrollTop=logEl.scrollHeight;if(pr.done){foot.textContent=(String(pr.exit_code||'')==='0')?'鍗囩骇瀹屾垚骞跺凡閲嶅惎':'閸楀洨楠囩€瑰本鍨氶敍宀勨偓鈧崙铏圭垳 '+String(pr.exit_code||'?');return;}if(!pr.running)stable++;else stable=0;if(stable>=3){foot.textContent='閸楀洨楠囨潻娑氣柤瀹歌尙绮ㄩ弶鐕傜礄閻樿埖鈧焦婀惌銉礆閿涘矁顕Λ鈧弻銉︽）韫?;return;}}foot.textContent='閸楀洨楠囨禒宥呮躬鏉╂稖顢戦敍宀冾嚞缁嬪秴鎮楅崘宥囨箙';}
function closeUpgradeModal(){const m=document.getElementById('upgradeModal');if(m)m.classList.remove('show');}
async function doUpgrade(event){if(event)event.preventDefault();openUpgradeModal();return false;}
document.querySelectorAll('button[data-mode]').forEach(b=>b.onclick=()=>setMode((b.dataset.mode||'weighted')));
loadModeFromStorage();
loadTuneFromStorage();
syncTuneUI();
const hs=document.getElementById('halfLifeSel'),rw=document.getElementById('rollWinSel');
if(hs)hs.onchange=applyTuneFromUI;
if(rw)rw.onchange=applyTuneFromUI;
bindDepthInteraction();
initTheme();setInterval(load,5000);load();(async()=>{try{const r=await fetch('/api/version');const v=await r.json();const el=document.getElementById('globalFooter');if(el)el.textContent='Code by Yuhao@jiansutech.com - '+(v.commit_time||'-')+' - '+(v.commit_id||'-')+' - '+(v.branch||'-');}catch(_){const el=document.getElementById('globalFooter');if(el)el.textContent='Code by Yuhao@jiansutech.com - - - -';}})();
</script><div id="upgradeModal" class="upgrade-modal"><div class="upgrade-card"><div class="upgrade-head"><div class="upgrade-title">鍗囩骇杩囩▼</div><button class="upgrade-close" onclick="closeUpgradeModal()">鍏抽棴</button></div><pre id="upgradeLog" class="upgrade-log"></pre><div id="upgradeFoot" class="upgrade-foot">绛夊緟寮€濮?..</div></div></div><div id="globalFooter" class="footer">Code by Yuhao@jiansutech.com - loading - loading - loading</div></body></html>`

const channelHTML = `<!doctype html>
<html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>&#28040;&#24687;&#36890;&#36947;</title>
<style>body{margin:0;background:#f5f7fb;color:#1f2937;font-family:Inter,system-ui,Segoe UI,Arial,sans-serif}.nav{height:56px;background:#0b1220;border-bottom:1px solid #243145;display:flex;align-items:center;justify-content:space-between;padding:0 20px;position:sticky;top:0;z-index:10}.nav-left,.nav-right{display:flex;align-items:center;gap:20px}.brand{font-size:18px;font-weight:700;color:#eef3f9}.menu a{color:#d6deea;text-decoration:none;font-size:16px;margin-right:18px}.menu a.active{color:#fff;font-weight:700}.upgrade{color:#fff;font-weight:700;text-decoration:none}.theme-toggle{display:inline-flex;align-items:center;gap:6px;font-size:13px}.theme-toggle button{height:30px;padding:0 10px;border-radius:999px;border:1px solid rgba(148,163,184,0.45);background:transparent;color:#e5e7eb;cursor:pointer}.theme-toggle button.label{cursor:default;opacity:.92}.theme-toggle button.active{background:rgba(255,255,255,0.12);border-color:rgba(255,255,255,0.18);color:#fff}.wrap{max-width:900px;margin:0 auto;padding:22px}.panel{border:1px solid #dce3ec;background:#fff;margin:14px 0;padding:16px;border-radius:10px;box-shadow:0 1px 2px rgba(15,23,42,.04)}.small{font-size:12px;color:#6b7280}button.primary{background:#22c55e;color:#fff;border:0;padding:10px 16px;border-radius:8px;cursor:pointer}button.secondary{background:#fff;color:#111827;border:1px solid #cbd5e1;padding:10px 16px;border-radius:8px;cursor:pointer}input{width:100%;box-sizing:border-box;padding:10px;border:1px solid #cbd5e1;border-radius:8px;background:#fff;color:#111827;margin-top:6px}.switch-row{display:flex;align-items:center;justify-content:space-between;gap:14px;margin-top:14px;padding:12px;border:1px solid #dce3ec;border-radius:8px}.switch-row input{width:auto;margin:0;transform:scale(1.2)}.row{display:flex;gap:10px;flex-wrap:wrap;align-items:center}[data-theme="dark"] body{background:#000;color:#e5e7eb}[data-theme="dark"] .nav{background:#000;border-bottom-color:#111827}[data-theme="dark"] .panel{background:#000;border-color:#1f2937}[data-theme="dark"] .small{color:#94a3b8}[data-theme="dark"] input,[data-theme="dark"] button.secondary{background:#000;color:#e5e7eb;border-color:#334155}[data-theme="dark"] .switch-row{border-color:#1f2937}.upgrade-modal{position:fixed;inset:0;background:rgba(2,6,23,.55);display:none;align-items:center;justify-content:center;z-index:9999}.upgrade-modal.show{display:flex}.upgrade-card{width:min(880px,92vw);max-height:82vh;background:#0b1220;color:#e2e8f0;border:1px solid #334155;border-radius:10px;box-shadow:0 10px 30px rgba(2,6,23,.45);overflow:hidden}.upgrade-head{display:flex;align-items:center;justify-content:space-between;padding:10px 12px;border-bottom:1px solid #334155}.upgrade-title{font-size:14px;font-weight:700}.upgrade-close{background:transparent;border:1px solid #475569;color:#e2e8f0;border-radius:6px;padding:4px 8px;cursor:pointer}.upgrade-log{margin:0;padding:12px;white-space:pre-wrap;overflow:auto;max-height:62vh;font-family:ui-monospace,SFMono-Regular,Menlo,Consolas,monospace;font-size:12px;line-height:1.45}.upgrade-foot{padding:8px 12px;border-top:1px solid #334155;font-size:12px;color:#94a3b8}.footer{margin:18px auto 0 auto;max-width:1200px;padding:10px 12px;font-size:12px;color:#64748b;text-align:center}</style></head>
<body><div class="nav"><div class="nav-left"><div class="brand">ETH Liquidation Map</div><div class="menu"><a href="/">娓呯畻鐑尯</a><a href="/config">妯″瀷閰嶇疆</a><a href="/monitor">闆峰尯鐩戞帶</a><a href="/map">鐩樺彛姹囨€?/a><a href="/liquidations">寮哄钩娓呯畻</a><a href="/bubbles">姘旀场鍥?/a><a href="/webdatasource">椤甸潰鏁版嵁婧?/a><a href="/channel" class="active">娑堟伅閫氶亾</a><a href="/analysis">鏃ュ唴鍒嗘瀽</a></div></div><div class="nav-right"><div class="theme-toggle"><button class="label" type="button">涓婚</button><button id="themeDark" onclick="setTheme('dark')">娣辫壊</button><button id="themeLight" onclick="setTheme('light')">娴呰壊</button></div><a href="#" class="upgrade" onclick="return doUpgrade(event)">&#21319;&#32423;</a></div></div>
<div class="wrap"><div class="panel"><h2 style="margin-top:0">Telegram 娑堟伅閫氶亾</h2><div class="small">闁板秶鐤?Telegram 閺堝搫娅掓禍杞扮瑢妫版垿浜鹃敍宀€閮寸紒鐔风殺閹稿顔曠€规岸妫块梾鏂垮絺闁線鈧氨鐓￠妴?/div><label class="switch-row"><span><strong>閸氼垳鏁ら懛顏勫З閹恒劑鈧?/strong><div class="small">姒涙顓婚崗鎶芥４閿涙稑绱戦崥顖氭倵閹稿鈧氨鐓￠梻鎾閹恒劑鈧焦绔荤粻妤冨劰閸栨椽鈧喐濮ら妴?/div></span><input id="notify-enabled" type="checkbox"></label><div style="margin-top:14px"><label>Telegram Bot Token</label><input id="token" autocomplete="off" placeholder="123456:ABC..."></div><div style="margin-top:14px"><label>Telegram Channel / Chat ID</label><input id="channel" autocomplete="off" placeholder="@mychannel 閹?-100123456789"></div><div style="margin-top:14px"><label>閫氱煡闂撮殧锛堝垎閽燂級</label><input id="notify-interval" type="number" min="1" step="1" placeholder="15"></div><div style="margin-top:16px" class="row"><button class="primary" onclick="save()">淇濆瓨</button><button class="secondary" onclick="testTelegram()">濞村鐦崣鎴︹偓?/button><span id="msg" class="small" style="margin-left:10px"></span></div></div></div>
<script>
let rawToken={{printf "%q" .TelegramBotToken}},rawChannel={{printf "%q" .TelegramChannel}},rawInterval={{.NotifyIntervalMin}},rawEnabled={{.NotifyEnabled}},tokenDirty=false,channelDirty=false;
function setTheme(t){const theme=(t==='dark')?'dark':'light';document.documentElement.setAttribute('data-theme',theme);try{localStorage.setItem('theme',theme);}catch(_){}const bd=document.getElementById('themeDark'),bl=document.getElementById('themeLight');if(bd)bd.classList.toggle('active',theme==='dark');if(bl)bl.classList.toggle('active',theme==='light');}
function initTheme(){let t='light';try{t=localStorage.getItem('theme')||'light';}catch(_){}setTheme(t);}
function maskInput(v){v=String(v||'');if(!v)return '';if(v.length<=8)return '*'.repeat(v.length);return v.slice(0,4)+'*'.repeat(v.length-8)+v.slice(-4);}function syncInputs(){const t=document.getElementById('token'),c=document.getElementById('channel'),n=document.getElementById('notify-interval'),e=document.getElementById('notify-enabled');if(!tokenDirty)t.value=maskInput(rawToken);if(!channelDirty)c.value=maskInput(rawChannel);n.value=rawInterval||15;e.checked=!!rawEnabled;}function currentValue(i,raw,dirty){return dirty?(i?String(i.value||''):''):raw;}document.getElementById('token').addEventListener('input',()=>{tokenDirty=true});document.getElementById('channel').addEventListener('input',()=>{channelDirty=true});syncInputs();initTheme();
async function loadFooter(){try{const r=await fetch('/api/version');const v=await r.json();const el=document.getElementById('globalFooter');if(el)el.textContent='Code by Yuhao@jiansutech.com - '+(v.commit_time||'-')+' - '+(v.commit_id||'-')+' - '+(v.branch||'-');}catch(_){const el=document.getElementById('globalFooter');if(el)el.textContent='Code by Yuhao@jiansutech.com - - - -';}}async function openUpgradeModal(){const m=document.getElementById('upgradeModal'),logEl=document.getElementById('upgradeLog'),foot=document.getElementById('upgradeFoot');if(!m||!logEl||!foot)return;m.classList.add('show');logEl.textContent='';foot.textContent='姝ｅ湪瑙﹀彂鍗囩骇...';const r=await fetch('/api/upgrade/pull',{method:'POST'});const d=await r.json().catch(()=>({error:'response parse failed',output:''}));if(d.error){logEl.textContent=String(d.output||'');foot.textContent='瑙﹀彂澶辫触: '+d.error;return;}foot.textContent='宸茶Е鍙戯紝姝ｅ湪鎵ц...';let stable=0;for(let i=0;i<180;i++){await new Promise(res=>setTimeout(res,1000));const pr=await fetch('/api/upgrade/progress').then(x=>x.json()).catch(()=>null);if(!pr)continue;logEl.textContent=String(pr.log||'');logEl.scrollTop=logEl.scrollHeight;if(pr.done){foot.textContent=(String(pr.exit_code||'')==='0')?'鍗囩骇瀹屾垚骞跺凡閲嶅惎':'閸楀洨楠囩€瑰本鍨氶敍宀勨偓鈧崙铏圭垳 '+String(pr.exit_code||'?');return;}if(!pr.running)stable++;else stable=0;if(stable>=3){foot.textContent='閸楀洨楠囨潻娑氣柤瀹歌尙绮ㄩ弶鐕傜礄閻樿埖鈧焦婀惌銉礆閿涘矁顕Λ鈧弻銉︽）韫?;return;}}foot.textContent='閸楀洨楠囨禒宥呮躬鏉╂稖顢戦敍宀冾嚞缁嬪秴鎮楅崘宥囨箙';}
function closeUpgradeModal(){const m=document.getElementById('upgradeModal');if(m)m.classList.remove('show');}
async function doUpgrade(event){if(event)event.preventDefault();openUpgradeModal();return false;}
async function save(){const n=document.getElementById('notify-interval');const iv=Math.max(1,Number((n&&n.value)||rawInterval||15)|0);const body={telegram_bot_token:currentValue(document.getElementById('token'),rawToken,tokenDirty),telegram_channel:currentValue(document.getElementById('channel'),rawChannel,channelDirty),notify_interval_min:iv,notify_enabled:document.getElementById('notify-enabled').checked};const r=await fetch('/api/settings',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify(body)});if(r.ok){rawToken=body.telegram_bot_token;rawChannel=body.telegram_channel;rawInterval=iv;rawEnabled=body.notify_enabled;tokenDirty=false;channelDirty=false;syncInputs();document.getElementById('msg').textContent='淇濆瓨鎴愬姛';}else{document.getElementById('msg').textContent='淇濆瓨澶辫触';}}
async function testTelegram(){const msg=document.getElementById('msg');msg.textContent='濮濓絽婀崣鎴︹偓浣圭ゴ鐠囨洘绉烽幁?..';const r=await fetch('/api/channel/test',{method:'POST'});msg.textContent=r.ok?'濞村鐦崣鎴︹偓浣瑰灇閸?:('濞村鐦崣鎴︹偓浣搞亼鐠? '+await r.text());}
</script><div id="upgradeModal" class="upgrade-modal"><div class="upgrade-card"><div class="upgrade-head"><div class="upgrade-title">鍗囩骇杩囩▼</div><button class="upgrade-close" onclick="closeUpgradeModal()">鍏抽棴</button></div><pre id="upgradeLog" class="upgrade-log"></pre><div id="upgradeFoot" class="upgrade-foot">绛夊緟寮€濮?..</div></div></div><div id="globalFooter" class="footer">Code by Yuhao@jiansutech.com - loading - loading - loading</div></body></html>`

const channelHTMLV2 = `<!doctype html>
<html lang="zh-CN">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>娑堟伅閫氶亾</title>
<style>
body{margin:0;background:#f5f7fb;color:#1f2937;font-family:Inter,system-ui,Segoe UI,Arial,sans-serif}
.nav{height:56px;background:#0b1220;border-bottom:1px solid #243145;display:flex;align-items:center;justify-content:space-between;padding:0 20px;position:sticky;top:0;z-index:10}
.nav-left,.nav-right{display:flex;align-items:center;gap:20px}
.brand{font-size:18px;font-weight:700;color:#eef3f9}
.menu a{color:#d6deea;text-decoration:none;font-size:16px;margin-right:18px}
.menu a.active{color:#fff;font-weight:700}
.upgrade{color:#fff;font-weight:700;text-decoration:none}
.theme-toggle{display:inline-flex;align-items:center;gap:6px;font-size:13px}
.theme-toggle button{height:30px;padding:0 10px;border-radius:999px;border:1px solid rgba(148,163,184,0.45);background:transparent;color:#e5e7eb;cursor:pointer}
.theme-toggle button.label{cursor:default;opacity:.92}
.theme-toggle button.active{background:rgba(255,255,255,0.12);border-color:rgba(255,255,255,0.18);color:#fff}
.wrap{max-width:1584px;margin:0 auto;padding:22px}
.channel-layout{display:grid;grid-template-columns:minmax(0,1fr) minmax(0,1fr);gap:14px;align-items:start}
.panel{border:1px solid #dce3ec;background:#fff;padding:16px;border-radius:10px;box-shadow:0 1px 2px rgba(15,23,42,.04);min-width:0}
.small{font-size:12px;color:#6b7280;line-height:1.6}
.grid{display:grid;grid-template-columns:1fr 1fr;gap:12px}
.field label{display:block;font-size:12px;color:#6b7280}
.field input,.field textarea{width:100%;box-sizing:border-box;padding:10px;border:1px solid #cbd5e1;border-radius:8px;background:#fff;color:#111827;margin-top:6px}
.field textarea{min-height:110px;resize:vertical}
.switch-row{display:flex;align-items:center;justify-content:space-between;gap:14px;margin-top:14px;padding:12px;border:1px solid #dce3ec;border-radius:8px}
.switch-row input{width:auto;margin:0;transform:scale(1.2)}
.row{display:flex;gap:10px;flex-wrap:wrap;align-items:center}
.hint{margin-top:8px;padding:10px 12px;border-radius:8px;background:#f8fafc;border:1px solid #e2e8f0}
.mono{font-family:ui-monospace,SFMono-Regular,Menlo,Consolas,monospace}
.status{min-height:20px}
.table-scroll{width:100%;overflow-x:auto;margin-top:10px}
.table-scroll-fit{overflow-x:hidden}
.history-table{width:100%;border-collapse:collapse;table-layout:fixed}
.history-table-wide{width:100%;min-width:100%}
.history-table th,.history-table td{padding:8px 10px;border-top:1px solid #e5e7eb;text-align:left;font-size:12px;vertical-align:top;word-break:break-word}
.history-table thead th{border-top:0;color:#64748b}
.ok{color:#15803d;font-weight:700}
.fail{color:#dc2626;font-weight:700}
@media (max-width:960px){.channel-layout,.grid{grid-template-columns:1fr}}
[data-theme="dark"] body{background:#000;color:#e5e7eb}
[data-theme="dark"] .nav{background:#000;border-bottom-color:#111827}
[data-theme="dark"] .panel{background:#000;border-color:#1f2937}
[data-theme="dark"] .small,[data-theme="dark"] .field label,[data-theme="dark"] .history-table thead th{color:#94a3b8}
[data-theme="dark"] .field input,[data-theme="dark"] .field textarea,[data-theme="dark"] button.secondary{background:#000;color:#e5e7eb;border-color:#334155}
[data-theme="dark"] .switch-row,[data-theme="dark"] .hint{border-color:#1f2937;background:#020617}
[data-theme="dark"] .history-table th,[data-theme="dark"] .history-table td{border-top-color:#1f2937}
.upgrade-modal{position:fixed;inset:0;background:rgba(2,6,23,.55);display:none;align-items:center;justify-content:center;z-index:9999}
.upgrade-modal.show{display:flex}
.upgrade-card{width:min(880px,92vw);max-height:82vh;background:#0b1220;color:#e2e8f0;border:1px solid #334155;border-radius:10px;box-shadow:0 10px 30px rgba(2,6,23,.45);overflow:hidden}
.upgrade-head{display:flex;align-items:center;justify-content:space-between;padding:10px 12px;border-bottom:1px solid #334155}
.upgrade-title{font-size:14px;font-weight:700}
.upgrade-close{background:transparent;border:1px solid #475569;color:#e2e8f0;border-radius:6px;padding:4px 8px;cursor:pointer}
.upgrade-log{margin:0;padding:12px;white-space:pre-wrap;overflow:auto;max-height:62vh;font-family:ui-monospace,SFMono-Regular,Menlo,Consolas,monospace;font-size:12px;line-height:1.45}
.upgrade-foot{padding:8px 12px;border-top:1px solid #334155;font-size:12px;color:#94a3b8}
.footer{margin:18px auto 0 auto;max-width:1200px;padding:10px 12px;font-size:12px;color:#64748b;text-align:center}
button.primary{background:#22c55e;color:#fff;border:0;padding:10px 16px;border-radius:8px;cursor:pointer}
button.secondary{background:#fff;color:#111827;border:1px solid #cbd5e1;padding:10px 16px;border-radius:8px;cursor:pointer}
</style>
</head>
<body>
<div class="nav">
  <div class="nav-left">
    <div class="brand">ETH Liquidation Map</div>
    <div class="menu">
      <a href="/">棣栭〉</a>
      <a href="/config">妯″瀷閰嶇疆</a>
      <a href="/monitor">闆峰尯鐩戞帶</a>
      <a href="/map">鐩樺彛姹囨€?/a>
      <a href="/liquidations">寮哄钩娓呯畻</a>
      <a href="/bubbles">姘旀场鍥?/a>
      <a href="/webdatasource">椤甸潰鏁版嵁婧?/a>
      <a href="/channel" class="active">娑堟伅閫氶亾</a>
      <a href="/analysis">鏃ュ唴鍒嗘瀽</a>
    </div>
  </div>
  <div class="nav-right">
    <div class="theme-toggle">
      <button class="label" type="button">涓婚</button>
      <button id="themeDark" onclick="setTheme('dark')">娣辫壊</button>
      <button id="themeLight" onclick="setTheme('light')">娴呰壊</button>
    </div>
    <a href="#" class="upgrade" onclick="return doUpgrade(event)">鍗囩骇</a>
  </div>
</div>

<div class="wrap">
  <div class="channel-layout">
    <div class="panel">
      <h2 style="margin-top:0">Telegram 娑堟伅閫氶亾</h2>
      <div class="small">閰嶇疆 Telegram Bot 鍜岄閬撳悗锛岀郴缁熶細鎸夊伐浣滄椂闂村拰闈炲伐浣滄椂闂寸殑涓嶅悓棰戠巼鑷姩鎺ㄩ€佹秷鎭€?/div>
      <label class="switch-row">
        <span>
          <strong>鍚敤鑷姩閫氱煡</strong>
          <div class="small">鍏抽棴鏃朵笉浼氳嚜鍔ㄦ帹閫侊紱寮€鍚悗浼氭寜涓嬫柟閰嶇疆鐨勬椂闂磋寖鍥村拰棰戠巼鎵ц銆?/div>
        </span>
        <input id="notify-enabled" type="checkbox">
      </label>
      <div class="grid" style="margin-top:14px">
        <div class="field" style="grid-column:1/-1">
          <label>Telegram Bot Token</label>
          <input id="token" autocomplete="off" placeholder="123456:ABC...">
          <div class="small">鐣欑┖浼氫繚鐣欏綋鍓嶅凡淇濆瓨鐨?Token銆?/div>
        </div>
        <div class="field" style="grid-column:1/-1">
          <label>Telegram Channel / Chat ID</label>
          <input id="channel" autocomplete="off" placeholder="@mychannel 鎴?-100123456789">
          <div class="small">鐣欑┖浼氫繚鐣欏綋鍓嶅凡淇濆瓨鐨勯閬撴垨 Chat ID銆?/div>
        </div>
        <div class="field">
          <label>宸ヤ綔鏃堕棿閫氱煡闂撮殧锛堝垎閽燂級</label>
          <input id="notify-work-interval" type="number" min="1" step="1" placeholder="15">
        </div>
        <div class="field">
          <label>闈炲伐浣滄椂闂撮€氱煡闂撮殧锛堝垎閽燂級</label>
          <input id="notify-off-interval" type="number" min="1" step="1" placeholder="60">
        </div>
        <div class="field" style="grid-column:1/-1">
          <label>宸ヤ綔鏃堕棿鑼冨洿 / 姝ｅ垯琛ㄨ揪寮?/label>
          <textarea id="work-time-expr" placeholder="09:00-12:00,13:00-18:00&#10;鎴?regex:^(Mon|Tue|Wed|Thu|Fri) (09|1[0-7]):[0-5][0-9]$"></textarea>
          <div class="hint small">鏀寔涓ょ鍐欐硶锛?span class="mono">09:00-18:00</span>銆?span class="mono">09:00-12:00,13:00-18:00</span>锛涗篃鏀寔鐩存帴杈撳叆姝ｅ垯锛屾帹鑽愬甫涓?<span class="mono">regex:</span> 鍓嶇紑銆傜暀绌鸿〃绀哄叏澶╂寜鈥滃伐浣滄椂闂撮€氱煡闂撮殧鈥濆鐞嗐€?/div>
        </div>
      </div>
      <div style="margin-top:16px" class="row">
        <button class="primary" onclick="save()">淇濆瓨</button>
        <button class="secondary" onclick="testTelegram()">鍙戦€佹祴璇曟秷鎭?/button>
        <span id="msg" class="small status" style="margin-left:10px"></span>
      </div>
    </div>

    <div class="panel">
      <div class="row" style="justify-content:space-between">
        <div>
          <h3 style="margin:0">鍘嗗彶鍙戦€佽褰?/h3>
          <div class="small">鏄剧ず鏈€杩?15 鏉″巻鍙插彂閫佽褰曘€?/div>
        </div>
        <button class="secondary" onclick="loadHistory()">鍒锋柊</button>
      </div>
      <div class="table-scroll table-scroll-fit">
        <table class="history-table history-table-wide">
          <colgroup>
            <col style="width:16%">
            <col style="width:10%">
            <col style="width:29%">
            <col style="width:10%">
            <col style="width:35%">
          </colgroup>
          <thead>
            <tr><th>鏃堕棿</th><th>绫诲瀷</th><th>缁勫埆</th><th>缁撴灉</th><th>璇︽儏</th></tr>
          </thead>
          <tbody id="historyBody"><tr><td colspan="5" class="small">鍔犺浇涓?..</td></tr></tbody>
        </table>
      </div>
    </div>

    <div class="panel">
      <div class="row" style="justify-content:space-between">
        <div>
          <h3 style="margin:0">鎶撳彇鍘嗗彶</h3>
          <div class="small">鏄剧ず鏈€杩?24 灏忔椂鐨?24 鏉℃姄鍙栬褰曘€?/div>
        </div>
        <button class="secondary" onclick="loadTimeline()">鍒锋柊</button>
      </div>
      <div class="table-scroll">
        <table class="history-table">
          <colgroup>
            <col style="width:18%">
            <col style="width:12%">
            <col style="width:12%">
            <col style="width:26%">
            <col style="width:12%">
            <col style="width:20%">
          </colgroup>
          <thead>
            <tr><th>鏃堕棿</th><th>鏉ユ簮</th><th>绫诲瀷</th><th>绐楀彛/鐩爣</th><th>鐘舵€?/th><th>璇︽儏</th></tr>
          </thead>
          <tbody id="timelineBody"><tr><td colspan="6" class="small">鍔犺浇涓?..</td></tr></tbody>
        </table>
      </div>
    </div>

    <div class="panel">
      <div class="row" style="justify-content:space-between">
        <div>
          <h3 style="margin:0">24灏忔椂鐨勯璁℃帹閫佹椂闂磋〃</h3>
          <div class="small">鍒楀嚭鏈潵 24 灏忔椂鍐呯殑鎶撳彇涓庢帹閫佽鍒掞紝鏂逛究鏌ョ湅鏇撮暱鐨勬湭鏉ュ畨鎺掋€?/div>
        </div>
        <button class="secondary" onclick="loadSchedule()">鍒锋柊</button>
      </div>
      <div class="table-scroll">
        <table class="history-table">
          <colgroup>
            <col style="width:18%">
            <col style="width:18%">
            <col style="width:12%">
            <col style="width:12%">
            <col style="width:40%">
          </colgroup>
          <thead>
            <tr><th>棰勮鎺ㄩ€佹椂闂?/th><th>棰勮鎶撳彇鏃堕棿</th><th>鏃舵</th><th>棰戠巼</th><th>璇存槑</th></tr>
          </thead>
          <tbody id="scheduleBody"><tr><td colspan="5" class="small">鍔犺浇涓?..</td></tr></tbody>
        </table>
      </div>
    </div>
  </div>
</div>

<script>
let rawToken={{printf "%q" .TelegramBotToken}},rawChannel={{printf "%q" .TelegramChannel}},rawInterval={{.NotifyIntervalMin}},rawWorkInterval={{.NotifyWorkIntervalMin}},rawOffInterval={{.NotifyOffIntervalMin}},rawWorkExpr={{printf "%q" .WorkTimeExpr}},rawEnabled={{.NotifyEnabled}},tokenDirty=false,channelDirty=false;
function setTheme(t){const theme=(t==='dark')?'dark':'light';document.documentElement.setAttribute('data-theme',theme);try{localStorage.setItem('theme',theme);}catch(_){}const bd=document.getElementById('themeDark'),bl=document.getElementById('themeLight');if(bd)bd.classList.toggle('active',theme==='dark');if(bl)bl.classList.toggle('active',theme==='light');}
function initTheme(){let t='light';try{t=localStorage.getItem('theme')||'light';}catch(_){}setTheme(t);}
function maskInput(v){v=String(v||'');if(!v)return '';if(v.length<=8)return '*'.repeat(v.length);return v.slice(0,4)+'*'.repeat(v.length-8)+v.slice(-4);}
function syncInputs(){const t=document.getElementById('token'),c=document.getElementById('channel'),w=document.getElementById('notify-work-interval'),o=document.getElementById('notify-off-interval'),x=document.getElementById('work-time-expr'),e=document.getElementById('notify-enabled');if(!tokenDirty){t.value='';t.placeholder=rawToken?maskInput(rawToken):'123456:ABC...';}if(!channelDirty){c.value='';c.placeholder=rawChannel?maskInput(rawChannel):'@mychannel 鎴?-100123456789';}w.value=rawWorkInterval||rawInterval||15;o.value=rawOffInterval||rawWorkInterval||rawInterval||15;x.value=rawWorkExpr||'';e.checked=!!rawEnabled;}
function currentValue(i,raw){const next=String((i&&i.value)||'').trim();return next!==''?next:raw;}
function intValue(id,fallback){const el=document.getElementById(id);const raw=String((el&&el.value)||'').trim();const v=Number(raw);return Number.isFinite(v)&&v>0?Math.floor(v):fallback;}
function fmtHistoryTime(ts){if(!ts)return '-';return new Date(ts).toLocaleString('zh-CN',{hour12:false});}
function esc(v){return String(v||'').replace(/[&<>"]/g,s=>({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;'}[s]));}
function sendModeLabel(v){if(v==='test')return '娴嬭瘯';if(v==='auto')return '鑷姩';if(v==='manual')return '鎵嬪姩';return String(v||'-');}
function sourceLabel(v){if(v==='channel')return '娑堟伅閫氶亾';if(v==='webdatasource')return '椤甸潰鏁版嵁婧?;return String(v||'-');}
function typeLabel(v){if(v==='push')return '鎺ㄩ€?;if(v==='capture')return '鎶撳彇';return String(v||'-');}
function periodLabel(v){if(v==='work-hours')return '宸ヤ綔鏃堕棿';if(v==='off-hours')return '闈炲伐浣滄椂闂?;return String(v||'-');}

document.getElementById('token').addEventListener('input',()=>{tokenDirty=true;});
document.getElementById('channel').addEventListener('input',()=>{channelDirty=true;});
syncInputs();
initTheme();

async function loadFooter(){try{const r=await fetch('/api/version');const v=await r.json();const el=document.getElementById('globalFooter');if(el)el.textContent='Code by Yuhao@jiansutech.com - '+(v.commit_time||'-')+' - '+(v.commit_id||'-')+' - '+(v.branch||'-');}catch(_){const el=document.getElementById('globalFooter');if(el)el.textContent='Code by Yuhao@jiansutech.com - - - -';}}
async function openUpgradeModal(){const m=document.getElementById('upgradeModal'),logEl=document.getElementById('upgradeLog'),foot=document.getElementById('upgradeFoot');if(!m||!logEl||!foot)return;m.classList.add('show');logEl.textContent='';foot.textContent='姝ｅ湪瑙﹀彂鍗囩骇...';const r=await fetch('/api/upgrade/pull',{method:'POST'});const d=await r.json().catch(()=>({error:'response parse failed',output:''}));if(d.error){logEl.textContent=String(d.output||'');foot.textContent='瑙﹀彂澶辫触: '+d.error;return;}foot.textContent='宸茶Е鍙戯紝姝ｅ湪鎵ц...';let stable=0;for(let i=0;i<180;i++){await new Promise(res=>setTimeout(res,1000));const pr=await fetch('/api/upgrade/progress').then(x=>x.json()).catch(()=>null);if(!pr)continue;logEl.textContent=String(pr.log||'');logEl.scrollTop=logEl.scrollHeight;if(pr.done){foot.textContent=(String(pr.exit_code||'')==='0')?'鍗囩骇瀹屾垚骞跺凡閲嶅惎':'鍗囩骇瀹屾垚锛岄€€鍑虹爜 '+String(pr.exit_code||'?');return;}if(!pr.running)stable++;else stable=0;if(stable>=3){foot.textContent='鍗囩骇杩涚▼宸茬粨鏉燂紝浣嗙姸鎬佹湭鐭ワ紝璇锋鏌ユ棩蹇?;return;}}foot.textContent='鍗囩骇浠嶅湪杩涜锛岃绋嶅悗鍐嶇湅';}
function closeUpgradeModal(){const m=document.getElementById('upgradeModal');if(m)m.classList.remove('show');}
async function doUpgrade(event){if(event)event.preventDefault();openUpgradeModal();return false;}

async function loadHistory(){const body=document.getElementById('historyBody');if(!body)return;const rows=await fetch('/api/channel/history?limit=15').then(r=>r.ok?r.json():[]).catch(()=>[]);if(!Array.isArray(rows)||!rows.length){body.innerHTML='<tr><td colspan="5" class="small">鏆傛棤鏈€杩戝巻鍙插彂閫佽褰?/td></tr>';return;}body.innerHTML=rows.map(it=>'<tr><td>'+fmtHistoryTime(it.sent_at)+'</td><td>'+sendModeLabel(it.send_mode)+'</td><td>绗?+Number(it.group_index||0)+'缁?/ '+esc(it.group_name||'-')+'</td><td class="'+(String(it.status||'').toLowerCase()==='success'?'ok':'fail')+'">'+(String(it.status||'').toLowerCase()==='success'?'鎴愬姛':'澶辫触')+'</td><td>'+esc(it.error_text||'-')+'</td></tr>').join('');}
async function loadTimeline(){const body=document.getElementById('timelineBody');if(!body)return;const rows=await fetch('/api/channel/timeline?hours=24&limit=24').then(r=>r.ok?r.json():[]).catch(()=>[]);if(!Array.isArray(rows)||!rows.length){body.innerHTML='<tr><td colspan="6" class="small">杩?24 灏忔椂鏆傛棤鎶撳彇璁板綍</td></tr>';return;}body.innerHTML=rows.map(it=>{const status=String(it.status||'').toLowerCase();const statusCls=status==='success'?'ok':(status==='failed'?'fail':'');const target=it.target||it.window||'-';const detail=[it.records?('璁板綍鏁?'+Number(it.records)):'',it.detail||''].filter(Boolean).join(' | ');return '<tr><td>'+fmtHistoryTime(it.ts)+'</td><td>'+esc(sourceLabel(it.source||'-'))+'</td><td>'+esc(typeLabel(it.type||'-'))+'</td><td>'+esc(target)+'</td><td class="'+statusCls+'">'+esc(it.status||'-')+'</td><td>'+esc(detail||'-')+'</td></tr>';}).join('');}
async function loadSchedule(){const body=document.getElementById('scheduleBody');if(!body)return;const rows=await fetch('/api/channel/schedule?hours=24').then(r=>r.ok?r.json():[]).catch(()=>[]);if(!Array.isArray(rows)||!rows.length){body.innerHTML='<tr><td colspan="5" class="small">'+(rawEnabled?'鏈潵 24 灏忔椂鏆傛棤棰勮鎺ㄩ€侀」鐩?:'鑷姩閫氱煡鏈紑鍚紝鏆傛棤鏈潵鎺ㄩ€佽鍒?)+'</td></tr>';return;}body.innerHTML=rows.map(it=>'<tr><td>'+fmtHistoryTime(it.push_ts)+'</td><td>'+fmtHistoryTime(it.capture_ts)+'</td><td>'+esc(periodLabel(it.period||'-'))+'</td><td>'+(Number(it.interval_min||0)>0?(Number(it.interval_min)+' 鍒嗛挓'):'-')+'</td><td>'+esc(it.detail||'-')+'</td></tr>').join('');}
async function save(){const msg=document.getElementById('msg');msg.textContent='姝ｅ湪淇濆瓨...';const workInterval=intValue('notify-work-interval',rawWorkInterval||rawInterval||15);const offInterval=intValue('notify-off-interval',rawOffInterval||workInterval);const body={telegram_bot_token:currentValue(document.getElementById('token'),rawToken),telegram_channel:currentValue(document.getElementById('channel'),rawChannel),notify_interval_min:workInterval,notify_work_interval_min:workInterval,notify_off_interval_min:offInterval,work_time_expr:String((document.getElementById('work-time-expr').value||'').trim()),notify_enabled:document.getElementById('notify-enabled').checked};const r=await fetch('/api/settings',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify(body)});if(r.ok){rawToken=body.telegram_bot_token;rawChannel=body.telegram_channel;rawInterval=workInterval;rawWorkInterval=workInterval;rawOffInterval=offInterval;rawWorkExpr=body.work_time_expr;rawEnabled=body.notify_enabled;tokenDirty=false;channelDirty=false;syncInputs();await loadSchedule();msg.textContent='淇濆瓨鎴愬姛';return;}msg.textContent='淇濆瓨澶辫触: '+await r.text();}
async function testTelegram(){const msg=document.getElementById('msg');msg.textContent='姝ｅ湪瑙﹀彂娴嬭瘯鍙戦€?..';const r=await fetch('/api/channel/test',{method:'POST'});const text=String(await r.text().catch(()=>'' )||'').trim();if(r.ok){msg.textContent=text==='already running'?'宸叉湁娴嬭瘯鍙戦€佽繘琛屼腑锛岃绋嶅悗鏌ョ湅涓嬫柟鍘嗗彶':'娴嬭瘯鍙戦€佸凡瑙﹀彂锛岀害 30-60 绉掑悗鏌ョ湅涓嬫柟鍘嗗彶';await loadHistory();setTimeout(loadHistory,5000);setTimeout(loadHistory,20000);return;}msg.textContent='娴嬭瘯鍙戦€佸け璐? '+(text||('HTTP '+r.status));}

window.addEventListener('load',async()=>{await loadFooter();await loadHistory();await loadTimeline();await loadSchedule();});
</script>
<div id="upgradeModal" class="upgrade-modal">
  <div class="upgrade-card">
    <div class="upgrade-head">
      <div class="upgrade-title">鍗囩骇杩囩▼</div>
      <button class="upgrade-close" onclick="closeUpgradeModal()">鍏抽棴</button>
    </div>
    <pre id="upgradeLog" class="upgrade-log"></pre>
    <div id="upgradeFoot" class="upgrade-foot">绛夊緟寮€濮?..</div>
  </div>
</div>
<div id="globalFooter" class="footer">Code by Yuhao@jiansutech.com - loading - loading - loading</div>
</body>
</html>`

const liquidationsHTML = `<!doctype html>
<html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>寮哄钩娓呯畻</title>
<style>body{margin:0;background:#f5f7fb;color:#1f2937;font-family:Inter,system-ui,Segoe UI,Arial,sans-serif}.nav{height:56px;background:#0b1220;border-bottom:1px solid #243145;display:flex;align-items:center;justify-content:space-between;padding:0 20px;position:sticky;top:0;z-index:10}.nav-left,.nav-right{display:flex;align-items:center;gap:20px}.brand{font-size:18px;font-weight:700;color:#eef3f9}.menu a{color:#d6deea;text-decoration:none;font-size:16px;margin-right:18px}.menu a.active{color:#fff;font-weight:700}.upgrade{color:#fff;font-weight:700;text-decoration:none}.theme-toggle{display:inline-flex;align-items:center;gap:6px;font-size:13px}.theme-toggle button{height:30px;padding:0 10px;border-radius:999px;border:1px solid rgba(148,163,184,0.45);background:transparent;color:#e5e7eb;cursor:pointer}.theme-toggle button.label{cursor:default;opacity:.92}.theme-toggle button.active{background:rgba(255,255,255,0.12);border-color:rgba(255,255,255,0.18);color:#fff}.wrap{max-width:1200px;margin:0 auto;padding:22px}.panel{border:1px solid #dce3ec;background:#fff;margin:14px 0;padding:16px;border-radius:10px;box-shadow:0 1px 2px rgba(15,23,42,.04)}table{width:100%;border-collapse:collapse}th,td{border-bottom:1px solid #e5e7eb;padding:8px 10px;text-align:center;font-size:13px}.row{display:flex;gap:8px;align-items:center}.btn{background:#fff;color:#111827;border:1px solid #cbd5e1;padding:8px 12px;border-radius:8px;cursor:pointer}.btn.primary{background:#22c55e;color:#fff;border-color:#22c55e}.small{font-size:12px;color:#64748b}[data-theme="dark"] body{background:#000;color:#e5e7eb}[data-theme="dark"] .nav{background:#000;border-bottom-color:#111827}[data-theme="dark"] .panel{background:#000;border-color:#1f2937}[data-theme="dark"] .small{color:#94a3b8}[data-theme="dark"] th,[data-theme="dark"] td{border-bottom-color:#1f2937}[data-theme="dark"] .btn{background:#000;color:#e5e7eb;border-color:#334155}.upgrade-modal{position:fixed;inset:0;background:rgba(2,6,23,.55);display:none;align-items:center;justify-content:center;z-index:9999}.upgrade-modal.show{display:flex}.upgrade-card{width:min(880px,92vw);max-height:82vh;background:#0b1220;color:#e2e8f0;border:1px solid #334155;border-radius:10px;box-shadow:0 10px 30px rgba(2,6,23,.45);overflow:hidden}.upgrade-head{display:flex;align-items:center;justify-content:space-between;padding:10px 12px;border-bottom:1px solid #334155}.upgrade-title{font-size:14px;font-weight:700}.upgrade-close{background:transparent;border:1px solid #475569;color:#e2e8f0;border-radius:6px;padding:4px 8px;cursor:pointer}.upgrade-log{margin:0;padding:12px;white-space:pre-wrap;overflow:auto;max-height:62vh;font-family:ui-monospace,SFMono-Regular,Menlo,Consolas,monospace;font-size:12px;line-height:1.45}.upgrade-foot{padding:8px 12px;border-top:1px solid #334155;font-size:12px;color:#94a3b8}.footer{margin:18px auto 0 auto;max-width:1200px;padding:10px 12px;font-size:12px;color:#64748b;text-align:center}</style></head>
<body><div class="nav"><div class="nav-left"><div class="brand">ETH Liquidation Map</div><div class="menu"><a href="/">娓呯畻鐑尯</a><a href="/config">妯″瀷閰嶇疆</a><a href="/monitor">闆峰尯鐩戞帶</a><a href="/map">鐩樺彛姹囨€?/a><a href="/liquidations" class="active">寮哄钩娓呯畻</a><a href="/bubbles">姘旀场鍥?/a><a href="/webdatasource">椤甸潰鏁版嵁婧?/a><a href="/channel">娑堟伅閫氶亾</a><a href="/analysis">鏃ュ唴鍒嗘瀽</a></div></div><div class="nav-right"><div class="theme-toggle"><button class="label" type="button">涓婚</button><button id="themeDark" onclick="setTheme('dark')">娣辫壊</button><button id="themeLight" onclick="setTheme('light')">娴呰壊</button></div><a href="#" class="upgrade" onclick="return doUpgrade(event)">&#21319;&#32423;</a></div></div>
<div class="wrap"><div class="panel"><div class="row" style="justify-content:space-between"><h2 style="margin:0">ETH 瀵搫閽╁〒鍛暬閿涘湐inance / Bybit / OKX閿?/h2><div class="row"><button id="filterBtn" class="btn" onclick="toggleFilter()">杩囨护灏忓崟</button><button class="btn" onclick="prev()">娑撳﹣绔存い?/button><button class="btn" onclick="next()">娑撳绔存い?/button><button class="btn primary" onclick="load()">鍒锋柊</button></div></div><div id="meta" class="small" style="margin:8px 0 10px 0"></div><div id="table"></div></div></div>
<script>
function setTheme(t){const theme=(t==='dark')?'dark':'light';document.documentElement.setAttribute('data-theme',theme);try{localStorage.setItem('theme',theme);}catch(_){}const bd=document.getElementById('themeDark'),bl=document.getElementById('themeLight');if(bd)bd.classList.toggle('active',theme==='dark');if(bl)bl.classList.toggle('active',theme==='light');}
function initTheme(){let t='light';try{t=localStorage.getItem('theme')||'light';}catch(_){}setTheme(t);}
let page=1,pageSize=25,filterSmall=false;
function fmtPrice(n){return Number(n).toLocaleString('zh-CN',{minimumFractionDigits:1,maximumFractionDigits:1})}
function fmtQty(n){return Number(n).toLocaleString('zh-CN',{maximumFractionDigits:4})}
function fmtAmt(n){n=Number(n);if(!isFinite(n))return '-';return n.toLocaleString('zh-CN',{minimumFractionDigits:2,maximumFractionDigits:2});}
function fmtTime(ts){try{return new Date(Number(ts||0)).toLocaleString('zh-CN',{hour12:false});}catch(_){return '-';}}
function render(rows){if(!rows||!rows.length){document.getElementById('table').innerHTML='<div class="small">鏆傛棤鏁版嵁</div>';return;}let h='<table><thead><tr><th>鏃堕棿</th><th>娴溿倖妲楅幍鈧?/th><th>鏂瑰悜</th><th>浠锋牸</th><th>鏁伴噺</th><th>閲戦 (USD)</th></tr></thead><tbody>';for(const r of rows){h+='<tr><td>'+fmtTime(r.event_ts)+'</td><td>'+String(r.exchange||'').toUpperCase()+'</td><td>'+r.side+'</td><td>'+fmtPrice(r.price)+'</td><td>'+fmtQty(r.qty)+'</td><td>'+fmtAmt(r.notional_usd)+'</td></tr>';}h+='</tbody></table>';document.getElementById('table').innerHTML=h;}
function toggleFilter(){filterSmall=!filterSmall;const b=document.getElementById('filterBtn');if(b)b.textContent=filterSmall?'鏄剧ず鍏ㄩ儴':'杩囨护灏忓崟';load();}
async function load(){const r=await fetch('/api/liquidations?page='+page+'&limit='+pageSize);const d=await r.json();let rows=(d.rows||[]);if(filterSmall)rows=rows.filter(x=>Number(x.qty||0)>=1);document.getElementById('meta').textContent='缁?'+d.page+' 妞?| 濮ｅ繘銆?'+d.page_size+' 閺?+(filterSmall?' | 瀹歌尪绻冨銈呯毈娴?1 ETH 閻ㄥ嫯顔囪ぐ?:'');render(rows);}
function prev(){if(page>1){page--;load();}}
function next(){page++;load();}
async function openUpgradeModal(){const m=document.getElementById('upgradeModal'),logEl=document.getElementById('upgradeLog'),foot=document.getElementById('upgradeFoot');if(!m||!logEl||!foot)return;m.classList.add('show');logEl.textContent='';foot.textContent='姝ｅ湪瑙﹀彂鍗囩骇...';const r=await fetch('/api/upgrade/pull',{method:'POST'});const d=await r.json().catch(()=>({error:'response parse failed',output:''}));if(d.error){logEl.textContent=String(d.output||'');foot.textContent='瑙﹀彂澶辫触: '+d.error;return;}foot.textContent='宸茶Е鍙戯紝姝ｅ湪鎵ц...';let stable=0;for(let i=0;i<180;i++){await new Promise(res=>setTimeout(res,1000));const pr=await fetch('/api/upgrade/progress').then(x=>x.json()).catch(()=>null);if(!pr)continue;logEl.textContent=String(pr.log||'');logEl.scrollTop=logEl.scrollHeight;if(pr.done){foot.textContent=(String(pr.exit_code||'')==='0')?'鍗囩骇瀹屾垚骞跺凡閲嶅惎':'閸楀洨楠囩€瑰本鍨氶敍宀勨偓鈧崙铏圭垳 '+String(pr.exit_code||'?');return;}if(!pr.running)stable++;else stable=0;if(stable>=3){foot.textContent='閸楀洨楠囨潻娑氣柤瀹歌尙绮ㄩ弶鐕傜礄閻樿埖鈧焦婀惌銉礆閿涘矁顕Λ鈧弻銉︽）韫?;return;}}foot.textContent='閸楀洨楠囨禒宥呮躬鏉╂稖顢戦敍宀冾嚞缁嬪秴鎮楅崘宥囨箙';}
function closeUpgradeModal(){const m=document.getElementById('upgradeModal');if(m)m.classList.remove('show');}
async function doUpgrade(event){if(event)event.preventDefault();openUpgradeModal();return false;}
initTheme();setInterval(load,5000);load();(async()=>{try{const r=await fetch('/api/version');const v=await r.json();const el=document.getElementById('globalFooter');if(el)el.textContent='Code by Yuhao@jiansutech.com - '+(v.commit_time||'-')+' - '+(v.commit_id||'-')+' - '+(v.branch||'-');}catch(_){const el=document.getElementById('globalFooter');if(el)el.textContent='Code by Yuhao@jiansutech.com - - - -';}})();
</script><div id="upgradeModal" class="upgrade-modal"><div class="upgrade-card"><div class="upgrade-head"><div class="upgrade-title">鍗囩骇杩囩▼</div><button class="upgrade-close" onclick="closeUpgradeModal()">鍏抽棴</button></div><pre id="upgradeLog" class="upgrade-log"></pre><div id="upgradeFoot" class="upgrade-foot">绛夊緟寮€濮?..</div></div></div><div id="globalFooter" class="footer">Code by Yuhao@jiansutech.com - loading - loading - loading</div></body></html>`

const bubblesHTML = `<!doctype html>
<html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>姘旀场鍥?/title>
<style>:root{--bg:#f5f7fb;--text:#1f2937;--muted:#64748b;--nav-bg:#0b1220;--nav-border:#243145;--nav-text:#eef3f9;--link:#d6deea;--panel-bg:#fff;--panel-border:#dce3ec;--ctl-bg:#fff;--ctl-text:#111827;--ctl-border:#cbd5e1;--chart-border:#e5e7eb}[data-theme="dark"]{--bg:#000;--text:#e5e7eb;--muted:#94a3b8;--nav-bg:#000;--nav-border:#111827;--nav-text:#eef3f9;--link:#d6deea;--panel-bg:#000;--panel-border:#1f2937;--ctl-bg:#000;--ctl-text:#e5e7eb;--ctl-border:#334155;--chart-border:#1f2937}body{margin:0;background:var(--bg);color:var(--text);font-family:Inter,system-ui,Segoe UI,Arial,sans-serif}.nav{height:56px;background:var(--nav-bg);border-bottom:1px solid var(--nav-border);display:flex;align-items:center;justify-content:space-between;padding:0 20px;position:sticky;top:0;z-index:10}.nav-left,.nav-right{display:flex;align-items:center;gap:14px}.brand{font-size:18px;font-weight:700;color:var(--nav-text)}.menu a{color:var(--link);text-decoration:none;font-size:16px;margin-right:18px}.menu a.active{color:#fff;font-weight:700}.upgrade{color:#fff;font-weight:700;text-decoration:none}.wrap{width:100%;max-width:none;margin:0 auto;padding:14px;box-sizing:border-box}.panel{border:1px solid var(--panel-border);background:var(--panel-bg);margin:10px 0;padding:12px;border-radius:10px}.row{display:flex;gap:8px;align-items:center;flex-wrap:wrap}.small{font-size:12px;color:var(--muted)}select,button,input{height:34px;border:1px solid var(--ctl-border);border-radius:8px;background:var(--ctl-bg);color:var(--ctl-text);padding:0 10px}input{width:96px}button{cursor:pointer}.btn-applied{background:#0f172a;color:#eef3f9;border-color:#0f172a}.chart{width:100%;height:682px;border:1px solid var(--chart-border);border-radius:8px;background:var(--panel-bg);display:block}.chart-wrap{position:relative}.legend{display:flex;align-items:center;justify-content:space-between;gap:12px;margin-top:8px}.tag{display:inline-flex;align-items:center;gap:6px}.dot{width:10px;height:10px;border-radius:999px;display:inline-block}.bubble-tip{position:absolute;display:none;min-width:180px;max-width:260px;background:rgba(15,23,42,.96);color:#e2e8f0;border:1px solid rgba(148,163,184,.25);border-radius:10px;padding:10px 12px;font-size:12px;line-height:1.5;box-shadow:0 10px 28px rgba(2,6,23,.35);pointer-events:none}.theme-toggle{display:inline-flex;align-items:center;gap:6px;font-size:13px}.theme-toggle button{height:30px;padding:0 10px;border-radius:999px;border:1px solid rgba(148,163,184,0.45);background:transparent;color:var(--nav-text);cursor:pointer}.theme-toggle button.label{cursor:default;opacity:.92}.theme-toggle button.active{background:rgba(255,255,255,0.12);border-color:rgba(255,255,255,0.18);color:#fff}.upgrade-modal{position:fixed;inset:0;background:rgba(2,6,23,.55);display:none;align-items:center;justify-content:center;z-index:9999}.upgrade-modal.show{display:flex}.upgrade-card{width:min(880px,92vw);max-height:82vh;background:#0b1220;color:#e2e8f0;border:1px solid #334155;border-radius:10px;box-shadow:0 10px 30px rgba(2,6,23,.45);overflow:hidden}.upgrade-head{display:flex;align-items:center;justify-content:space-between;padding:10px 12px;border-bottom:1px solid #334155}.upgrade-title{font-size:14px;font-weight:700}.upgrade-close{background:transparent;border:1px solid #475569;color:#e2e8f0;border-radius:6px;padding:4px 8px;cursor:pointer}.upgrade-log{margin:0;padding:12px;white-space:pre-wrap;overflow:auto;max-height:62vh;font-family:ui-monospace,SFMono-Regular,Menlo,Consolas,monospace;font-size:12px;line-height:1.45}.upgrade-foot{padding:8px 12px;border-top:1px solid #334155;font-size:12px;color:#94a3b8}.footer{margin:18px auto 0 auto;max-width:1200px;padding:10px 12px;font-size:12px;color:var(--muted);text-align:center}</style></head>
<body><div class="nav"><div class="nav-left"><div class="brand">ETH Liquidation Map</div><div class="menu"><a href="/">娓呯畻鐑尯</a><a href="/config">妯″瀷閰嶇疆</a><a href="/monitor">闆峰尯鐩戞帶</a><a href="/map">鐩樺彛姹囨€?/a><a href="/liquidations">寮哄钩娓呯畻</a><a href="/bubbles" class="active">姘旀场鍥?/a><a href="/webdatasource">椤甸潰鏁版嵁婧?/a><a href="/channel">娑堟伅閫氶亾</a><a href="/analysis">鏃ュ唴鍒嗘瀽</a></div></div><div class="nav-right"><div class="theme-toggle"><button class="label" type="button">涓婚</button><button id="themeDark" onclick="setTheme('dark')">娣辫壊</button><button id="themeLight" onclick="setTheme('light')">娴呰壊</button></div><a href="#" class="upgrade" onclick="return doUpgrade(event)">&#21319;&#32423;</a></div></div>
<div class="wrap"><div class="panel"><div class="row"><span>鍛ㄦ湡</span><select id="iv"><option value="1m">1M</option><option value="2m">2M</option><option value="5m">5M</option><option value="10m">10M</option><option value="15m">15M</option><option value="30m">30M</option><option value="1h">1H</option><option value="4h">4H</option><option value="8h">8H</option><option value="12h">12H</option><option value="1d">1D</option><option value="3d">3D</option><option value="7d" selected>7D</option></select><button onclick="load()">鍒锋柊</button><span class="small">杩囨护灏忓崟 ETH 鏁伴噺</span><input id="qtyFilter" type="number" min="0" step="0.1" value="50"><button id="filterBtn" onclick="applyQtyFilter()">搴旂敤杩囨护</button><label class="small" style="display:inline-flex;align-items:center;gap:6px;margin-left:6px"><input id="hist" type="checkbox">鍘嗗彶</label><button id="moreBtn" style="display:none" onclick="loadMoreHistory()">鍚戝乏鍔犺浇</button><span id="meta" class="small"></span></div><div class="chart-wrap"><canvas id="cv" class="chart" width="1600" height="620"></canvas><div id="bubbleTip" class="bubble-tip"></div></div><div class="legend small"><div>濮樻梹鍦烘径褍鐨禒锝堛€冨〒鍛暬闁叉垿顤傞敍宀勵杹閼硅弓鍞悰銊ヮ樋缁岀儤鏌熼崥鎴欌偓鍌涚泊鏉烆喚缂夐弨鎾呯礄姒涙顓绘禒銉︽付閸欏厖鏅禟缁惧じ璐熼柨姘卞仯閿涘绱濋幐澶夌秶姒х姵鐖ｅ锕傛暛瀹革箑褰搁幏鏍уЗ閵?/div><div class="tag"><span class="dot" style="background:#dc2626"></span><span>绾㈣壊=澶氬崟鐖嗕粨</span><span class="dot" style="margin-left:10px;background:#16a34a"></span><span>缁胯壊=绌哄崟鐖嗕粨</span></div></div></div></div>
<script>
let candles=[],events=[],viewStart=0,viewCount=120,drag=false,lastX=0,intervalMs=0,latestStart=0,qtyFilter=50,filterApplied=false,bubbleHoverMeta=[],klineSource='-';
function setTheme(t){const theme=(t==='dark')?'dark':'light';document.documentElement.setAttribute('data-theme',theme);try{localStorage.setItem('theme',theme);}catch(_){}const bd=document.getElementById('themeDark'),bl=document.getElementById('themeLight');if(bd)bd.classList.toggle('active',theme==='dark');if(bl)bl.classList.toggle('active',theme==='light');}
function initTheme(){let t='light';try{t=localStorage.getItem('theme')||'light';}catch(_){}setTheme(t);}
function intervalToMs(iv){switch(String(iv||'')){case '1m':return 60e3;case '2m':return 120e3;case '5m':return 300e3;case '10m':return 600e3;case '15m':return 900e3;case '30m':return 1800e3;case '1h':return 3600e3;case '4h':return 14400e3;case '8h':return 28800e3;case '12h':return 43200e3;case '1d':return 86400e3;case '3d':return 3*86400e3;case '7d':return 7*86400e3;default:return 0;}}
function fit(){const c=document.getElementById('cv');const r=c.getBoundingClientRect(),dpr=window.devicePixelRatio||1;const w=Math.max(700,Math.floor(r.width)),h=Math.max(420,Math.floor(r.height));c.width=Math.floor(w*dpr);c.height=Math.floor(h*dpr);const x=c.getContext('2d');x.setTransform(dpr,0,0,dpr,0,0);return {c,x,w,h};}
function toNum(v){const n=Number(v);return isFinite(n)?n:0}
function mapInterval(v){if(v==='10m')return '5m';if(v==='7d')return '1w';return v;}
function visibleEvents(){return (events||[]).filter(ev=>toNum(ev.qty)>=qtyFilter);}
function syncFilterBtn(){const btn=document.getElementById('filterBtn');if(btn)btn.classList.toggle('btn-applied',!!filterApplied);}
function applyQtyFilter(){const input=document.getElementById('qtyFilter');qtyFilter=Math.max(0,toNum(input&&input.value));if(input)input.value=String(qtyFilter);filterApplied=true;syncFilterBtn();draw();updateMeta();}
function updateMeta(){const meta=document.getElementById('meta');if(!meta)return;meta.textContent='K缁炬寧娼靛┃? '+(klineSource||((document.getElementById('iv')&&document.getElementById('iv').value)||'-'))+' | 鍖洪棿娓呯畻浜嬩欢 '+visibleEvents().length+' 閺?| 瀹歌尪绻冨銈呯毈娴?'+qtyFilter+' ETH';}
function drawMessage(msg){const v=fit(),x=v.x,W=v.w,H=v.h;x.clearRect(0,0,W,H);x.fillStyle=cssVar('--panel-bg','#fff');x.fillRect(0,0,W,H);x.fillStyle=cssVar('--muted','#64748b');x.font='14px sans-serif';x.fillText(msg,16,26);}
function fmtAmt(n){n=toNum(n);if(!isFinite(n))return '-';return n.toLocaleString('zh-CN',{minimumFractionDigits:2,maximumFractionDigits:2});}
function fmtQty(n){return toNum(n).toLocaleString('zh-CN',{maximumFractionDigits:4});}
function fmtPrice(n){return toNum(n).toLocaleString('zh-CN',{minimumFractionDigits:1,maximumFractionDigits:1});}
function fmtTime(ts){try{return new Date(toNum(ts)).toLocaleString('zh-CN',{hour12:false});}catch(_){return '-';}}
function hideBubbleTip(){const tip=document.getElementById('bubbleTip');if(tip)tip.style.display='none';}
function showBubbleTip(hit,ev){
  const tip=document.getElementById('bubbleTip');
  const wrap=document.querySelector('.chart-wrap');
  if(!tip||!wrap||!hit)return;
  tip.innerHTML='<div><strong>'+(String(hit.side)==='long'?'澶氬崟鐖嗕粨':'绌哄崟鐖嗕粨')+'</strong></div>'+
    '<div>娴溿倖妲楅幍鈧? '+String(hit.exchange||'-').toUpperCase()+'</div>'+
    '<div>浠锋牸: '+fmtPrice(hit.price)+'</div>'+
    '<div>鏃堕棿: '+fmtTime(hit.event_ts)+'</div>'+
    '<div>鏁伴噺: '+fmtQty(hit.qty)+' ETH</div>'+
    '<div>閲戦: '+fmtAmt(hit.notional_usd)+' USD</div>';
  tip.style.display='block';
  const wr=wrap.getBoundingClientRect();
  let left=(ev.clientX-wr.left)+14,top=(ev.clientY-wr.top)+14;
  tip.style.left='0px';tip.style.top='0px';
  const tw=tip.offsetWidth||220,th=tip.offsetHeight||110;
  if(left+tw>wr.width-10)left=Math.max(8,wr.width-10-tw);
  if(top+th>wr.height-10)top=Math.max(8,wr.height-10-th);
  tip.style.left=left+'px';tip.style.top=top+'px';
}
function parseRows(rows,interval){let cs=(rows||[]).map(r=>({t:toNum(r[0]),o:toNum(r[1]),h:toNum(r[2]),l:toNum(r[3]),c:toNum(r[4])}));if(interval==='2m'){const out=[];for(let i=0;i+1<cs.length;i+=2){const a=cs[i],b=cs[i+1];out.push({t:a.t,o:a.o,h:Math.max(a.h,b.h),l:Math.min(a.l,b.l),c:b.c});}cs=out;}if(interval==='10m'){const out=[];for(let i=0;i+1<cs.length;i+=2){const a=cs[i],b=cs[i+1];out.push({t:a.t,o:a.o,h:Math.max(a.h,b.h),l:Math.min(a.l,b.l),c:b.c});}cs=out;}if(interval==='7d'){const out=[];for(let i=0;i+6<cs.length;i+=7){const a=cs[i],g=cs.slice(i,i+7),z=g[g.length-1];out.push({t:a.t,o:a.o,h:Math.max(...g.map(v=>v.h)),l:Math.min(...g.map(v=>v.l)),c:z.c});}cs=out;}return cs;}
function cssVar(name,fallback){try{const v=getComputedStyle(document.documentElement).getPropertyValue(name).trim();return v||fallback;}catch(_){return fallback;}}
function draw(){const v=fit(),x=v.x,W=v.w,H=v.h,padL=70,padR=20,padT=18,padB=42,pw=W-padL-padR,ph=H-padT-padB,by=padT+ph;x.clearRect(0,0,W,H);bubbleHoverMeta=[];hideBubbleTip();const bg=cssVar('--panel-bg','#fff');const muted=cssVar('--muted','#64748b');const grid=cssVar('--chart-border','#e5e7eb');x.fillStyle=bg;x.fillRect(0,0,W,H);if(!candles.length){x.fillStyle=muted;x.fillText('鏆傛棤鏁版嵁',16,24);return;}const s=Math.max(0,Math.min(candles.length-1,viewStart)),e=Math.max(s+10,Math.min(candles.length,s+viewCount));const cs=candles.slice(s,e);const minP=Math.min(...cs.map(v=>v.l)),maxP=Math.max(...cs.map(v=>v.h));const span=Math.max(1e-6,maxP-minP);const sx=i=>padL+(i/(cs.length-1))*pw,sy=p=>padT+((maxP-p)/span)*ph;x.strokeStyle=grid;x.font='12px sans-serif';for(let i=0;i<=4;i++){const y=padT+ph*i/4,val=maxP-(span*i/4);x.beginPath();x.moveTo(padL,y);x.lineTo(W-padR,y);x.stroke();x.fillStyle=muted;x.fillText(val.toFixed(1),6,y+4);}const bodyW=Math.max(3,Math.min(12,pw/Math.max(20,cs.length)));for(let i=0;i<cs.length;i++){const c=cs[i],px=sx(i),yo=sy(c.o),yc=sy(c.c),yh=sy(c.h),yl=sy(c.l),up=c.c>=c.o;x.strokeStyle=up?'#16a34a':'#dc2626';x.beginPath();x.moveTo(px,yh);x.lineTo(px,yl);x.stroke();x.fillStyle=up?'rgba(22,163,74,0.75)':'rgba(220,38,38,0.75)';x.fillRect(px-bodyW/2,Math.min(yo,yc),bodyW,Math.max(1,Math.abs(yc-yo)));}
function upperBound(arr,x){let lo=0,hi=arr.length;while(lo<hi){const mid=(lo+hi)>>1;if(arr[mid]<=x)lo=mid+1;else hi=mid;}return lo;}
const t0=cs[0].t,t1=cs[cs.length-1].t;
const spanMs=(intervalMs>0?intervalMs:intervalToMs(document.getElementById('iv').value||''))||0;
const tEnd=t1+(spanMs>0?spanMs:0);
const times=cs.map(v=>v.t);
const vis=visibleEvents().filter(e=>{const tt=toNum(e.event_ts);return tt>=t0&&(tEnd>t0?tt<tEnd:tt<=t1);});
const maxN=Math.max(1,...vis.map(e=>toNum(e.notional_usd)));
const highlightStart=times[times.length-1]||t1;
const highlightEnd=highlightStart+(spanMs>0?spanMs:0);
for(const ev of vis){
  const tt=toNum(ev.event_ts);
  if(spanMs>0 && tt>=highlightEnd) continue;
  const idx=Math.max(0,Math.min(times.length-1,upperBound(times,tt)-1));
  let px=sx(idx);
  if(spanMs>0 && idx<times.length-1){
    const step=sx(idx+1)-sx(idx);
    const frac=Math.max(0,Math.min(1,(tt-times[idx])/spanMs));
    px+=step*frac;
  }
  const py=sy(toNum(ev.price));
  const r=3+18*Math.sqrt(Math.max(0,toNum(ev.notional_usd))/maxN);
  const side=String(ev.side||'').toLowerCase();
  const isLatest=(spanMs>0)?(tt>=highlightStart&&tt<highlightEnd):(idx===times.length-1);
  let color=(side==='long')?'rgba(220,38,38,'+(isLatest?'0.45':'0.28')+')':'rgba(22,163,74,'+(isLatest?'0.45':'0.28')+')';
  let stroke=(side==='long')?'rgba(220,38,38,'+(isLatest?'0.95':'0.55')+')':'rgba(22,163,74,'+(isLatest?'0.95':'0.55')+')';
  x.beginPath();x.fillStyle=color;x.strokeStyle=stroke;x.arc(px,py,r,0,Math.PI*2);x.fill();x.stroke();
  bubbleHoverMeta.push({px:px,py:py,r:r,event:ev});
}
x.fillStyle=muted;for(let i=0;i<=6;i++){const k=Math.floor((cs.length-1)*i/6),px=sx(k),t=new Date(cs[k].t).toLocaleString('zh-CN',{month:'2-digit',day:'2-digit',hour:'2-digit',minute:'2-digit',hour12:false});x.fillText(t,Math.max(padL,px-30),H-10);} }
function uniqByT(list){const out=[];const seen=new Set();for(const it of (list||[])){const t=toNum(it.t);if(!t||seen.has(t))continue;seen.add(t);out.push(it);}out.sort((a,b)=>a.t-b.t);return out;}
function mergeEvents(a,b){const out=[];const key=(e)=>String(toNum(e.event_ts))+'|'+String(e.exchange||'')+'|'+String(e.side||'')+'|'+String(toNum(e.price))+'|'+String(toNum(e.qty));const seen=new Set();for(const it of (a||[])){const k=key(it);if(seen.has(k))continue;seen.add(k);out.push(it);}for(const it of (b||[])){const k=key(it);if(seen.has(k))continue;seen.add(k);out.push(it);}out.sort((x,y)=>toNum(x.event_ts)-toNum(y.event_ts));return out;}
function setMoreBtnVisible(){const cb=document.getElementById('hist');const b=document.getElementById('moreBtn');if(!cb||!b)return;b.style.display=cb.checked?'inline-block':'none';}
async function load(){const iv=document.getElementById('iv').value;const kr=await fetch('/api/klines?interval='+encodeURIComponent(iv)+'&limit=500');if(!kr.ok){const msg='K缁惧灝濮炴潪钘夈亼鐠? '+await kr.text();candles=[];events=[];klineSource='-';updateMeta();drawMessage(msg);return;}const kd=await kr.json();klineSource=kd.source||iv;candles=uniqByT(parseRows(kd.rows||[],iv));if(!candles.length){events=[];updateMeta();drawMessage('K缁惧灝濮炴潪钘夈亼鐠? 鏉╂柨娲栭弫鐗堝祦娑撹櫣鈹?);return;}intervalMs=(candles.length>=2)?Math.max(0,toNum(candles[candles.length-1].t)-toNum(candles[candles.length-2].t)):intervalToMs(iv);if(!(intervalMs>0))intervalMs=intervalToMs(iv);latestStart=candles.length?toNum(candles[candles.length-1].t):0;events=[];if(candles.length){const startTS=toNum(candles[0].t);const endTS=latestStart+(intervalMs>0?intervalMs:0);const er=await fetch('/api/liquidations?limit=5000&page=1&start_ts='+encodeURIComponent(startTS)+'&end_ts='+encodeURIComponent(endTS));const ed=await er.json();events=ed.rows||[];}viewCount=Math.min(160,Math.max(50,Math.floor(candles.length*0.45)));viewStart=Math.max(0,candles.length-viewCount);setMoreBtnVisible();updateMeta();draw();}
async function loadMoreHistory(){const cb=document.getElementById('hist');if(!cb||!cb.checked)return;if(!candles.length)return;const iv=document.getElementById('iv').value;const endTS=Math.max(0,toNum(candles[0].t)-1);const kr=await fetch('/api/klines?interval='+encodeURIComponent(iv)+'&limit=500&end_ts='+encodeURIComponent(endTS));if(!kr.ok){document.getElementById('meta').textContent='閸樺棗褰禟缁惧灝濮炴潪钘夈亼鐠? '+await kr.text();return;}const kd=await kr.json();klineSource=kd.source||klineSource;const more=uniqByT(parseRows(kd.rows||[],iv));if(!more.length){document.getElementById('meta').textContent='濞屸剝婀侀弴鏉戭樋閸樺棗褰禟缁?;return;}const added=more.filter(x=>toNum(x.t)<toNum(candles[0].t));if(!added.length){document.getElementById('meta').textContent='濞屸剝婀侀弴鏉戭樋閸樺棗褰禟缁?;return;}candles=uniqByT(added.concat(candles));const spanMs=(intervalMs>0?intervalMs:intervalToMs(iv));const startTS=toNum(added[0].t),endTS2=toNum(added[added.length-1].t)+(spanMs>0?spanMs:0);const er=await fetch('/api/liquidations?limit=5000&page=1&start_ts='+encodeURIComponent(startTS)+'&end_ts='+encodeURIComponent(endTS2));const ed=await er.json();events=mergeEvents(ed.rows||[],events);viewStart=Math.max(0,viewStart+added.length);updateMeta();draw();}
const c=document.getElementById('cv');c.addEventListener('wheel',e=>{if(!candles.length)return;e.preventDefault();const right=Math.min(candles.length,viewStart+viewCount);const factor=e.deltaY<0?0.88:1.12;const nextCount=Math.max(30,Math.min(candles.length,Math.round(viewCount*factor)));viewCount=nextCount;viewStart=Math.max(0,Math.min(candles.length-viewCount,right-viewCount));draw();},{passive:false});c.addEventListener('mousedown',e=>{drag=true;lastX=e.clientX});c.addEventListener('mousemove',e=>{const rect=c.getBoundingClientRect();const mx=e.clientX-rect.left,my=e.clientY-rect.top;let hit=null,dist=1e18;for(const it of bubbleHoverMeta){const d=Math.hypot(mx-it.px,my-it.py);if(d<=it.r+3&&d<dist){dist=d;hit=it.event;}}if(hit)showBubbleTip(hit,e);else hideBubbleTip();});c.addEventListener('mouseleave',()=>hideBubbleTip());window.addEventListener('mouseup',()=>drag=false);window.addEventListener('mousemove',e=>{if(!drag||!candles.length)return;const dx=e.clientX-lastX;lastX=e.clientX;const shift=Math.round(-dx/8);if(shift!==0){viewStart=Math.max(0,Math.min(candles.length-viewCount,viewStart+shift));draw();}});window.addEventListener('resize',()=>draw());document.getElementById('iv').addEventListener('change',load);document.getElementById('hist').addEventListener('change',setMoreBtnVisible);document.getElementById('qtyFilter').addEventListener('input',()=>{filterApplied=false;syncFilterBtn();});document.getElementById('qtyFilter').addEventListener('change',applyQtyFilter);
async function openUpgradeModal(){const m=document.getElementById('upgradeModal'),logEl=document.getElementById('upgradeLog'),foot=document.getElementById('upgradeFoot');if(!m||!logEl||!foot)return;m.classList.add('show');logEl.textContent='';foot.textContent='姝ｅ湪瑙﹀彂鍗囩骇...';const r=await fetch('/api/upgrade/pull',{method:'POST'});const d=await r.json().catch(()=>({error:'response parse failed',output:''}));if(d.error){logEl.textContent=String(d.output||'');foot.textContent='瑙﹀彂澶辫触: '+d.error;return;}foot.textContent='宸茶Е鍙戯紝姝ｅ湪鎵ц...';let stable=0;for(let i=0;i<180;i++){await new Promise(res=>setTimeout(res,1000));const pr=await fetch('/api/upgrade/progress').then(x=>x.json()).catch(()=>null);if(!pr)continue;logEl.textContent=String(pr.log||'');logEl.scrollTop=logEl.scrollHeight;if(pr.done){foot.textContent=(String(pr.exit_code||'')==='0')?'鍗囩骇瀹屾垚骞跺凡閲嶅惎':'閸楀洨楠囩€瑰本鍨氶敍宀勨偓鈧崙铏圭垳 '+String(pr.exit_code||'?');return;}if(!pr.running)stable++;else stable=0;if(stable>=3){foot.textContent='閸楀洨楠囨潻娑氣柤瀹歌尙绮ㄩ弶鐕傜礄閻樿埖鈧焦婀惌銉礆閿涘矁顕Λ鈧弻銉︽）韫?;return;}}foot.textContent='閸楀洨楠囨禒宥呮躬鏉╂稖顢戦敍宀冾嚞缁嬪秴鎮楅崘宥囨箙';}
function closeUpgradeModal(){const m=document.getElementById('upgradeModal');if(m)m.classList.remove('show');}
async function doUpgrade(event){if(event)event.preventDefault();openUpgradeModal();return false;}
initTheme();syncFilterBtn();load();
</script><div id="upgradeModal" class="upgrade-modal"><div class="upgrade-card"><div class="upgrade-head"><div class="upgrade-title">鍗囩骇杩囩▼</div><button class="upgrade-close" onclick="closeUpgradeModal()">鍏抽棴</button></div><pre id="upgradeLog" class="upgrade-log"></pre><div id="upgradeFoot" class="upgrade-foot">绛夊緟寮€濮?..</div></div></div><div id="globalFooter" class="footer">Code by Yuhao@jiansutech.com - loading - loading - loading</div><script>(async()=>{try{const r=await fetch('/api/version');const v=await r.json();const el=document.getElementById('globalFooter');if(el)el.textContent='Code by Yuhao@jiansutech.com - '+(v.commit_time||'-')+' - '+(v.commit_id||'-')+' - '+(v.branch||'-');}catch(_){const el=document.getElementById('globalFooter');if(el)el.textContent='Code by Yuhao@jiansutech.com - - - -';}})();</script></body></html>`
const configHTML = `<!doctype html>
<html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>{{.PageTitle}}</title>
<style>body{margin:0;background:#f5f7fb;color:#1f2937;font-family:Inter,system-ui,Segoe UI,Arial,sans-serif}.nav{height:56px;background:#0b1220;border-bottom:1px solid #243145;display:flex;align-items:center;justify-content:space-between;padding:0 20px;position:sticky;top:0;z-index:10}.nav-left,.nav-right{display:flex;align-items:center;gap:20px}.brand{font-size:18px;font-weight:700;color:#eef3f9}.menu a{color:#d6deea;text-decoration:none;font-size:16px;margin-right:18px}.menu a.active{color:#fff;font-weight:700}.upgrade{color:#fff;font-weight:700;text-decoration:none}.theme-toggle{display:inline-flex;align-items:center;gap:6px;font-size:13px}.theme-toggle button{height:30px;padding:0 10px;border-radius:999px;border:1px solid rgba(148,163,184,0.45);background:transparent;color:#e5e7eb;cursor:pointer}.theme-toggle button.label{cursor:default;opacity:.92}.theme-toggle button.active{background:rgba(255,255,255,0.12);border-color:rgba(255,255,255,0.18);color:#fff}.wrap{max-width:980px;margin:0 auto;padding:22px}.panel{border:1px solid #dce3ec;background:#fff;margin:14px 0;padding:16px;border-radius:10px;box-shadow:0 1px 2px rgba(15,23,42,.04)}.small{font-size:12px;color:#6b7280}.row{display:flex;gap:10px;flex-wrap:wrap;align-items:center}.grid{display:grid;grid-template-columns:1fr 1fr;gap:12px}.field label{display:block;font-size:12px;color:#6b7280}.field input{width:100%;box-sizing:border-box;padding:10px;border:1px solid #cbd5e1;border-radius:8px;background:#fff;color:#111827;margin-top:6px}.field input:disabled{background:#f1f5f9;color:#64748b;cursor:not-allowed}button.primary{background:#22c55e;color:#fff;border:0;padding:10px 16px;border-radius:8px;cursor:pointer}button.secondary{background:#fff;color:#111827;border:1px solid #cbd5e1;padding:10px 16px;border-radius:8px;cursor:pointer}.q{display:inline-flex;align-items:center;justify-content:center;width:16px;height:16px;margin-left:6px;border-radius:999px;border:1px solid #cbd5e1;color:#475569;font-size:12px;line-height:1;cursor:help;background:#fff}[data-theme="dark"] body{background:#000;color:#e5e7eb}[data-theme="dark"] .nav{background:#000;border-bottom-color:#111827}[data-theme="dark"] .panel{background:#000;border-color:#1f2937}[data-theme="dark"] .small,[data-theme="dark"] .field label{color:#94a3b8}[data-theme="dark"] .field input,[data-theme="dark"] select,[data-theme="dark"] button.secondary{background:#000;color:#e5e7eb;border-color:#334155}[data-theme="dark"] .q{background:#000;color:#cbd5e1;border-color:#334155}.upgrade-modal{position:fixed;inset:0;background:rgba(2,6,23,.55);display:none;align-items:center;justify-content:center;z-index:9999}.upgrade-modal.show{display:flex}.upgrade-card{width:min(880px,92vw);max-height:82vh;background:#0b1220;color:#e2e8f0;border:1px solid #334155;border-radius:10px;box-shadow:0 10px 30px rgba(2,6,23,.45);overflow:hidden}.upgrade-head{display:flex;align-items:center;justify-content:space-between;padding:10px 12px;border-bottom:1px solid #334155}.upgrade-title{font-size:14px;font-weight:700}.upgrade-close{background:transparent;border:1px solid #475569;color:#e2e8f0;border-radius:6px;padding:4px 8px;cursor:pointer}.upgrade-log{margin:0;padding:12px;white-space:pre-wrap;overflow:auto;max-height:62vh;font-family:ui-monospace,SFMono-Regular,Menlo,Consolas,monospace;font-size:12px;line-height:1.45}.upgrade-foot{padding:8px 12px;border-top:1px solid #334155;font-size:12px;color:#94a3b8}.footer{margin:18px auto 0 auto;max-width:1200px;padding:10px 12px;font-size:12px;color:#64748b;text-align:center}</style></head>
<body><div class="nav"><div class="nav-left"><div class="brand">ETH Liquidation Map</div><div class="menu"><a href="/">娓呯畻鐑尯</a><a href="/config"{{if eq .ActiveMenu "config"}} class="active"{{end}}>妯″瀷閰嶇疆</a><a href="/monitor">闆峰尯鐩戞帶</a><a href="/map">鐩樺彛姹囨€?/a><a href="/liquidations">寮哄钩娓呯畻</a><a href="/bubbles">姘旀场鍥?/a><a href="/webdatasource">椤甸潰鏁版嵁婧?/a><a href="/channel">娑堟伅閫氶亾</a><a href="/analysis"{{if eq .ActiveMenu "analysis"}} class="active"{{end}}>鏃ュ唴鍒嗘瀽</a></div></div><div class="nav-right"><div class="theme-toggle"><button class="label" type="button">涓婚</button><button id="themeDark" onclick="setTheme('dark')">娣辫壊</button><button id="themeLight" onclick="setTheme('light')">娴呰壊</button></div><a href="#" class="upgrade" onclick="return doUpgrade(event)">&#21319;&#32423;</a></div></div>
<div class="wrap">{{if .ShowAnalysisInfo}}<div class="panel"><h2 style="margin-top:0">鏃ュ唴鍒嗘瀽</h2><div class="small">棣栫増鍏堟壙鎺ユā鍨嬪弬鏁颁笌璇存槑锛屽悗缁細鍦ㄨ繖閲岃ˉ鍏呭垎鏋愮粨鏋溿€佽瘎鍒嗕笌鍥炴祴鎽樿銆?/div><div class="small" style="margin-top:8px">褰撳墠椤甸潰鍙洿鎺ョ紪杈戜笌鏃ュ唴鍒嗘瀽鐩稿叧鐨勬ā鍨嬪弬鏁帮紝淇濆瓨鍚庝細涓庘€滄ā鍨嬮厤缃€濋〉闈繚鎸佸悓姝ャ€?/div></div>{{end}}<div class="panel"><h2 style="margin-top:0">娓呯畻鍦板浘妯″瀷鍙傛暟</h2><div class="small">娣囶喗鏁奸崥搴ｇ彌閸楀啿濂栭崫?OI 婢х偤鍣哄Ο鈥崇€烽惃鍕吀缁犳ぞ绗岀仦鏇犮仛閵?/div>
<div class="grid" style="margin-top:14px">
  <div class="field"><label>閸ョ偟婀呯粣妤€褰涢敍鍫濄亯閿?/label><input id="lookback" type="number" min="0" max="30" step="1" disabled></div>
  <div class="field"><label>閺冨爼妫垮璁圭礄閸掑棝鎸撻敍?/label><input id="bucket" type="number" min="1" max="30" step="1"></div>
  <div class="field"><label>浠锋牸姝ラ暱</label><input id="step" type="number" min="1" max="50" step="0.5"></div>
  <div class="field"><label>浠锋牸鑼冨洿锛埪憋級</label><input id="range" type="number" min="100" max="1000" step="10"></div>
  <div class="field" style="grid-column:1/-1">
    <label>閺夌姵娼岄崶鍝勭暰濡楋絼缍呴敍?x / 5x / 10x / 20x / 30x / 50x / 100x閿?/label>
    <div class="small" style="margin-top:6px">閸掑棗鍩嗛柊宥囩枂閿涙碍娼堥柌宥冣偓浣烘樊閹躲倓绻氱拠渚€鍣鹃悳鍥モ偓浣界カ闁叉垼鍨傞悳鍥╃級閺€鍓ч兇閺佸府绱欐稉搴㈡浆閺夊棙銆傛担宥勭娑撯偓鐎电懓绨查敍澶堚偓?/div>
    <div class="row" style="margin-top:10px;justify-content:space-between">
      <div class="field" style="min-width:280px">
        <label>缂傛牞绶懠鍐ㄦ纯閿涘牊娼堥柌?mm閿?/label>
        <select id="ex_scope" style="width:100%;box-sizing:border-box;padding:10px;border:1px solid #cbd5e1;border-radius:8px;background:#fff;color:#111827;margin-top:6px">
          <option value="global">鍏ㄥ眬</option>
          <option value="binance">Binance</option>
          <option value="okx">OKX</option>
          <option value="bybit">Bybit</option>
        </select>
        <div class="small" style="margin-top:6px">閹绘劗銇氶敍姘崇カ闁叉垼鍨傞悳鍥╃級閺€鍓ч兇閺侀绮涙稉鍝勫弿鐏炩偓闁板秶鐤嗛妴?/div>
      </div>
      <div class="row" style="margin-top:18px">
        <input id="fit_hours" type="number" min="1" max="168" step="1" value="24" style="width:88px;height:34px;border:1px solid #cbd5e1;border-radius:8px;background:#fff;padding:0 10px">
        <span class="small">灏忔椂</span>
        <input id="fit_min" type="number" min="5" max="200" step="1" value="25" style="width:76px;height:34px;border:1px solid #cbd5e1;border-radius:8px;background:#fff;padding:0 10px">
        <span class="small">閺?/span>
        <button class="secondary" type="button" onclick="fitScope()">鎷熷悎</button>
        <button class="secondary" type="button" onclick="clearScope()">娓呯┖瑕嗙洊</button>
        <span id="fitMsg" class="small"></span>
      </div>
    </div>
    <div style="overflow:auto;margin-top:10px">
      <table style="width:100%;border-collapse:collapse">
        <thead><tr style="background:#f1f5f9"><th style="text-align:left;padding:8px;border:1px solid #e2e8f0">鏉犳潌</th><th style="text-align:left;padding:8px;border:1px solid #e2e8f0">鏉冮噸</th><th style="text-align:left;padding:8px;border:1px solid #e2e8f0">缁存姢淇濊瘉閲戠巼 <span class="q" title="閻劑鈧棑绱版潻鎴滄妧娴溿倖妲楅幍鈧惃鍕樊閹躲倓绻氱拠渚€鍣鹃悳鍥风礄Maintenance Margin Ratio閿涘矁顔囨担?mm閿涘绱濋悽銊ょ艾鐠侊紕鐣诲〒鍛暬娴犺渹缍呯純顔衡偓淇秏 鐡掑﹤銇囬敍灞藉帒鐠佸摜娈戞禍蹇斿疮鐡掑﹤鐨敍灞剧缁犳ぞ鐜搾濞锯偓婊堟浆鏉╂垹骞囨禒灏佲偓婵勨偓?#10;&#10;閺堫剚膩閸ㄥ娈戝〒鍛暬娴犲嚖绱欓幐澶嬬垼鐠侀鐜?mark 娴兼壆鐣婚敍澶涚窗&#10;婢舵艾宕熷〒鍛暬娴犲嚖绱欐稉瀣煙閿涘绱發iqLong = mark * (1 - 1/lev + mm)&#10;缁屽搫宕熷〒鍛暬娴犲嚖绱欐稉濠冩煙閿涘绱發iqShort = mark * (1 + 1/lev - mm)&#10;&#10;瑜板崬鎼烽敍?#10;- 閹绘劙鐝?mm閿涙艾顦块崡鏇熺缁犳ぞ鐜稉濠勑╅妴浣衡敄閸楁洘绔荤粻妞剧幆娑撳些閿涘牅琚辨潏褰掑厴閺囩鍒涙潻鎴犲箛娴犲嚖绱氶敍灞剧叴鐎涙劖娲块梿鍡曡厬閿?#10;- 闂勫秳缍?mm閿涙碍绔荤粻妞剧幆閺囩绻欑粋鑽ゅ箛娴犲嚖绱濋弻鍗炵摍閺囨潙鍨庨弫锝冣偓?#10;&#10;缁€杞扮伐閿涙ark=2250閿涘ev=10x&#10;- mm=0.005閿涙iqLong=2250*(1-0.1+0.005)=2021.25閿涙泊iqShort=2250*(1+0.1-0.005)=2463.75&#10;- mm=0.010閿涙iqLong=2250*(1-0.1+0.010)=2032.50閿涙泊iqShort=2250*(1+0.1-0.010)=2452.50&#10;&#10;閹绘劗銇氶敍姘辨窗閸撳秶鏅棃銏犲帒鐠侀晲缍樻稉鐑樼槨娑擃亝娼弶鍡樸€傛担宥呭礋閻欘剝顔曠純?mm閵?>?</span></th><th style="text-align:left;padding:8px;border:1px solid #e2e8f0">璧勯噾璐圭巼缂╂斁绯绘暟 <span class="q" title="閻劑鈧棑绱伴悽銊ㄧカ闁叉垼鍨傞悳鍥风礄Funding Rate閿涘娼电拫鍐╂殻閳ユ粌顦?缁岃　鈧繃绔荤粻妤€宸辨惔锔炬畱閸掑棝鍘ゅВ鏂剧伐閵?#10;&#10;閸忣剙绱￠敍鍫熺槨娑擃亙姘﹂弰鎾村閵嗕焦鐦℃稉顏呮浆閺夊棙銆傛担宥呭瀻閸掝偉顓哥粻妤嬬礆閿?#10;longShare = clamp(0.5 + funding_rate * funding_scale, 0.2, 0.8)&#10;shortShare = 1 - longShare&#10;longAmt = 铻朞I * longShare * weight&#10;shortAmt = 铻朞I * shortShare * weight&#10;&#10;鐟欙綁鍣撮敍?#10;- funding_rate > 0 闁艾鐖剁悰銊с仛婢舵艾銇旀禒妯垮瀭閿涘本膩閸ㄥ绱伴幎濠冩纯婢舵艾宸辨惔锕€鍨庣紒娆屸偓婊呪敄閸楁洘绔荤粻妞剧幆閿涘牅绗傞弬鐟板隘閸╃噦绱氶垾婵撶礉閸ョ姵顒濇稉濠冩煙閺屽崬鐡欐导姘綁婢堆嶇幢&#10;- funding_rate < 0 閻╃寮介敍灞肩窗鐠佲晙绗呴弬瑙勭叴鐎涙劖娲挎径褝绱?#10;- funding_scale 鐡掑﹤銇囬敍灞戒焊閸氭垼绉哄鐚寸礉娴ｅ棔绱扮悮顐︽閸掕泛婀?20%~80% 閸栨椽妫块妴?#10;&#10;缁€杞扮伐閿涙瓲unding_rate=0.00005閿?e-5閿?#10;- funding_scale=4500 閳?longShare=0.5+0.00005*4500=0.725閿?2.5%/27.5%閿?#10;- funding_scale=7000 閳?0.85 閳?clamp 閸?0.80閿?0%/20%閿?>?</span></th></tr></thead>
        <tbody>
          <tr><td style="padding:8px;border:1px solid #e2e8f0">1x</td><td style="padding:8px;border:1px solid #e2e8f0"><input id="w_1" type="number" step="0.01" min="0" max="100"></td><td style="padding:8px;border:1px solid #e2e8f0"><input id="mm_1" type="number" step="0.0001"></td><td style="padding:8px;border:1px solid #e2e8f0"><input id="fs_1" type="number" step="100"></td></tr>
          <tr><td style="padding:8px;border:1px solid #e2e8f0">5x</td><td style="padding:8px;border:1px solid #e2e8f0"><input id="w_5" type="number" step="0.01" min="0" max="100"></td><td style="padding:8px;border:1px solid #e2e8f0"><input id="mm_5" type="number" step="0.0001"></td><td style="padding:8px;border:1px solid #e2e8f0"><input id="fs_5" type="number" step="100"></td></tr>
          <tr><td style="padding:8px;border:1px solid #e2e8f0">10x</td><td style="padding:8px;border:1px solid #e2e8f0"><input id="w_10" type="number" step="0.01" min="0" max="100"></td><td style="padding:8px;border:1px solid #e2e8f0"><input id="mm_10" type="number" step="0.0001"></td><td style="padding:8px;border:1px solid #e2e8f0"><input id="fs_10" type="number" step="100"></td></tr>
          <tr><td style="padding:8px;border:1px solid #e2e8f0">20x</td><td style="padding:8px;border:1px solid #e2e8f0"><input id="w_20" type="number" step="0.01" min="0" max="100"></td><td style="padding:8px;border:1px solid #e2e8f0"><input id="mm_20" type="number" step="0.0001"></td><td style="padding:8px;border:1px solid #e2e8f0"><input id="fs_20" type="number" step="100"></td></tr>
          <tr><td style="padding:8px;border:1px solid #e2e8f0">30x</td><td style="padding:8px;border:1px solid #e2e8f0"><input id="w_30" type="number" step="0.01" min="0" max="100"></td><td style="padding:8px;border:1px solid #e2e8f0"><input id="mm_30" type="number" step="0.0001"></td><td style="padding:8px;border:1px solid #e2e8f0"><input id="fs_30" type="number" step="100"></td></tr>
          <tr><td style="padding:8px;border:1px solid #e2e8f0">50x</td><td style="padding:8px;border:1px solid #e2e8f0"><input id="w_50" type="number" step="0.01" min="0" max="100"></td><td style="padding:8px;border:1px solid #e2e8f0"><input id="mm_50" type="number" step="0.0001"></td><td style="padding:8px;border:1px solid #e2e8f0"><input id="fs_50" type="number" step="100"></td></tr>
          <tr><td style="padding:8px;border:1px solid #e2e8f0">100x</td><td style="padding:8px;border:1px solid #e2e8f0"><input id="w_100" type="number" step="0.01" min="0" max="100"></td><td style="padding:8px;border:1px solid #e2e8f0"><input id="mm_100" type="number" step="0.0001"></td><td style="padding:8px;border:1px solid #e2e8f0"><input id="fs_100" type="number" step="100"></td></tr>
          <tr style="background:#f8fafc"><td style="padding:8px;border:1px solid #e2e8f0;font-weight:700">鍚堣</td><td style="padding:8px;border:1px solid #e2e8f0"><span id="w_sum" style="font-weight:700">-</span>%</td><td style="padding:8px;border:1px solid #e2e8f0"></td><td style="padding:8px;border:1px solid #e2e8f0"></td></tr>
        </tbody>
      </table>
    </div>
  </div>
  <div class="field"><label>娓呯畻寮哄害缂╂斁绯绘暟</label><input id="scale" type="number" step="0.1" min="0.1" max="50"></div>
  <div class="field"><label>鏃堕棿琛板噺绯绘暟 k</label><input id="decay" type="number" step="0.1"></div>
  <div class="field"><label>闁槒绻庢禒閿嬪⒖閺侊絾鐦笟?/label><input id="neighbor" type="number" step="0.01"></div>
</div>
<div class="row" style="margin-top:14px"><button class="primary" onclick="save()">淇濆瓨</button><button class="secondary" onclick="reloadCfg()">閲嶈浇</button><span id="msg" class="small" style="margin-left:10px"></span></div></div></div>
<script>
function setTheme(t){const theme=(t==='dark')?'dark':'light';document.documentElement.setAttribute('data-theme',theme);try{localStorage.setItem('theme',theme);}catch(_){}const bd=document.getElementById('themeDark'),bl=document.getElementById('themeLight');if(bd)bd.classList.toggle('active',theme==='dark');if(bl)bl.classList.toggle('active',theme==='light');}
function initTheme(){let t='light';try{t=localStorage.getItem('theme')||'light';}catch(_){}setTheme(t);}
let lastCfg=null;
let currentScope='global';
let scopeCleared=false;
async function reloadWindowLookback(){
  const el=document.getElementById('lookback');
  if(!el) return;
  const d=await fetch('/api/dashboard').then(r=>r.json()).catch(()=>null);
  if(!d||!(d.window_days===0||d.window_days)) return;
  el.value=String(d.window_days);
}
function bind(cfg){
  lastCfg=cfg;
  scopeCleared=false;
  document.getElementById('bucket').value=cfg.BucketMin||5;
  document.getElementById('step').value=cfg.PriceStep||5;
  document.getElementById('range').value=cfg.PriceRange||400;
  const levs=[1,5,10,20,30,50,100];
  const fs=csvNums(cfg.FundingScaleCSV,levs.length);
  const defFS=Number(cfg.FundingScale||7000);
  for(let i=0;i<levs.length;i++){
    const lv=levs[i];
    const fi=isFinite(fs[i])?fs[i]:defFS;
    const fsEl=document.getElementById('fs_'+lv);
    if(fsEl) fsEl.value=String(fi);
  }
  const scopeSel=document.getElementById('ex_scope');
  if(scopeSel){
    if(!currentScope) currentScope='global';
    scopeSel.value=currentScope;
    scopeSel.onchange=()=>{currentScope=scopeSel.value||'global';scopeCleared=false;renderScope();};
  }
  renderScope();
  document.getElementById('scale').value=cfg.IntensityScale||1.0;
  document.getElementById('decay').value=cfg.DecayK||2.2;
  document.getElementById('neighbor').value=cfg.NeighborShare||0.28;
}

function toNum(v){const n=Number(String(v??'').trim());return isFinite(n)?n:NaN;}
function csvNums(raw,n){const parts=String(raw||'').split(',');if(parts.length===1){const v=toNum(parts[0]);return Array.from({length:n},()=>v);}const out=[];for(let i=0;i<n;i++){out.push(toNum(parts[i]));}return out;}

function getScopeCSV(scope, kind){
  const cfg=lastCfg||{};
  const s=String(scope||'global');
  if(kind==='w'){
    if(s==='binance') return String(cfg.WeightCSVBinance||'').trim()||String(cfg.WeightCSV||'').trim();
    if(s==='okx') return String(cfg.WeightCSVOKX||'').trim()||String(cfg.WeightCSV||'').trim();
    if(s==='bybit') return String(cfg.WeightCSVBybit||'').trim()||String(cfg.WeightCSV||'').trim();
    return String(cfg.WeightCSV||'').trim();
  }
  if(kind==='mm'){
    if(s==='binance') return String(cfg.MaintMarginCSVBinance||'').trim()||String(cfg.MaintMarginCSV||'').trim();
    if(s==='okx') return String(cfg.MaintMarginCSVOKX||'').trim()||String(cfg.MaintMarginCSV||'').trim();
    if(s==='bybit') return String(cfg.MaintMarginCSVBybit||'').trim()||String(cfg.MaintMarginCSV||'').trim();
    return String(cfg.MaintMarginCSV||'').trim();
  }
  return '';
}

function renderScope(){
  const cfg=lastCfg||{};
  const levs=[1,5,10,20,30,50,100];
  const defW=1/levs.length, defMM=Number(cfg.MaintMargin||0.005);
  const w=csvNums(getScopeCSV(currentScope,'w'),levs.length);
  const mm=csvNums(getScopeCSV(currentScope,'mm'),levs.length);
  for(let i=0;i<levs.length;i++){
    const lv=levs[i];
    const wi=isFinite(w[i])?w[i]:defW;
    const mi=isFinite(mm[i])?mm[i]:defMM;
    const wEl=document.getElementById('w_'+lv),mmEl=document.getElementById('mm_'+lv);
    if(wEl) wEl.value=(wi*100).toFixed(2);
    if(mmEl) mmEl.value=String(mi);
  }
  const sumEl=document.getElementById('w_sum');
  if(sumEl){
    const s=levs.map(lv=>Number((document.getElementById('w_'+lv)||{}).value||0)).reduce((a,b)=>a+(isFinite(b)?b:0),0);
    sumEl.textContent=s.toFixed(2);
  }
  const msg=document.getElementById('fitMsg');
  if(msg){
    msg.textContent=(currentScope==='global')?'瑜版挸澧犵紓鏍帆閿涙艾鍙忕仦鈧?:('瑜版挸澧犵紓鏍帆閿?+currentScope.toUpperCase());
  }
}

async function fitScope(){
  const msg=document.getElementById('fitMsg');
  if(!msg){return;}
  msg.textContent='閹风喎鎮庢稉?..';
  const hoursEl=document.getElementById('fit_hours');
  const hours=Math.max(1,Math.min(168,Number((hoursEl&&hoursEl.value)||24)||24));
  const minEl=document.getElementById('fit_min');
  const minEvents=Math.max(5,Math.min(200,Number((minEl&&minEl.value)||25)||25));
  const ex=(currentScope==='global')?'':currentScope;
  const u='/api/model-fit?hours='+encodeURIComponent(String(hours))+'&min_events='+encodeURIComponent(String(minEvents))+(ex?('&exchange='+encodeURIComponent(ex)):'&mode=global');
  const r=await fetch(u).catch(()=>null);
  if(!r){msg.textContent='閹风喎鎮庢径杈Е閿涙氨缍夌紒婊堟晩鐠?;return;}
  if(!r.ok){msg.textContent='鎷熷悎澶辫触锛欻TTP '+String(r.status);return;}
  const d=await r.json().catch(()=>null);
  if(!d){msg.textContent='閹风喎鎮庢径杈Е閿涙艾鎼锋惔鏃囆掗弸鎰亼鐠?;return;}
  if(!d.suggestions||!d.suggestions.length){
    const cs=d.counts||{};
    const parts=[];
    for(const k of ['binance','okx','bybit']){
      const c=cs[k]||{};
      const n=Number(c.count||0);
      if(n>0) parts.push(k+':'+n);
    }
    msg.textContent='閹风喎鎮庢径杈Е閿涙碍鐗遍張顑跨瑝鐡掔绱欓棁鈧?='+String(minEvents)+'鏉★紝绐楀彛 '+String(hours)+'h閿?+(parts.length?(' '+parts.join(' ')):'');
    return;
  }
  const sug=d.suggestions[0];
  if(!sug||!sug.weight_csv||!sug.maint_margin_csv){msg.textContent='鎷熷悎澶辫触锛氭棤鏈夋晥缁撴灉';return;}
  if(!lastCfg) lastCfg={};
  if(currentScope==='binance'){lastCfg.WeightCSVBinance=sug.weight_csv;lastCfg.MaintMarginCSVBinance=sug.maint_margin_csv;}
  else if(currentScope==='okx'){lastCfg.WeightCSVOKX=sug.weight_csv;lastCfg.MaintMarginCSVOKX=sug.maint_margin_csv;}
  else if(currentScope==='bybit'){lastCfg.WeightCSVBybit=sug.weight_csv;lastCfg.MaintMarginCSVBybit=sug.maint_margin_csv;}
  else {lastCfg.WeightCSV=sug.weight_csv;lastCfg.MaintMarginCSV=sug.maint_margin_csv;}
  scopeCleared=false;
  renderScope();
  msg.textContent='閹风喎鎮庣€瑰本鍨氶敍姘壉閺?'+String(sug.count||0)+' 閺?;
}

function clearScope(){
  const msg=document.getElementById('fitMsg');
  if(currentScope==='global'){
    if(msg) msg.textContent='鍏ㄥ眬涓嶅彲娓呯┖瑕嗙洊';
    return;
  }
  scopeCleared=true;
  if(msg) msg.textContent='瀹稿弶鐖ｇ拋鐗堢缁岄缚顩惄鏍电窗娣囨繂鐡ㄩ崥搴ゎ嚉娴溿倖妲楅幍鈧崶鐐衡偓鈧崗銊ョ湰';
}
async function reloadCfg(){
  const cfg=await fetch('/api/model-config').then(r=>r.json()).catch(()=>null);
  if(cfg) bind(cfg);
  reloadWindowLookback();
}
async function save(){
  const levs=[1,5,10,20,30,50,100];
  const levCSV=levs.join(',');
  const wList=[],mmList=[],fsList=[];
  for(const lv of levs){
    const w=Number((document.getElementById('w_'+lv)||{}).value||0); // percent
    const mm=Number((document.getElementById('mm_'+lv)||{}).value||0);
    const fs=Number((document.getElementById('fs_'+lv)||{}).value||0);
    wList.push(isFinite(w)?w:0);
    mmList.push(isFinite(mm)?mm:0);
    fsList.push(isFinite(fs)?fs:0);
  }
  const sumW=wList.reduce((a,b)=>a+b,0);
  const sumEl=document.getElementById('w_sum');if(sumEl)sumEl.textContent=(isFinite(sumW)?sumW:0).toFixed(2);
  // If clearing per-exchange overrides, don't validate current inputs.
  if(!(scopeCleared && currentScope!=='global')){
    if(!(sumW>0)){document.getElementById('msg').textContent='淇濆瓨澶辫触锛氭潈閲嶅悎璁″繀椤讳负 100.00%';return;}
    if(Math.abs(sumW-100)>0.01){document.getElementById('msg').textContent='娣囨繂鐡ㄦ径杈Е閿涙碍娼堥柌宥呮値鐠侊繝娓剁粵澶夌艾 100.00%閿涘苯缍嬮崜?'+sumW.toFixed(2)+'%';return;}
    for(let i=0;i<wList.length;i++) wList[i]=wList[i]/100.0;
    for(const v of mmList){if(!(v>0&&v<=0.02)){document.getElementById('msg').textContent='娣囨繂鐡ㄦ径杈Е閿涙氨娣幎銈勭箽鐠囦線鍣鹃悳鍥付閸?(0,0.02]';return;}}
  }else{
    for(let i=0;i<wList.length;i++) wList[i]=wList[i]/100.0;
  }
  for(const v of fsList){if(!(v>=1000&&v<=20000)){document.getElementById('msg').textContent='娣囨繂鐡ㄦ径杈Е閿涙俺绁柌鎴ｅ瀭閻滃洨缂夐弨鍓ч兇閺佷即娓堕崷?[1000,20000]';return;}}
  const mmCSV=(scopeCleared && currentScope!=='global')?'':mmList.map(v=>String(v)).join(',');
  const fsCSV=fsList.map(v=>String(v)).join(',');
  const wCSV=(scopeCleared && currentScope!=='global')?'':wList.map(v=>String(v)).join(',');
  const prev=(k)=>String((lastCfg&&lastCfg[k])||'').trim();
  const body={
    LookbackMin:Number((lastCfg&&lastCfg.LookbackMin)||1440),
    BucketMin:Number(document.getElementById('bucket').value||5),
    PriceStep:Number(document.getElementById('step').value||5),
    PriceRange:Number(document.getElementById('range').value||400),
    LeverageCSV:levCSV,
    WeightCSV:(currentScope==='global'?wCSV:prev('WeightCSV')),
    WeightCSVBinance:(currentScope==='binance'?(scopeCleared?'':wCSV):prev('WeightCSVBinance')),
    WeightCSVOKX:(currentScope==='okx'?(scopeCleared?'':wCSV):prev('WeightCSVOKX')),
    WeightCSVBybit:(currentScope==='bybit'?(scopeCleared?'':wCSV):prev('WeightCSVBybit')),
    MaintMargin:mmList[0]||0.005,
    MaintMarginCSV:(currentScope==='global'?mmCSV:prev('MaintMarginCSV')),
    MaintMarginCSVBinance:(currentScope==='binance'?(scopeCleared?'':mmCSV):prev('MaintMarginCSVBinance')),
    MaintMarginCSVOKX:(currentScope==='okx'?(scopeCleared?'':mmCSV):prev('MaintMarginCSVOKX')),
    MaintMarginCSVBybit:(currentScope==='bybit'?(scopeCleared?'':mmCSV):prev('MaintMarginCSVBybit')),
    FundingScale:fsList[0]||7000,
    FundingScaleCSV:fsCSV,
    IntensityScale:Number(document.getElementById('scale').value||1.0),
    DecayK:Number(document.getElementById('decay').value||2.2),
    NeighborShare:Number(document.getElementById('neighbor').value||0.28)
  };
  const r=await fetch('/api/model-config',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify(body)});
  if(r.ok){document.getElementById('msg').textContent='瀹歌弓绻氱€?;await reloadCfg();bindWeightSumLive();}
  else{document.getElementById('msg').textContent='淇濆瓨澶辫触';}
}
function bindWeightSumLive(){
  const levs=[1,5,10,20,30,50,100];
  const sumEl=document.getElementById('w_sum');
  if(!sumEl) return;
  function recalc(){
    const s=levs.map(lv=>Number((document.getElementById('w_'+lv)||{}).value||0)).reduce((a,b)=>a+(isFinite(b)?b:0),0);
    sumEl.textContent=(isFinite(s)?s:0).toFixed(2);
  }
  for(const lv of levs){
    const el=document.getElementById('w_'+lv);
    if(el) el.addEventListener('input',recalc);
  }
  recalc();
}
async function loadFooter(){try{const r=await fetch('/api/version');const v=await r.json();const el=document.getElementById('globalFooter');if(el)el.textContent='Code by Yuhao@jiansutech.com - '+(v.commit_time||'-')+' - '+(v.commit_id||'-')+' - '+(v.branch||'-');}catch(_){const el=document.getElementById('globalFooter');if(el)el.textContent='Code by Yuhao@jiansutech.com - - - -';}}
async function openUpgradeModal(){const m=document.getElementById('upgradeModal'),logEl=document.getElementById('upgradeLog'),foot=document.getElementById('upgradeFoot');if(!m||!logEl||!foot)return;m.classList.add('show');logEl.textContent='';foot.textContent='姝ｅ湪瑙﹀彂鍗囩骇...';const r=await fetch('/api/upgrade/pull',{method:'POST'});const d=await r.json().catch(()=>({error:'response parse failed',output:''}));if(d.error){logEl.textContent=String(d.output||'');foot.textContent='瑙﹀彂澶辫触: '+d.error;return;}foot.textContent='宸茶Е鍙戯紝姝ｅ湪鎵ц...';let stable=0;for(let i=0;i<180;i++){await new Promise(res=>setTimeout(res,1000));const pr=await fetch('/api/upgrade/progress').then(x=>x.json()).catch(()=>null);if(!pr)continue;logEl.textContent=String(pr.log||'');logEl.scrollTop=logEl.scrollHeight;if(pr.done){foot.textContent=(String(pr.exit_code||'')==='0')?'鍗囩骇瀹屾垚骞跺凡閲嶅惎':'閸楀洨楠囩€瑰本鍨氶敍宀勨偓鈧崙铏圭垳 '+String(pr.exit_code||'?');return;}if(!pr.running)stable++;else stable=0;if(stable>=3){foot.textContent='閸楀洨楠囨潻娑氣柤瀹歌尙绮ㄩ弶鐕傜礄閻樿埖鈧焦婀惌銉礆閿涘矁顕Λ鈧弻銉︽）韫?;return;}}foot.textContent='閸楀洨楠囨禒宥呮躬鏉╂稖顢戦敍宀冾嚞缁嬪秴鎮楅崘宥囨箙';}
function closeUpgradeModal(){const m=document.getElementById('upgradeModal');if(m)m.classList.remove('show');}
async function doUpgrade(event){if(event)event.preventDefault();openUpgradeModal();return false;}
initTheme();bind({LookbackMin:{{.LookbackMin}},BucketMin:{{.BucketMin}},PriceStep:{{.PriceStep}},PriceRange:{{.PriceRange}},LeverageCSV:{{printf "%q" .LeverageCSV}},WeightCSV:{{printf "%q" .WeightCSV}},MaintMargin:{{.MaintMargin}},MaintMarginCSV:{{printf "%q" .MaintMarginCSV}},FundingScale:{{.FundingScale}},FundingScaleCSV:{{printf "%q" .FundingScaleCSV}},IntensityScale:{{.IntensityScale}},DecayK:{{.DecayK}},NeighborShare:{{.NeighborShare}}});
bindWeightSumLive();
reloadCfg();
loadFooter();
</script><div id="upgradeModal" class="upgrade-modal"><div class="upgrade-card"><div class="upgrade-head"><div class="upgrade-title">鍗囩骇杩囩▼</div><button class="upgrade-close" onclick="closeUpgradeModal()">鍏抽棴</button></div><pre id="upgradeLog" class="upgrade-log"></pre><div id="upgradeFoot" class="upgrade-foot">绛夊緟寮€濮?..</div></div></div><div id="globalFooter" class="footer">Code by Yuhao@jiansutech.com - loading - loading - loading</div></body></html>`
