package liqmap

import (
	"database/sql"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	dbpkg "multipleexchangeliquidationmap/internal/platform/db"

	_ "modernc.org/sqlite"
)

func makeAnalysisDirectionSnapshot(days int, currentPrice float64, points ...WebDataSourcePoint) analysisDirectionSnapshot {
	return analysisDirectionSnapshot{
		Window:       analysisDirectionWindowLabel(days),
		Days:         days,
		CapturedAt:   time.Now().UnixMilli(),
		CurrentPrice: currentPrice,
		Points:       points,
	}
}

func shortPoint(price, liq float64) WebDataSourcePoint {
	return WebDataSourcePoint{Exchange: "BINANCE", Side: "short", Price: price, LiqValue: liq}
}

func longPoint(price, liq float64) WebDataSourcePoint {
	return WebDataSourcePoint{Exchange: "BINANCE", Side: "long", Price: price, LiqValue: liq}
}

func decisionFromSnapshots(currentPrice float64, snapshots map[int][2]analysisDirectionSnapshot) (analysisDirectionDecision, bool) {
	for days, pair := range snapshots {
		pair[0].Days = days
		pair[1].Days = days
		pair[0].Window = analysisDirectionWindowLabel(days)
		pair[1].Window = analysisDirectionWindowLabel(days)
		snapshots[days] = pair
	}
	return buildAnalysisDirectionDecision(currentPrice, snapshots)
}

func TestBuildAnalysisDirectionDecisionUp(t *testing.T) {
	currentPrice := 2300.0
	snapshots := map[int][2]analysisDirectionSnapshot{
		1: {
			makeAnalysisDirectionSnapshot(1, currentPrice, shortPoint(2320, 420000), shortPoint(2340, 240000), longPoint(2280, 90000)),
			makeAnalysisDirectionSnapshot(1, currentPrice, shortPoint(2320, 120000), shortPoint(2340, 80000), longPoint(2280, 70000)),
		},
		7: {
			makeAnalysisDirectionSnapshot(7, currentPrice, shortPoint(2330, 460000), shortPoint(2360, 210000), longPoint(2270, 110000)),
			makeAnalysisDirectionSnapshot(7, currentPrice, shortPoint(2330, 180000), shortPoint(2360, 90000), longPoint(2270, 85000)),
		},
		30: {
			makeAnalysisDirectionSnapshot(30, currentPrice, shortPoint(2350, 520000), shortPoint(2380, 260000), longPoint(2260, 120000)),
			makeAnalysisDirectionSnapshot(30, currentPrice, shortPoint(2350, 230000), shortPoint(2380, 120000), longPoint(2260, 100000)),
		},
	}

	decision, ok := decisionFromSnapshots(currentPrice, snapshots)
	if !ok {
		t.Fatalf("expected decision")
	}
	if decision.Direction != "up" {
		t.Fatalf("expected up, got %s", decision.Direction)
	}
	if decision.Confidence < 60 {
		t.Fatalf("expected strong confidence, got %.1f", decision.Confidence)
	}
}

func TestBuildAnalysisDirectionDecisionMixedTurnsDown(t *testing.T) {
	currentPrice := 2300.0
	snapshots := map[int][2]analysisDirectionSnapshot{
		1: {
			makeAnalysisDirectionSnapshot(1, currentPrice, shortPoint(2320, 330000), shortPoint(2340, 170000), longPoint(2285, 130000)),
			makeAnalysisDirectionSnapshot(1, currentPrice, shortPoint(2320, 240000), shortPoint(2340, 140000), longPoint(2285, 120000)),
		},
		7: {
			makeAnalysisDirectionSnapshot(7, currentPrice, longPoint(2280, 1_400_000), longPoint(2260, 1_100_000), shortPoint(2330, 70000)),
			makeAnalysisDirectionSnapshot(7, currentPrice, longPoint(2280, 150000), longPoint(2260, 140000), shortPoint(2330, 65000)),
		},
		30: {
			makeAnalysisDirectionSnapshot(30, currentPrice, longPoint(2275, 1_700_000), longPoint(2250, 1_300_000), shortPoint(2350, 85000)),
			makeAnalysisDirectionSnapshot(30, currentPrice, longPoint(2275, 170000), longPoint(2250, 160000), shortPoint(2350, 75000)),
		},
	}

	decision, ok := decisionFromSnapshots(currentPrice, snapshots)
	if !ok {
		t.Fatalf("expected decision")
	}
	if decision.Direction != "down" {
		t.Fatalf("expected down, got %s", decision.Direction)
	}
}

func TestBuildAnalysisDirectionDecisionWeakReturnsFalse(t *testing.T) {
	currentPrice := 2300.0
	snapshots := map[int][2]analysisDirectionSnapshot{
		1: {
			makeAnalysisDirectionSnapshot(1, currentPrice, shortPoint(2320, 100000), longPoint(2280, 90000)),
			makeAnalysisDirectionSnapshot(1, currentPrice, shortPoint(2320, 90000), longPoint(2280, 85000)),
		},
	}

	if _, ok := decisionFromSnapshots(currentPrice, snapshots); ok {
		t.Fatalf("expected no signal for weak changes")
	}
}

func TestBuildAnalysisDirectionDecisionOneDayOnlyReturnsFalse(t *testing.T) {
	currentPrice := 2300.0
	snapshots := map[int][2]analysisDirectionSnapshot{
		1: {
			makeAnalysisDirectionSnapshot(1, currentPrice, shortPoint(2320, 420000), shortPoint(2340, 180000)),
			makeAnalysisDirectionSnapshot(1, currentPrice, shortPoint(2320, 90000), shortPoint(2340, 70000)),
		},
	}

	if _, ok := decisionFromSnapshots(currentPrice, snapshots); ok {
		t.Fatalf("expected no signal when fewer than two windows agree")
	}
}

func TestBuildAnalysisDirectionDecisionFragmentedHasLowerConfidence(t *testing.T) {
	currentPrice := 2300.0
	aligned := map[int][2]analysisDirectionSnapshot{
		1: {
			makeAnalysisDirectionSnapshot(1, currentPrice, shortPoint(2320, 460000), shortPoint(2340, 260000)),
			makeAnalysisDirectionSnapshot(1, currentPrice, shortPoint(2320, 120000), shortPoint(2340, 90000)),
		},
		7: {
			makeAnalysisDirectionSnapshot(7, currentPrice, shortPoint(2330, 500000), shortPoint(2350, 260000)),
			makeAnalysisDirectionSnapshot(7, currentPrice, shortPoint(2330, 140000), shortPoint(2350, 90000)),
		},
	}
	fragmented := map[int][2]analysisDirectionSnapshot{
		1: {
			makeAnalysisDirectionSnapshot(1, currentPrice,
				shortPoint(2320, 500000),
				longPoint(2280, 320000),
				shortPoint(2340, 260000),
				longPoint(2260, 190000),
			),
			makeAnalysisDirectionSnapshot(1, currentPrice,
				shortPoint(2320, 130000),
				longPoint(2280, 90000),
				shortPoint(2340, 90000),
				longPoint(2260, 50000),
			),
		},
		7: {
			makeAnalysisDirectionSnapshot(7, currentPrice,
				shortPoint(2330, 520000),
				longPoint(2270, 310000),
				shortPoint(2350, 250000),
				longPoint(2250, 180000),
			),
			makeAnalysisDirectionSnapshot(7, currentPrice,
				shortPoint(2330, 130000),
				longPoint(2270, 80000),
				shortPoint(2350, 90000),
				longPoint(2250, 50000),
			),
		},
	}

	alignedDecision, ok := decisionFromSnapshots(currentPrice, aligned)
	if !ok {
		t.Fatalf("expected aligned decision")
	}
	fragmentedDecision, ok := decisionFromSnapshots(currentPrice, fragmented)
	if !ok {
		t.Fatalf("expected fragmented decision")
	}
	if fragmentedDecision.Confidence >= alignedDecision.Confidence {
		t.Fatalf("expected fragmented confidence < aligned confidence, got %.1f >= %.1f", fragmentedDecision.Confidence, alignedDecision.Confidence)
	}
	if math.Abs(fragmentedDecision.Explain.FinalScore) >= math.Abs(alignedDecision.Explain.FinalScore) {
		t.Fatalf("expected fragmented score magnitude < aligned score magnitude, got %.1f >= %.1f", math.Abs(fragmentedDecision.Explain.FinalScore), math.Abs(alignedDecision.Explain.FinalScore))
	}
}

func TestBuildAnalysisDirectionDecisionExtremePointIsClamped(t *testing.T) {
	currentPrice := 2300.0
	snapshots := map[int][2]analysisDirectionSnapshot{
		1: {
			makeAnalysisDirectionSnapshot(1, currentPrice, shortPoint(2320, 9_000_000), shortPoint(2340, 400000)),
			makeAnalysisDirectionSnapshot(1, currentPrice, shortPoint(2320, 100000), shortPoint(2340, 70000)),
		},
		7: {
			makeAnalysisDirectionSnapshot(7, currentPrice, shortPoint(2330, 4_500_000), shortPoint(2350, 400000)),
			makeAnalysisDirectionSnapshot(7, currentPrice, shortPoint(2330, 100000), shortPoint(2350, 70000)),
		},
	}

	decision, ok := decisionFromSnapshots(currentPrice, snapshots)
	if !ok {
		t.Fatalf("expected decision")
	}
	if decision.Confidence > 92 {
		t.Fatalf("expected clamped confidence, got %.1f", decision.Confidence)
	}
}

func TestRecordAnalysisDirectionSignalPersistsNewAlgorithmSignal(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()
	if err := dbpkg.Configure(db); err != nil {
		t.Fatalf("configure db: %v", err)
	}
	if err := dbpkg.Init(db); err != nil {
		t.Fatalf("init db: %v", err)
	}

	app := &App{db: db}
	now := time.Now()
	insertSnapshot := func(days int, capturedAt int64, points []WebDataSourcePoint) int64 {
		payload := fmt.Sprintf(`{"currentPrice":2300}`)
		res, err := db.Exec(`INSERT INTO webdatasource_snapshots(symbol, window_days, captured_at, range_low, range_high, payload_json) VALUES('ETH', ?, ?, 2200, 2400, ?)`, days, capturedAt, payload)
		if err != nil {
			t.Fatalf("insert snapshot: %v", err)
		}
		id, err := res.LastInsertId()
		if err != nil {
			t.Fatalf("snapshot id: %v", err)
		}
		for _, point := range points {
			_, err = db.Exec(`INSERT INTO webdatasource_points(snapshot_id, symbol, window_days, side, exchange, price, liq_value, captured_at) VALUES(?, 'ETH', ?, ?, ?, ?, ?, ?)`,
				id, days, point.Side, point.Exchange, point.Price, point.LiqValue, capturedAt)
			if err != nil {
				t.Fatalf("insert point: %v", err)
			}
		}
		return id
	}

	insertSnapshot(1, now.Add(-5*time.Minute).UnixMilli(), []WebDataSourcePoint{
		shortPoint(2320, 420000), shortPoint(2340, 220000), longPoint(2280, 80000),
	})
	insertSnapshot(1, now.Add(-35*time.Minute).UnixMilli(), []WebDataSourcePoint{
		shortPoint(2320, 100000), shortPoint(2340, 70000), longPoint(2280, 60000),
	})
	insertSnapshot(7, now.Add(-5*time.Minute).UnixMilli(), []WebDataSourcePoint{
		shortPoint(2330, 500000), shortPoint(2360, 240000), longPoint(2270, 100000),
	})
	insertSnapshot(7, now.Add(-35*time.Minute).UnixMilli(), []WebDataSourcePoint{
		shortPoint(2330, 180000), shortPoint(2360, 100000), longPoint(2270, 90000),
	})
	insertSnapshot(30, now.Add(-5*time.Minute).UnixMilli(), []WebDataSourcePoint{
		shortPoint(2350, 540000), shortPoint(2380, 260000), longPoint(2260, 110000),
	})
	insertSnapshot(30, now.Add(-35*time.Minute).UnixMilli(), []WebDataSourcePoint{
		shortPoint(2350, 200000), shortPoint(2380, 110000), longPoint(2260, 90000),
	})

	err = app.recordAnalysisDirectionSignal(AnalysisSnapshot{
		Symbol:       defaultSymbol,
		GeneratedAt:  now.UnixMilli(),
		CurrentPrice: 2300,
	})
	if err != nil {
		t.Fatalf("record signal: %v", err)
	}

	var direction, headline, summary string
	var confidence float64
	err = db.QueryRow(`SELECT direction, confidence, headline, summary FROM analysis_direction_signals ORDER BY id DESC LIMIT 1`).Scan(&direction, &confidence, &headline, &summary)
	if err != nil {
		t.Fatalf("query signal: %v", err)
	}
	if direction != "up" {
		t.Fatalf("expected up signal, got %s", direction)
	}
	if !strings.Contains(headline, "多窗口清算带变化") {
		t.Fatalf("unexpected headline: %s", headline)
	}
	if !strings.Contains(summary, "1D 权重") {
		t.Fatalf("expected summary to include weights, got: %s", summary)
	}
	if confidence <= 0 {
		t.Fatalf("expected confidence > 0, got %.1f", confidence)
	}
}
