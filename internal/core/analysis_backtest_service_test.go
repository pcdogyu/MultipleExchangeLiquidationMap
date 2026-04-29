package liqmap

import (
	"reflect"
	"testing"
	"time"
)

func TestAnalysisBacktestHorizonsForSignalTrimmedByOppositeSignal(t *testing.T) {
	baseTS := time.Date(2026, 4, 29, 10, 0, 0, 0, time.UTC).UnixMilli()
	minutes := func(v int) int64 {
		return int64(v) * int64(time.Minute/time.Millisecond)
	}
	record := func(id int64, offsetMin int, direction string) AnalysisSignalRecord {
		return AnalysisSignalRecord{
			ID:        id,
			SignalTS:  baseTS + minutes(offsetMin),
			Direction: direction,
		}
	}

	tests := []struct {
		name    string
		records []AnalysisSignalRecord
		want    []int
	}{
		{
			name: "opposite at five minutes keeps minimum horizon",
			records: []AnalysisSignalRecord{
				record(1, 0, "up"),
				record(2, 5, "down"),
			},
			want: []int{5},
		},
		{
			name: "opposite at thirty minutes excludes thirty and sixty",
			records: []AnalysisSignalRecord{
				record(1, 0, "up"),
				record(2, 30, "down"),
			},
			want: []int{5, 15},
		},
		{
			name: "opposite at forty five minutes keeps completed horizons",
			records: []AnalysisSignalRecord{
				record(1, 0, "up"),
				record(2, 45, "down"),
			},
			want: []int{5, 15, 30},
		},
		{
			name: "same direction before opposite does not stop scan",
			records: []AnalysisSignalRecord{
				record(1, 0, "up"),
				record(2, 5, "up"),
				record(3, 20, "down"),
			},
			want: []int{5, 15},
		},
		{
			name: "flat signal does not trim horizons",
			records: []AnalysisSignalRecord{
				record(1, 0, "up"),
				record(2, 5, "flat"),
				record(3, 70, "up"),
			},
			want: []int{5, 15, 30, 60},
		},
		{
			name: "flat before opposite is ignored",
			records: []AnalysisSignalRecord{
				record(1, 0, "up"),
				record(2, 5, "flat"),
				record(3, 30, "down"),
			},
			want: []int{5, 15},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := analysisBacktestHorizonsForSignal(tt.records, 0)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("analysisBacktestHorizonsForSignal() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestAnalysisBacktestHorizonStatsSkipTrimmedHorizons(t *testing.T) {
	stats := buildAnalysisBacktestHorizonStats([]AnalysisSignalResult{
		{
			Horizons: []AnalysisSignalHorizonResult{
				{HorizonMin: 5, Result: "correct"},
				{HorizonMin: 15, Result: "wrong"},
			},
		},
	})

	totalByHorizon := map[int]int{}
	for _, stat := range stats {
		totalByHorizon[stat.HorizonMin] = stat.TotalSignals
	}
	want := map[int]int{5: 1, 15: 1, 30: 0, 60: 0}
	if !reflect.DeepEqual(totalByHorizon, want) {
		t.Fatalf("horizon totals = %v, want %v", totalByHorizon, want)
	}
}

func TestAnalysisBacktestStatsUseConfidenceFilteredSignals(t *testing.T) {
	results := []AnalysisSignalResult{
		backtestSignalForTest(65, "correct", map[int]string{5: "correct", 15: "wrong"}),
		backtestSignalForTest(70, "wrong", map[int]string{5: "wrong"}),
		backtestSignalForTest(85, "wrong", map[int]string{5: "wrong", 15: "correct", 30: "correct"}),
		backtestSignalForTest(91, "correct", map[int]string{5: "correct", 15: "correct", 30: "wrong", 60: "pending"}),
	}

	filtered := filterAnalysisSignalsByConfidence(results, 70)
	summary := buildAnalysisBacktestSummary(filtered, 24)
	if summary.TotalSignals != 2 || summary.CorrectCount != 1 || summary.WrongCount != 1 || summary.CorrectRate != 50 {
		t.Fatalf("filtered summary = %+v, want total=2 correct=1 wrong=1 rate=50", summary)
	}

	stats := buildAnalysisBacktestHorizonStats(filtered)
	totalByHorizon := map[int]int{}
	rateByHorizon := map[int]float64{}
	pendingByHorizon := map[int]int{}
	for _, stat := range stats {
		totalByHorizon[stat.HorizonMin] = stat.TotalSignals
		rateByHorizon[stat.HorizonMin] = stat.CorrectRate
		pendingByHorizon[stat.HorizonMin] = stat.PendingCount
	}
	if !reflect.DeepEqual(totalByHorizon, map[int]int{5: 2, 15: 2, 30: 2, 60: 1}) {
		t.Fatalf("filtered horizon totals = %v", totalByHorizon)
	}
	if !reflect.DeepEqual(rateByHorizon, map[int]float64{5: 50, 15: 100, 30: 50, 60: 0}) {
		t.Fatalf("filtered horizon rates = %v", rateByHorizon)
	}
	if pendingByHorizon[60] != 1 {
		t.Fatalf("expected 1 pending 60m horizon, got %d", pendingByHorizon[60])
	}

	filtered = filterAnalysisSignalsByConfidence(results, 90)
	summary = buildAnalysisBacktestSummary(filtered, 24)
	if summary.TotalSignals != 1 || summary.CorrectCount != 1 || summary.CorrectRate != 100 {
		t.Fatalf("90+ summary = %+v, want total=1 correct=1 rate=100", summary)
	}
}

func TestParseAnalysisBacktestCandlesKeepsQuoteVolume(t *testing.T) {
	candles := parseAnalysisBacktestCandles([][]any{
		{int64(1000), "1", "2", "0.5", "1.5", "10", int64(1999), "12345.6"},
		{"2000", "2", "3", "1.5", "2.5", "6789.1"},
	})
	if len(candles) != 2 {
		t.Fatalf("parsed candle count = %d, want 2", len(candles))
	}
	if candles[0].QuoteVolume != 12345.6 {
		t.Fatalf("binance quote volume = %v, want 12345.6", candles[0].QuoteVolume)
	}
	if candles[1].QuoteVolume != 6789.1 {
		t.Fatalf("okx fallback quote volume = %v, want 6789.1", candles[1].QuoteVolume)
	}
}

func TestClassifyAnalysisSecondFactorUsesRelativeVolume(t *testing.T) {
	baseTS := time.Date(2026, 4, 29, 10, 0, 0, 0, time.UTC).UnixMilli()
	const step = int64(5 * time.Minute / time.Millisecond)
	record := AnalysisSignalRecord{SignalTS: baseTS + 12*step}
	candlesWithCurrent := func(currentVolume float64) []analysisBacktestCandle {
		candles := make([]analysisBacktestCandle, 0, 13)
		for i := 0; i < 12; i++ {
			candles = append(candles, analysisBacktestCandle{TS: baseTS + int64(i)*step, C: 2200, QuoteVolume: 100})
		}
		candles = append(candles, analysisBacktestCandle{TS: record.SignalTS, C: 2200, QuoteVolume: currentVolume})
		return candles
	}

	tests := []struct {
		name          string
		candles       []analysisBacktestCandle
		signalTS      int64
		wantFactorKey string
	}{
		{name: "spike at one point five times average", candles: candlesWithCurrent(150), wantFactorKey: analysisSecondFactorVolumeSpike},
		{name: "normal includes point eight boundary", candles: candlesWithCurrent(80), wantFactorKey: analysisSecondFactorVolumeNormal},
		{name: "low below point eight average", candles: candlesWithCurrent(79.9), wantFactorKey: analysisSecondFactorVolumeLow},
		{name: "missing current volume is insufficient", candles: candlesWithCurrent(0), wantFactorKey: analysisSecondFactorInsufficient},
		{name: "short lookback is insufficient", candles: candlesWithCurrent(150)[:12], signalTS: baseTS + 11*step, wantFactorKey: analysisSecondFactorInsufficient},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			item := record
			if tt.signalTS > 0 {
				item.SignalTS = tt.signalTS
			}
			got, _ := classifyAnalysisSecondFactor(item, tt.candles)
			if got != tt.wantFactorKey {
				t.Fatalf("factor key = %q, want %q", got, tt.wantFactorKey)
			}
		})
	}
}

func TestAnalysisBacktest2FAStatsUseConfidenceFilteredSignals(t *testing.T) {
	base := []AnalysisSignalResult{
		backtestSignalForTest(65, "correct", map[int]string{5: "correct", 15: "correct"}),
		backtestSignalForTest(60, "wrong", map[int]string{5: "wrong"}),
		backtestSignalForTest(95, "correct", map[int]string{5: "correct"}),
	}
	base[0].SecondFactorKey = analysisSecondFactorVolumeSpike
	base[0].SecondFactorLabel = analysisSecondFactorLabel(analysisSecondFactorVolumeSpike)
	base[1].SecondFactorKey = analysisSecondFactorVolumeSpike
	base[1].SecondFactorLabel = analysisSecondFactorLabel(analysisSecondFactorVolumeSpike)
	base[2].SecondFactorKey = analysisSecondFactorVolumeLow
	base[2].SecondFactorLabel = analysisSecondFactorLabel(analysisSecondFactorVolumeLow)

	candidates := make([]AnalysisSignalResult, 0, len(base))
	for _, item := range base {
		candidate, ok := buildAnalysisDualFactorCandidate(item)
		if ok {
			candidates = append(candidates, candidate)
		}
	}
	filtered := filterAnalysisSignalsBySecondFactor(filterAnalysisSignalsByConfidence(candidates, 70), analysisSecondFactorVolumeSpike)
	summary := buildAnalysisBacktestSummary(filtered, 24)
	if summary.TotalSignals != 1 || summary.CorrectCount != 1 || summary.CorrectRate != 100 {
		t.Fatalf("2FA filtered summary = %+v, want only volume spike signal above 70", summary)
	}
	stats := buildAnalysisBacktestHorizonStats(filtered)
	for _, stat := range stats {
		if stat.HorizonMin == 15 && stat.TotalSignals != 1 {
			t.Fatalf("2FA 15m total = %d, want 1", stat.TotalSignals)
		}
	}
}

func TestDefaultAnalysisSecondFactorSelectionUsesAll(t *testing.T) {
	if got := defaultAnalysisSecondFactorSelection(""); got != analysisSecondFactorAll {
		t.Fatalf("empty second factor default = %q, want %q", got, analysisSecondFactorAll)
	}
	if got := defaultAnalysisSecondFactorSelection("aligned"); got != analysisSecondFactorAll {
		t.Fatalf("legacy second factor = %q, want %q", got, analysisSecondFactorAll)
	}
	if got := defaultAnalysisSecondFactorSelection(analysisSecondFactorVolumeSpike); got != analysisSecondFactorVolumeSpike {
		t.Fatalf("explicit second factor = %q, want %q", got, analysisSecondFactorVolumeSpike)
	}
}

func TestLegacyAnalysisSecondFactorFilterFallsBackToAll(t *testing.T) {
	items := []AnalysisSignalResult{
		{SecondFactorKey: analysisSecondFactorVolumeSpike},
		{SecondFactorKey: analysisSecondFactorVolumeLow},
	}
	filtered := filterAnalysisSignalsBySecondFactor(items, "counter")
	if len(filtered) != len(items) {
		t.Fatalf("legacy factor filter returned %d rows, want %d", len(filtered), len(items))
	}
}

func backtestSignalForTest(confidence float64, result string, horizons map[int]string) AnalysisSignalResult {
	out := AnalysisSignalResult{
		Confidence: confidence,
		Result:     result,
	}
	for _, horizonMin := range analysisBacktestHorizons {
		horizonResult, ok := horizons[horizonMin]
		if !ok {
			continue
		}
		out.Horizons = append(out.Horizons, AnalysisSignalHorizonResult{
			HorizonMin: horizonMin,
			Result:     horizonResult,
		})
	}
	return out
}
