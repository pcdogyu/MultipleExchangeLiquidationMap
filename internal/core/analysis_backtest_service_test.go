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
