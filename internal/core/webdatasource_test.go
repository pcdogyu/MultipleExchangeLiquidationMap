package liqmap

import (
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
		"ETHBTC",
	}
	for _, value := range rejected {
		if isCoinglassETHSymbolValue(value) {
			t.Fatalf("expected %q to be rejected as ETH", value)
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
