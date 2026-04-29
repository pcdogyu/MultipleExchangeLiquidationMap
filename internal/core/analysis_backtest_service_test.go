package liqmap

import (
	"fmt"
	"reflect"
	"strings"
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

func TestAnalysisSignalHighQualityFilter(t *testing.T) {
	summary := func(scores ...float64) string {
		parts := make([]string, 0, len(scores))
		for i, score := range scores {
			label := []string{"1D", "7D", "30D"}[i]
			parts = append(parts, label+" 权重 33% / 分数 "+fmt.Sprintf("%+.1f", score)+" / 有效带 1")
		}
		return strings.Join(parts, " | ")
	}
	rows := []AnalysisSignalResult{
		{Direction: "up", Confidence: 69.9, Summary: summary(80, 70)},
		{Direction: "up", Confidence: 72, Summary: summary(90)},
		{Direction: "down", Confidence: 72, Summary: summary(-90, -80)},
		{Direction: "down", Confidence: 82, Summary: summary(-90)},
		{Direction: "up", Confidence: 88, Summary: summary(95, 0)},
		{Direction: "up", Confidence: 88, Summary: summary(95, 85)},
		{Direction: "up", Confidence: 88, Summary: summary(95, 85, 75)},
		{Direction: "down", Confidence: 90, Summary: summary(-90, 80, -85)},
	}
	filtered := filterAnalysisSignalsByQuality(rows, analysisBacktestQualityHigh)
	if len(filtered) != 3 {
		t.Fatalf("high quality filtered count = %d, want 3", len(filtered))
	}
	if filtered[0].Confidence != 72 || filtered[1].Confidence != 82 || filtered[2].Confidence != 88 {
		t.Fatalf("unexpected high quality rows: %+v", filtered)
	}

	all := filterAnalysisSignalsByQuality(rows, analysisBacktestQualityAll)
	if len(all) != len(rows) {
		t.Fatalf("all quality count = %d, want %d", len(all), len(rows))
	}
}

func TestHighQualityStatsUseFilteredSignals(t *testing.T) {
	results := []AnalysisSignalResult{
		{
			Direction:  "up",
			Confidence: 65,
			Result:     "correct",
			Summary:    "1D 权重 33% / 分数 +80.0 / 有效带 1 | 7D 权重 33% / 分数 +70.0 / 有效带 1",
			Horizons: []AnalysisSignalHorizonResult{
				{HorizonMin: 5, Result: "correct"},
			},
		},
		{
			Direction:  "up",
			Confidence: 76,
			Result:     "wrong",
			Summary:    "1D 权重 33% / 分数 +80.0 / 有效带 1",
			Horizons: []AnalysisSignalHorizonResult{
				{HorizonMin: 5, Result: "wrong"},
				{HorizonMin: 15, Result: "correct"},
			},
		},
		{
			Direction:  "up",
			Confidence: 90,
			Result:     "correct",
			Summary:    "1D 权重 33% / 分数 +90.0 / 有效带 1",
			Horizons: []AnalysisSignalHorizonResult{
				{HorizonMin: 5, Result: "correct"},
			},
		},
	}
	filtered := filterAnalysisSignalsByQuality(results, analysisBacktestQualityHigh)
	summary := buildAnalysisBacktestSummary(filtered, 24)
	if summary.TotalSignals != 1 || summary.WrongCount != 1 {
		t.Fatalf("high quality summary = %+v, want only the 70-85 signal", summary)
	}
	stats := buildAnalysisBacktestHorizonStats(filtered)
	totalByHorizon := map[int]int{}
	for _, stat := range stats {
		totalByHorizon[stat.HorizonMin] = stat.TotalSignals
	}
	if !reflect.DeepEqual(totalByHorizon, map[int]int{5: 1, 15: 1, 30: 0, 60: 0}) {
		t.Fatalf("high quality horizon totals = %v", totalByHorizon)
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
		{name: "spike at one point two times average", candles: candlesWithCurrent(120), wantFactorKey: analysisSecondFactorVolumeSpike},
		{name: "normal includes point eight boundary", candles: candlesWithCurrent(80), wantFactorKey: analysisSecondFactorVolumeNormal},
		{name: "normal includes one point nineteen boundary", candles: candlesWithCurrent(119), wantFactorKey: analysisSecondFactorVolumeNormal},
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

func TestAnalysisBacktestConfidenceFactorBuckets(t *testing.T) {
	results := []AnalysisSignalResult{
		{Confidence: 65, Result: "correct", SecondFactorKey: analysisSecondFactorVolumeSpike},
		{Confidence: 65, Result: "wrong", SecondFactorKey: analysisSecondFactorVolumeSpike},
		{Confidence: 72, Result: "correct", SecondFactorKey: analysisSecondFactorVolumeNormal},
		{Confidence: 92, Result: "pending", SecondFactorKey: analysisSecondFactorVolumeLow},
	}
	buckets := buildAnalysisBacktestConfidenceFactorBuckets(results)
	if len(buckets) != 15 {
		t.Fatalf("confidence factor bucket count = %d, want 15", len(buckets))
	}
	find := func(label, factor string) AnalysisBacktestConfidenceFactorBucket {
		for _, bucket := range buckets {
			if bucket.Label == label && bucket.FactorKey == factor {
				return bucket
			}
		}
		t.Fatalf("missing bucket %s/%s", label, factor)
		return AnalysisBacktestConfidenceFactorBucket{}
	}
	spikeLowConf := find("<70", analysisSecondFactorVolumeSpike)
	if spikeLowConf.TotalSignals != 2 || spikeLowConf.CorrectCount != 1 || spikeLowConf.WrongCount != 1 || spikeLowConf.CorrectRate != 50 {
		t.Fatalf("<70 spike bucket = %+v, want total=2 correct=1 wrong=1 rate=50", spikeLowConf)
	}
	normalMidConf := find("70-80", analysisSecondFactorVolumeNormal)
	if normalMidConf.TotalSignals != 1 || normalMidConf.CorrectRate != 100 {
		t.Fatalf("70-80 normal bucket = %+v, want total=1 rate=100", normalMidConf)
	}
	lowHighConf := find("90+", analysisSecondFactorVolumeLow)
	if lowHighConf.TotalSignals != 1 || lowHighConf.PendingCount != 1 || lowHighConf.CorrectRate != 0 {
		t.Fatalf("90+ low bucket = %+v, want one pending and no verified rate", lowHighConf)
	}
}

func TestAnalysisBacktestStrategyGroups(t *testing.T) {
	makeRows := func(confidence float64, factor string, fiveResults []string, fifteenResults []string) []AnalysisSignalResult {
		out := make([]AnalysisSignalResult, 0, len(fiveResults))
		for i, result := range fiveResults {
			horizons := map[int]string{5: result}
			if i < len(fifteenResults) {
				horizons[15] = fifteenResults[i]
			}
			row := backtestSignalForTest(confidence, result, horizons)
			row.SecondFactorKey = factor
			row.SecondFactorLabel = analysisSecondFactorLabel(factor)
			out = append(out, row)
		}
		return out
	}
	find := func(groups []AnalysisBacktestStrategyGroup, factor, label string) AnalysisBacktestStrategyGroup {
		for _, group := range groups {
			if group.FactorKey == factor && group.Label == label {
				return group
			}
		}
		t.Fatalf("missing strategy group %s/%s", factor, label)
		return AnalysisBacktestStrategyGroup{}
	}

	lowSample := find(buildAnalysisBacktestStrategyGroups(makeRows(65, analysisSecondFactorVolumeSpike, []string{"correct", "correct", "correct", "correct"}, nil)), analysisSecondFactorVolumeSpike, "<70")
	if lowSample.Selected || lowSample.SampleCount != 4 {
		t.Fatalf("low sample group = %+v, want not selected with 4 samples", lowSample)
	}

	lowFiveMinute := find(buildAnalysisBacktestStrategyGroups(makeRows(65, analysisSecondFactorVolumeSpike, []string{"correct", "correct", "wrong", "wrong", "wrong"}, nil)), analysisSecondFactorVolumeSpike, "<70")
	if lowFiveMinute.Selected || lowFiveMinute.FiveMinuteCorrectRate != 40 {
		t.Fatalf("low 5m group = %+v, want not selected with 40%% 5m rate", lowFiveMinute)
	}

	lowComposite := find(buildAnalysisBacktestStrategyGroups(makeRows(72, analysisSecondFactorVolumeNormal, []string{"correct", "correct", "correct", "wrong", "wrong"}, []string{"correct", "correct", "wrong", "wrong", "wrong"})), analysisSecondFactorVolumeNormal, "70-80")
	if lowComposite.Selected || lowComposite.CompositeScore >= 52 {
		t.Fatalf("low composite group = %+v, want not selected below 52 composite", lowComposite)
	}

	weakHorizon := find(buildAnalysisBacktestStrategyGroups(makeRows(92, analysisSecondFactorVolumeLow, []string{"correct", "correct", "correct", "correct", "correct"}, []string{"correct", "wrong", "wrong", "wrong", "wrong"})), analysisSecondFactorVolumeLow, "90+")
	if weakHorizon.Selected {
		t.Fatalf("weak horizon group selected unexpectedly: %+v", weakHorizon)
	}

	selected := find(buildAnalysisBacktestStrategyGroups(makeRows(92, analysisSecondFactorVolumeSpike, []string{"correct", "correct", "correct", "wrong", "wrong"}, []string{"correct", "correct", "correct", "wrong", "wrong"})), analysisSecondFactorVolumeSpike, "90+")
	if !selected.Selected || selected.CompositeScore != 60 {
		t.Fatalf("selected group = %+v, want selected with 60 composite", selected)
	}
}

func TestFilterAnalysisSignalsByPreferredStrategyGroups(t *testing.T) {
	selected := backtestSignalForTest(92, "correct", map[int]string{5: "correct", 15: "correct"})
	selected.SecondFactorKey = analysisSecondFactorVolumeSpike
	rejected := backtestSignalForTest(65, "wrong", map[int]string{5: "wrong", 15: "wrong"})
	rejected.SecondFactorKey = analysisSecondFactorVolumeLow

	rows := []AnalysisSignalResult{selected, rejected}
	groups := []AnalysisBacktestStrategyGroup{
		{FactorKey: analysisSecondFactorVolumeSpike, Label: "90+", Selected: true},
		{FactorKey: analysisSecondFactorVolumeLow, Label: "<70", Selected: false},
	}
	filtered, active := filterAnalysisSignalsByStrategyGroups(rows, groups, analysisBacktestStrategyPreferred)
	if !active || len(filtered) != 1 || filtered[0].SecondFactorKey != analysisSecondFactorVolumeSpike {
		t.Fatalf("preferred filter = active %v rows %+v, want one selected spike row", active, filtered)
	}

	fallback, active := filterAnalysisSignalsByStrategyGroups(rows, []AnalysisBacktestStrategyGroup{{FactorKey: analysisSecondFactorVolumeSpike, Label: "90+"}}, analysisBacktestStrategyPreferred)
	if active || len(fallback) != len(rows) {
		t.Fatalf("fallback filter = active %v len %d, want inactive full result", active, len(fallback))
	}
}

func TestNormalizeAnalysisBacktestStrategyDefaultsToAll(t *testing.T) {
	if got := normalizeAnalysisBacktestStrategy(""); got != analysisBacktestStrategyAll {
		t.Fatalf("empty strategy = %q, want %q", got, analysisBacktestStrategyAll)
	}
	if got := normalizeAnalysisBacktestStrategy("preferred"); got != analysisBacktestStrategyPreferred {
		t.Fatalf("preferred strategy = %q, want %q", got, analysisBacktestStrategyPreferred)
	}
	if got := normalizeAnalysisBacktestStrategy("unknown"); got != analysisBacktestStrategyAll {
		t.Fatalf("unknown strategy = %q, want %q", got, analysisBacktestStrategyAll)
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
