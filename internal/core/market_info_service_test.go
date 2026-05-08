package liqmap

import "testing"

func TestBuildMarketInfoWindowsClassifiesNetLongIncrease(t *testing.T) {
	now := int64(1_700_000_000_000)
	series := MarketInfoSeries{
		OI: []MarketInfoOIPoint{
			{TS: now - 60*60*1000, OIValueUSD: 100_000_000},
			{TS: now, OIValueUSD: 110_000_000},
		},
		TopPosition: []MarketInfoRatioPoint{
			{TS: now - 60*60*1000, Ratio: 1.0, LongShare: 0.5, ShortShare: 0.5},
			{TS: now, Ratio: 1.5, LongShare: 0.6, ShortShare: 0.4},
		},
		Taker: []MarketInfoTakerPoint{
			{TS: now - 60*60*1000, CVD: 10},
			{TS: now, CVD: 40},
		},
	}

	windows := buildMarketInfoWindows(series)
	var oneHour MarketInfoWindow
	for _, w := range windows {
		if w.Label == "1h" {
			oneHour = w
		}
	}

	if oneHour.OIDeltaUSD != 10_000_000 {
		t.Fatalf("OI delta = %v, want 10000000", oneHour.OIDeltaUSD)
	}
	if oneHour.NetPositionDeltaUSD <= 0 {
		t.Fatalf("expected net long increase, got %v", oneHour.NetPositionDeltaUSD)
	}
	if oneHour.Verdict != "增仓上攻，多头主动增强" {
		t.Fatalf("unexpected verdict %q", oneHour.Verdict)
	}
}

func TestMarketInfoSharesFromRatio(t *testing.T) {
	longShare, shortShare := marketInfoSharesFromRatio(3)
	if longShare < 0.749 || longShare > 0.751 {
		t.Fatalf("long share = %v, want about 0.75", longShare)
	}
	if shortShare < 0.249 || shortShare > 0.251 {
		t.Fatalf("short share = %v, want about 0.25", shortShare)
	}
}

func TestMergeMarketInfoCurrentAddsTickerAndSpreadFields(t *testing.T) {
	base := MarketInfoCurrent{MarkPrice: 3000, OIQty: 100}
	live := MarketInfoCurrent{
		MarkPrice:         3010,
		PriceChangePct24h: -0.0123,
		QuoteVolume24h:    123_000_000,
		High24h:           3100,
		Low24h:            2950,
		BidPrice:          3009.99,
		AskPrice:          3010.01,
		BidAskSpread:      0.02,
		BidAskSpreadPct:   0.00000664,
	}

	got := mergeMarketInfoCurrent(base, live)
	if got.MarkPrice != live.MarkPrice {
		t.Fatalf("mark price = %v, want %v", got.MarkPrice, live.MarkPrice)
	}
	if got.PriceChangePct24h != live.PriceChangePct24h {
		t.Fatalf("24h change = %v, want %v", got.PriceChangePct24h, live.PriceChangePct24h)
	}
	if got.QuoteVolume24h != live.QuoteVolume24h || got.High24h != live.High24h || got.Low24h != live.Low24h {
		t.Fatalf("24h ticker fields not merged: %+v", got)
	}
	if got.BidPrice != live.BidPrice || got.AskPrice != live.AskPrice || got.BidAskSpread != live.BidAskSpread || got.BidAskSpreadPct != live.BidAskSpreadPct {
		t.Fatalf("book ticker fields not merged: %+v", got)
	}
}

func TestMarketInfoGammaExposureUSD(t *testing.T) {
	got := marketInfoGammaExposureUSD(3000, 0.0005, 100, 1)
	want := 4_500.0
	if got != want {
		t.Fatalf("gamma exposure = %v, want %v", got, want)
	}
}

func TestMarketInfoOptionExpirationCode(t *testing.T) {
	if got := marketInfoOptionExpirationCode(1782460800000); got != "260626" {
		t.Fatalf("expiration code = %q, want 260626", got)
	}
}
