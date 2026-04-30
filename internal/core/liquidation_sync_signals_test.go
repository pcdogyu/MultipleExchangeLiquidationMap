package liqmap

import "testing"

func TestBuildLiquidationSyncSignals(t *testing.T) {
	buckets := []LiquidationPeriodBucket{
		{Label: "1H", Hours: 1, TotalUSD: 100, LongUSD: 20, ShortUSD: 80, PricePush: "up", BalanceRatio: 0.60},
		{Label: "4H", Hours: 4, TotalUSD: 120, LongUSD: 40, ShortUSD: 80, PricePush: "up", BalanceRatio: 0.33},
		{Label: "12H", Hours: 12, TotalUSD: 200, LongUSD: 140, ShortUSD: 60, PricePush: "down", BalanceRatio: 0.40},
		{Label: "24H", Hours: 24, TotalUSD: 260, LongUSD: 170, ShortUSD: 90, PricePush: "down", BalanceRatio: 0.31},
	}
	app := &App{}
	signals := app.buildLiquidationSyncSignals(buckets)

	if !signals.ShortTermSync.Active || signals.ShortTermSync.Direction != "up" {
		t.Fatalf("expected short-term up sync, got %+v", signals.ShortTermSync)
	}
	if !signals.Continuation.Active || signals.Continuation.Direction != "down" {
		t.Fatalf("expected continuation down sync, got %+v", signals.Continuation)
	}
	if signals.OverallAlignment != "mixed" {
		t.Fatalf("expected mixed alignment, got %s", signals.OverallAlignment)
	}
}

func TestBuildLiquidationSyncSignalsNeutralThreshold(t *testing.T) {
	buckets := []LiquidationPeriodBucket{
		{Label: "1H", Hours: 1, TotalUSD: 100, LongUSD: 44, ShortUSD: 56, PricePush: "up", BalanceRatio: 0.12},
		{Label: "4H", Hours: 4, TotalUSD: 100, LongUSD: 30, ShortUSD: 70, PricePush: "up", BalanceRatio: 0.40},
		{Label: "12H", Hours: 12, TotalUSD: 0, LongUSD: 0, ShortUSD: 0, PricePush: "neutral", BalanceRatio: 0},
		{Label: "24H", Hours: 24, TotalUSD: 100, LongUSD: 20, ShortUSD: 80, PricePush: "up", BalanceRatio: 0.60},
	}
	app := &App{}
	signals := app.buildLiquidationSyncSignals(buckets)

	if signals.ShortTermSync.Active {
		t.Fatalf("expected short-term sync inactive when 1H below threshold")
	}
	if signals.Continuation.Active {
		t.Fatalf("expected continuation sync inactive when 12H has no data")
	}
}

func TestShouldSendLiquidationSyncAlert(t *testing.T) {
	cases := []struct {
		name string
		prev liquidationSyncAlertState
		curr liquidationSyncAlertState
		want bool
	}{
		{
			name: "mixed to up resonance",
			prev: liquidationSyncAlertState{Alignment: "mixed"},
			curr: liquidationSyncAlertState{ShortActive: true, ShortDirection: "up", ContActive: true, ContDirection: "up", Alignment: "up_resonance"},
			want: true,
		},
		{
			name: "mixed to down resonance",
			prev: liquidationSyncAlertState{Alignment: "mixed"},
			curr: liquidationSyncAlertState{ShortActive: true, ShortDirection: "down", ContActive: true, ContDirection: "down", Alignment: "down_resonance"},
			want: true,
		},
		{
			name: "resonance to mixed",
			prev: liquidationSyncAlertState{ShortActive: true, ShortDirection: "up", ContActive: true, ContDirection: "up", Alignment: "up_resonance"},
			curr: liquidationSyncAlertState{ShortActive: true, ShortDirection: "up", ContActive: false, ContDirection: "", Alignment: "mixed"},
			want: true,
		},
		{
			name: "mixed unchanged",
			prev: liquidationSyncAlertState{Alignment: "mixed"},
			curr: liquidationSyncAlertState{ShortActive: true, ShortDirection: "up", Alignment: "mixed"},
			want: false,
		},
		{
			name: "same resonance unchanged",
			prev: liquidationSyncAlertState{ShortActive: true, ShortDirection: "up", ContActive: true, ContDirection: "up", Alignment: "up_resonance"},
			curr: liquidationSyncAlertState{ShortActive: true, ShortDirection: "up", ContActive: true, ContDirection: "up", Alignment: "up_resonance"},
			want: false,
		},
		{
			name: "up resonance to down resonance",
			prev: liquidationSyncAlertState{ShortActive: true, ShortDirection: "up", ContActive: true, ContDirection: "up", Alignment: "up_resonance"},
			curr: liquidationSyncAlertState{ShortActive: true, ShortDirection: "down", ContActive: true, ContDirection: "down", Alignment: "down_resonance"},
			want: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := shouldSendLiquidationSyncAlert(tc.prev, tc.curr)
			if got != tc.want {
				t.Fatalf("shouldSendLiquidationSyncAlert() = %v, want %v", got, tc.want)
			}
		})
	}
}
