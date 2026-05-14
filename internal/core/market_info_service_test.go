package liqmap

import (
	"database/sql"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	dbpkg "multipleexchangeliquidationmap/internal/platform/db"

	_ "modernc.org/sqlite"
)

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

func TestFetchBinanceMarketInfoCurrentUsesFuturesBookTickerEndpoint(t *testing.T) {
	seenPaths := map[string]bool{}
	app := &App{
		httpClient: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			seenPaths[req.URL.Path] = true
			body := "{}"
			switch req.URL.Path {
			case "/fapi/v1/premiumIndex":
				body = `{"markPrice":"3000","lastFundingRate":"0.0001","estimatedSettlePrice":"3001","nextFundingTime":1700000000000,"time":1699999900000}`
			case "/fapi/v1/openInterest":
				body = `{"openInterest":"100","time":1699999900000}`
			case "/fapi/v1/ticker/24hr":
				body = `{"priceChangePercent":"1.23","highPrice":"3100","lowPrice":"2950","quoteVolume":"1000000","closeTime":1699999950000}`
			case "/fapi/v1/ticker/bookTicker":
				body = `{"bidPrice":"2999.90","askPrice":"3000.10","time":1700000000000}`
			default:
				return &http.Response{
					StatusCode: http.StatusNotFound,
					Status:     "404 Not Found",
					Body:       io.NopCloser(strings.NewReader("not found")),
					Header:     make(http.Header),
				}, nil
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Status:     "200 OK",
				Body:       io.NopCloser(strings.NewReader(body)),
				Header:     make(http.Header),
			}, nil
		})},
	}

	current, warnings, err := app.fetchBinanceMarketInfoCurrent("ETHUSDT")
	if err != nil {
		t.Fatalf("fetch current: %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("unexpected warnings: %v", warnings)
	}
	if !seenPaths["/fapi/v1/ticker/bookTicker"] {
		t.Fatalf("book ticker endpoint was not requested; paths=%v", seenPaths)
	}
	if seenPaths["/fapi/v1/bookTicker"] {
		t.Fatalf("old book ticker endpoint was requested; paths=%v", seenPaths)
	}
	if current.BidPrice != 2999.90 || current.AskPrice != 3000.10 || current.BidAskSpread <= 0 {
		t.Fatalf("book ticker fields not populated: %+v", current)
	}
}

func TestMarketInfoUsesFreshSQLiteCache(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()
	if err := dbpkg.Configure(db); err != nil {
		t.Fatalf("configure db: %v", err)
	}
	if err := dbpkg.Init(db); err != nil {
		t.Fatalf("init db: %v", err)
	}

	calledHTTP := false
	app := NewApp(db, false)
	app.httpClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		calledHTTP = true
		return &http.Response{
			StatusCode: http.StatusInternalServerError,
			Status:     "500 Internal Server Error",
			Body:       io.NopCloser(strings.NewReader("unexpected")),
			Header:     make(http.Header),
		}, nil
	})}

	cached := newMarketInfoResponse("ETHUSDT", "5m", 2)
	cached.GeneratedAt = time.Now().UnixMilli()
	cached.Current = MarketInfoCurrent{
		MarkPrice:          3000,
		BidPrice:           2999.9,
		AskPrice:           3000.1,
		BidAskSpread:       0.2,
		CurrentDataQuality: "live",
	}
	cached.Series.OI = []MarketInfoOIPoint{{TS: cached.GeneratedAt, OIValueUSD: 100_000}}
	if err := app.saveMarketInfoCache(cached, "ready", ""); err != nil {
		t.Fatalf("save cache: %v", err)
	}

	got, err := app.MarketInfo("ETHUSDT", "5m", 2)
	if err != nil {
		t.Fatalf("market info: %v", err)
	}
	if calledHTTP {
		t.Fatalf("fresh cache should not synchronously call HTTP")
	}
	if got.CacheStatus != "ready" || got.Refreshing {
		t.Fatalf("unexpected cache status=%q refreshing=%v", got.CacheStatus, got.Refreshing)
	}
	if got.Current.CurrentDataQuality != "cache" {
		t.Fatalf("cached response quality = %q, want cache", got.Current.CurrentDataQuality)
	}
	if got.Current.MarkPrice != 3000 || got.Current.BidAskSpread != 0.2 {
		t.Fatalf("unexpected cached current: %+v", got.Current)
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
