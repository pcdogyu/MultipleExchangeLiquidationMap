package liqmap

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWebDataSourcePayloadCurrentPrice(t *testing.T) {
	payload := map[string]any{
		"lastPrice": 2338.42,
		"rangeLow":  2200.0,
		"rangeHigh": 2500.0,
	}
	if got := webDataSourcePayloadCurrentPrice(payload, 2200, 2500); got != 2338.4 {
		t.Fatalf("expected payload price 2338.4, got %v", got)
	}

	if got := webDataSourcePayloadCurrentPrice(map[string]any{}, 2200, 2500); got != 2350.0 {
		t.Fatalf("expected midpoint fallback 2350.0, got %v", got)
	}
}

func TestIsCoinglassETHSymbolValueAcceptsPairDisplayNames(t *testing.T) {
	accepted := []string{
		"ETH",
		"ETHUSDT",
		"ETH/USDT",
		"Binance ETH/USDT 永续",
		"Binance ETH-USDT Perpetual",
	}
	for _, value := range accepted {
		if !isCoinglassETHSymbolValue(value) {
			t.Fatalf("expected %q to be accepted as ETH", value)
		}
	}

	rejected := []string{
		"",
		"BTC",
		"BTCUSDT",
		"Binance BTC/USDT 永续",
		"Binance BTC/USDT 永续 ETH",
		"ETHBTC",
	}
	for _, value := range rejected {
		if isCoinglassETHSymbolValue(value) {
			t.Fatalf("expected %q to be rejected as ETH", value)
		}
	}
}

func TestValidateWebDataSourceETHPayloadRangeRejectsBTCPrices(t *testing.T) {
	if err := validateWebDataSourceETHPayloadRange(1565.9, 1933.8); err != nil {
		t.Fatalf("expected ETH price range to pass, got %v", err)
	}
	if err := validateWebDataSourceETHPayloadRange(56611, 69670); err == nil {
		t.Fatal("expected BTC-like price range to be rejected")
	}
}

func TestNormalizeWebDataSourcePayloadAcceptsChartFallbackPayload(t *testing.T) {
	payload := map[string]any{
		"source":    "echarts",
		"lastPrice": 1752.0,
		"rangeLow":  1740.0,
		"rangeHigh": 1765.0,
		"long": []any{
			map[string]any{"exchange": "Binance", "price": 1748.5, "value": 1200000.0},
		},
		"short": []any{
			map[string]any{"exchange": "Binance", "price": 1758.5, "value": 2300000.0},
		},
	}

	points, low, high := normalizeWebDataSourcePayload(payload)
	if low != 1740.0 || high != 1765.0 {
		t.Fatalf("expected range [1740,1765], got [%v,%v]", low, high)
	}
	if len(points) != 2 {
		t.Fatalf("expected 2 chart fallback points, got %d", len(points))
	}
	if points[0].Side != "long" || points[1].Side != "short" {
		t.Fatalf("expected long then short points, got %+v", points)
	}
}

func TestWebDataSourceChartFallbackSearchesReactOwnerState(t *testing.T) {
	script := webDataSourceExtractChartPayloadJS(webDataSourceFindTargetPanelJS())
	for _, marker := range []string{
		"payloadFromCandidate",
		"reactfiber",
		"fiber.return",
		"react-owner",
		"react-state",
	} {
		if !strings.Contains(script, marker) {
			t.Fatalf("expected chart fallback script to include %q", marker)
		}
	}
}

func TestWebDataSourceUpgradeControlDoesNotNavigateToConfig(t *testing.T) {
	body := WebDataSourceHTML()

	if strings.Contains(body, `href="/config" style`) && strings.Contains(body, `>升级</a>`) {
		t.Fatalf("expected webdatasource upgrade control to avoid config navigation")
	}
	if !strings.Contains(body, `onclick="return doUpgrade(event)"`) || !strings.Contains(body, `/api/upgrade/pull`) {
		t.Fatalf("expected webdatasource upgrade control to trigger upgrade API")
	}
}

func TestWebDataSourceScheduleHonorsConfiguredInterval(t *testing.T) {
	now := time.Date(2026, 5, 18, 17, 7, 30, 0, time.Local)

	latest := time.UnixMilli(latestScheduledWebDataSourceCaptureTSForInterval(now, 15))
	if latest.Minute() != 0 {
		t.Fatalf("expected latest 15-minute slot at minute 0, got %s", latest.Format("15:04:05"))
	}

	next := time.UnixMilli(nextScheduledWebDataSourceCaptureTSForInterval(now, 15))
	if next.Minute() != 15 {
		t.Fatalf("expected next 15-minute slot at minute 15, got %s", next.Format("15:04:05"))
	}

	latest = time.UnixMilli(latestScheduledWebDataSourceCaptureTSForInterval(now, 5))
	if latest.Minute() != 5 {
		t.Fatalf("expected latest 5-minute slot at minute 5, got %s", latest.Format("15:04:05"))
	}

	next = time.UnixMilli(nextScheduledWebDataSourceCaptureTSForInterval(now, 5))
	if next.Minute() != 10 {
		t.Fatalf("expected next 5-minute slot at minute 10, got %s", next.Format("15:04:05"))
	}
}

func TestWebDataSourceScheduleFallbackIntervalIsSixtyMinutes(t *testing.T) {
	now := time.Date(2026, 5, 18, 17, 7, 30, 0, time.Local)

	next := time.UnixMilli(nextScheduledWebDataSourceCaptureTSForInterval(now, 0))
	if next.Hour() != 18 || next.Minute() != 0 {
		t.Fatalf("expected fallback next slot at 18:00, got %s", next.Format("15:04:05"))
	}
}

func TestCloneChromeProfileForCaptureSkipsHeavyCacheDirs(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "coinglass_profile")
	target := filepath.Join(root, "runtime")
	if err := os.MkdirAll(filepath.Join(source, "Default", "Cache"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(source, "Default", "Service Worker"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(source, "Default", "Local Storage"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "Local State"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "Default", "Cache", "big.bin"), []byte("cache"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "Default", "Service Worker", "cache.bin"), []byte("cache"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "Default", "Local Storage", "login"), []byte("token"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := cloneChromeProfileForCapture(context.Background(), source, target); err != nil {
		t.Fatalf("cloneChromeProfileForCapture: %v", err)
	}
	if _, err := os.Stat(filepath.Join(target, "Default", "Local Storage", "login")); err != nil {
		t.Fatalf("expected local storage to be copied: %v", err)
	}
	if _, err := os.Stat(filepath.Join(target, "Default", "Cache", "big.bin")); !os.IsNotExist(err) {
		t.Fatalf("expected cache file to be skipped, stat err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(target, "Default", "Service Worker", "cache.bin")); !os.IsNotExist(err) {
		t.Fatalf("expected service worker cache to be skipped, stat err=%v", err)
	}
}

func TestCloneChromeProfileForCaptureHonorsCanceledContext(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "coinglass_profile")
	target := filepath.Join(root, "runtime")
	if err := os.MkdirAll(filepath.Join(source, "Default"), 0o755); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := cloneChromeProfileForCapture(ctx, source, target); err != context.Canceled {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}
