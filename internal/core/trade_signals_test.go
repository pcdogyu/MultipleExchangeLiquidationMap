package liqmap

import "testing"

func tradePoint(ts int64, shortActive bool, shortDir string, contActive bool, contDir string) tradeSignalSyncPoint {
	return tradeSignalSyncPoint{
		TS:            ts,
		Price:         2200 + float64(ts),
		ShortActive:   shortActive,
		ShortDir:      shortDir,
		ShortStrength: 45,
		ContActive:    contActive,
		ContDir:       contDir,
		ContStrength:  35,
	}
}

func assertTradeSignal(t *testing.T, got TradeSignal, side, action string) {
	t.Helper()
	if got.Side != side || got.Action != action {
		t.Fatalf("signal = %s/%s, want %s/%s: %+v", got.Side, got.Action, side, action, got)
	}
}

func TestBuildTradeSignalsLongLifecycle(t *testing.T) {
	signals := buildTradeSignalsFromSyncPoints([]tradeSignalSyncPoint{
		tradePoint(1, false, "", false, ""),
		tradePoint(2, true, "up", false, ""),
		tradePoint(3, true, "up", true, "up"),
		tradePoint(4, false, "", true, "up"),
		tradePoint(5, true, "down", true, "up"),
	})

	if len(signals) != 5 {
		t.Fatalf("signal count = %d, want 5: %+v", len(signals), signals)
	}
	assertTradeSignal(t, signals[0], "long", "open")
	assertTradeSignal(t, signals[1], "long", "add")
	assertTradeSignal(t, signals[2], "long", "reduce")
	assertTradeSignal(t, signals[3], "long", "close")
	assertTradeSignal(t, signals[4], "short", "open")
}

func TestBuildTradeSignalsShortLifecycle(t *testing.T) {
	signals := buildTradeSignalsFromSyncPoints([]tradeSignalSyncPoint{
		tradePoint(1, false, "", false, ""),
		tradePoint(2, true, "down", false, ""),
		tradePoint(3, true, "down", true, "down"),
		tradePoint(4, false, "", true, "down"),
		tradePoint(5, true, "up", true, "down"),
	})

	if len(signals) != 5 {
		t.Fatalf("signal count = %d, want 5: %+v", len(signals), signals)
	}
	assertTradeSignal(t, signals[0], "short", "open")
	assertTradeSignal(t, signals[1], "short", "add")
	assertTradeSignal(t, signals[2], "short", "reduce")
	assertTradeSignal(t, signals[3], "short", "close")
	assertTradeSignal(t, signals[4], "long", "open")
}

func TestSyncPointFromBucketsUsesLiquidationSyncThreshold(t *testing.T) {
	point := syncPointFromBuckets(1, 2200, []LiquidationPeriodBucket{
		{Label: "1H", Hours: 1, TotalUSD: 100, LongUSD: 20, ShortUSD: 80, PricePush: "up", BalanceRatio: 0.60},
		{Label: "4H", Hours: 4, TotalUSD: 120, LongUSD: 40, ShortUSD: 80, PricePush: "up", BalanceRatio: 0.33},
		{Label: "12H", Hours: 12, TotalUSD: 100, LongUSD: 44, ShortUSD: 56, PricePush: "up", BalanceRatio: 0.12},
		{Label: "24H", Hours: 24, TotalUSD: 100, LongUSD: 20, ShortUSD: 80, PricePush: "up", BalanceRatio: 0.60},
	})

	if !point.ShortActive || point.ShortDir != "up" {
		t.Fatalf("expected short-term up active, got %+v", point)
	}
	if point.ContActive {
		t.Fatalf("expected continuation inactive below threshold, got %+v", point)
	}
}
