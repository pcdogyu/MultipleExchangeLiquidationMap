package main

import (
	"strings"
	"testing"
)

func TestBuildHeatReportDataFromModelAtPriceReanchorsBandsAndCumulative(t *testing.T) {
	model := map[string]any{
		"generated_at":   float64(1234567890),
		"current_price":  100.0,
		"prices":         []float64{95, 100, 110, 115},
		"intensity_grid": [][]float64{{6}, {4}, {5}, {3}},
	}

	report := buildHeatReportDataFromModelAtPrice(model, 105)

	if report.CurrentPrice != 105 {
		t.Fatalf("expected current price 105, got %v", report.CurrentPrice)
	}
	if report.GeneratedAt != 1234567890 {
		t.Fatalf("expected generated_at to be preserved, got %d", report.GeneratedAt)
	}

	if report.LongPeak.Price != 95 || report.LongPeak.SingleUSD != 6 || report.LongPeak.CumulativeUSD != 10 {
		t.Fatalf("unexpected long peak: %+v", report.LongPeak)
	}
	if report.ShortPeak.Price != 110 || report.ShortPeak.SingleUSD != 5 || report.ShortPeak.CumulativeUSD != 5 {
		t.Fatalf("unexpected short peak: %+v", report.ShortPeak)
	}

	b20 := heatReportBandBySize(report, 20)
	if b20.UpPrice != 125 || b20.DownPrice != 85 {
		t.Fatalf("unexpected 20pt band prices: %+v", b20)
	}
	if b20.UpNotionalUSD != 8 || b20.DownNotionalUSD != 10 || b20.DiffUSD != 2 {
		t.Fatalf("unexpected 20pt band notionals: %+v", b20)
	}
}

func TestBuildMonitorHeatBandsFromWebMapAtPriceUsesCumulativeBands(t *testing.T) {
	webMap := WebDataSourceMapResponse{
		HasData:      true,
		CurrentPrice: 100,
		Points: []WebDataSourcePoint{
			{Side: "short", Price: 110, LiqValue: 3},
			{Side: "short", Price: 120, LiqValue: 4},
			{Side: "short", Price: 130, LiqValue: 5},
			{Side: "long", Price: 90, LiqValue: 6},
			{Side: "long", Price: 80, LiqValue: 7},
			{Side: "long", Price: 70, LiqValue: 8},
		},
	}

	bands := buildMonitorHeatBandsFromWebMapAtPrice(webMap, 100)
	b20 := heatBandBySize(bands, 20)
	if b20.UpPrice != 120 || b20.DownPrice != 80 {
		t.Fatalf("unexpected 20pt prices: %+v", b20)
	}
	if b20.UpNotionalUSD != 7 || b20.DownNotionalUSD != 13 || b20.DiffUSD != 6 {
		t.Fatalf("unexpected 20pt cumulative values: %+v", b20)
	}
}

func TestBuildTelegramThirtyDayTextV4UsesOneDecimalPrices(t *testing.T) {
	app := &App{}
	monitor := HeatReportData{
		CurrentPrice:  3500.12,
		LongTotalUSD:  2.4e8,
		ShortTotalUSD: 1.8e8,
		LongPeak: HeatReportPeak{
			Price:         3490.34,
			Distance:      9.78,
			SingleUSD:     0.9e8,
			CumulativeUSD: 1.3e8,
		},
		ShortPeak: HeatReportPeak{
			Price:         3510.56,
			Distance:      10.44,
			SingleUSD:     0.7e8,
			CumulativeUSD: 1.1e8,
		},
	}

	text := app.buildTelegramThirtyDayTextV4(monitor, nil, WebDataSourceMapResponse{})

	for _, want := range []string{"现价$3500.1", "价格$3490.3", "价格$3510.6"} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected telegram text to contain %q, got:\n%s", want, text)
		}
	}
	for _, unwanted := range []string{"3500.12", "3490.34", "3510.56"} {
		if strings.Contains(text, unwanted) {
			t.Fatalf("expected telegram text not to contain %q, got:\n%s", unwanted, text)
		}
	}
}

func TestBuildHeatReportTableHTMLOrdersLowerLongsBeforeUpperShorts(t *testing.T) {
	html := buildHeatReportTableHTML(HeatReportData{
		GeneratedAt:  1234567890,
		CurrentPrice: 3500.12,
		LongPeak:     HeatReportPeak{Price: 3490.34, SingleUSD: 1.5e8},
		ShortPeak:    HeatReportPeak{Price: 3510.56, SingleUSD: 1.2e8},
		Bands:        []HeatReportBand{{Band: 20, DownPrice: 3480.44, DownNotionalUSD: 1.8e8, UpPrice: 3520.66, UpNotionalUSD: 1.1e8, DiffUSD: 0.7e8}},
	})

	lowerIdx := strings.Index(html, "Lower Longs")
	upperIdx := strings.Index(html, "Upper Shorts")
	if lowerIdx < 0 || upperIdx < 0 || lowerIdx > upperIdx {
		t.Fatalf("expected Lower Longs before Upper Shorts, got:\n%s", html)
	}
	rowIdx := strings.Index(html, ">3480.4<")
	upIdx := strings.Index(html, ">3520.7<")
	if rowIdx < 0 || upIdx < 0 || rowIdx > upIdx {
		t.Fatalf("expected lower-long price before upper-short price, got:\n%s", html)
	}
	for _, unwanted := range []string{"3500.12", "3480.44", "3520.66"} {
		if strings.Contains(html, unwanted) {
			t.Fatalf("expected heat report html not to contain %q, got:\n%s", unwanted, html)
		}
	}
}
