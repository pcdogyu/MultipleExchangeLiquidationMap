package liqmap

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

var analysisTrendBands = []int{20, 50, 80, 100, 200, 300}

type analysisBandHistorySnapshot struct {
	TS         int64
	Current    float64
	UpByBand   map[int]float64
	DownByBand map[int]float64
}

func analysisBandPushScore(up, down float64) float64 {
	total := up + down
	if total <= 0 {
		return 0
	}
	return clamp(((up - down) / total) * 100, -100, 100)
}

func analysisPushDirectionText(score float64) string {
	switch {
	case score >= 8:
		return "偏向价格向上推动"
	case score <= -8:
		return "偏向价格向下推动"
	default:
		return "当前较中性"
	}
}

func analysisPushTone(score float64) string {
	switch {
	case score >= 8:
		return "up"
	case score <= -8:
		return "down"
	default:
		return "flat"
	}
}

func (a *App) loadAnalysisBandHistory(symbol string, hours int) []analysisBandHistorySnapshot {
	if hours <= 0 {
		hours = 24
	}
	startTS := time.Now().Add(-time.Duration(hours) * time.Hour).UnixMilli()
	rows, err := a.db.Query(`SELECT id, captured_at, range_low, range_high, payload_json
		FROM webdatasource_snapshots
		WHERE symbol IN (?, 'ETH') AND window_days=1 AND captured_at>=?
			AND EXISTS (SELECT 1 FROM webdatasource_points WHERE snapshot_id=webdatasource_snapshots.id LIMIT 1)
		ORDER BY captured_at`, symbol, startTS)
	if err != nil {
		return nil
	}
	defer rows.Close()

	type metaRow struct {
		ID        int64
		TS        int64
		RangeLow  float64
		RangeHigh float64
		Payload   string
	}
	metas := make([]metaRow, 0, 96)
	for rows.Next() {
		var meta metaRow
		if err := rows.Scan(&meta.ID, &meta.TS, &meta.RangeLow, &meta.RangeHigh, &meta.Payload); err != nil {
			continue
		}
		metas = append(metas, meta)
	}

	out := make([]analysisBandHistorySnapshot, 0, len(metas))
	for _, meta := range metas {
		current := 0.0
		if strings.TrimSpace(meta.Payload) != "" {
			var payload map[string]any
			if err := json.Unmarshal([]byte(meta.Payload), &payload); err == nil {
				current = webDataSourcePayloadCurrentPrice(payload, meta.RangeLow, meta.RangeHigh)
			}
		}
		if current <= 0 && meta.RangeHigh > meta.RangeLow {
			current = math.Round(((meta.RangeHigh + meta.RangeLow) / 2 * 10)) / 10
		}
		if current <= 0 {
			continue
		}

		points, err := a.db.Query(`SELECT side, price, liq_value FROM webdatasource_points WHERE snapshot_id=?`, meta.ID)
		if err != nil {
			continue
		}
		snap := analysisBandHistorySnapshot{
			TS:         meta.TS,
			Current:    current,
			UpByBand:   map[int]float64{},
			DownByBand: map[int]float64{},
		}
		for points.Next() {
			var side string
			var price, liq float64
			if err := points.Scan(&side, &price, &liq); err != nil {
				continue
			}
			if price <= 0 || liq <= 0 {
				continue
			}
			side = strings.ToLower(strings.TrimSpace(side))
			switch side {
			case "short":
				if price < current {
					continue
				}
				dist := price - current
				for _, band := range analysisTrendBands {
					if dist <= float64(band) {
						snap.UpByBand[band] += liq
					}
				}
			default:
				if price > current {
					continue
				}
				dist := current - price
				for _, band := range analysisTrendBands {
					if dist <= float64(band) {
						snap.DownByBand[band] += liq
					}
				}
			}
		}
		_ = points.Close()
		out = append(out, snap)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].TS < out[j].TS })
	return out
}

func (a *App) buildAnalysisBandTrendCharts(symbol string, hours int) []AnalysisDelta {
	history := a.loadAnalysisBandHistory(symbol, hours)
	out := make([]AnalysisDelta, 0, len(analysisTrendBands))
	for _, band := range analysisTrendBands {
		series := make([]AnalysisSeriesPoint, 0, len(history))
		latestUp := 0.0
		latestDown := 0.0
		latestScore := 0.0
		for _, snap := range history {
			score := analysisBandPushScore(snap.UpByBand[band], snap.DownByBand[band])
			series = append(series, AnalysisSeriesPoint{TS: snap.TS, Value: score})
			latestUp = snap.UpByBand[band]
			latestDown = snap.DownByBand[band]
			latestScore = score
		}
		out = append(out, AnalysisDelta{
			Label:         fmt.Sprintf("%d 点推动指数", band),
			Value:         latestScore,
			Unit:          "score",
			Tone:          analysisPushTone(latestScore),
			Subvalue:      fmt.Sprintf("上推 %s / 下压 %s", humanizeCompactUSD(latestUp), humanizeCompactUSD(latestDown)),
			PushDirection: analysisPushDirectionText(latestScore),
			Note:          "折线显示过去 24 小时页面数据源 1D 快照序列；公式 = (上方清算强度 - 下方清算强度) / 总强度 × 100。",
			Series:        series,
		})
	}
	return out
}

func (a *App) buildAnalysisLiquidationTrendChart(symbol string, hours int) AnalysisDelta {
	now := time.Now().Truncate(time.Hour)
	start := now.Add(-time.Duration(hours) * time.Hour)
	series := make([]AnalysisSeriesPoint, 0, hours+1)
	latestTotal := 0.0
	latestBias := 0.0
	for i := 0; i <= hours; i++ {
		endAt := start.Add(time.Duration(i) * time.Hour)
		windowStart := endAt.Add(-4 * time.Hour)
		rows, err := a.db.Query(`SELECT side, SUM(notional_usd) FROM liquidation_events
			WHERE symbol=? AND event_ts>=? AND event_ts<?
			GROUP BY side`, symbol, windowStart.UnixMilli(), endAt.UnixMilli())
		shortTotal := 0.0
		longTotal := 0.0
		if err == nil {
			for rows.Next() {
				var side string
				var total float64
				if err := rows.Scan(&side, &total); err != nil {
					continue
				}
				side = strings.ToLower(strings.TrimSpace(side))
				if side == "short" {
					shortTotal += total
				} else {
					longTotal += total
				}
			}
			_ = rows.Close()
		}
		total := shortTotal + longTotal
		bias := analysisBandPushScore(shortTotal, longTotal)
		series = append(series, AnalysisSeriesPoint{TS: endAt.UnixMilli(), Value: total})
		latestTotal = total
		latestBias = bias
	}
	return AnalysisDelta{
		Label:         "4 小时清算量",
		Value:         latestTotal,
		Unit:          "USD",
		Tone:          scoreTone(clamp(latestTotal/1_500_000*100, 0, 100)),
		Subvalue:      "滚动 4 小时实际清算总量",
		PushDirection: analysisPushDirectionText(latestBias),
		Note:          "折线显示过去 24 小时的滚动 4 小时清算总量；推动方向按空头被挤压与多头被踩踏的相对强弱估算。",
		Series:        series,
	}
}

func (a *App) buildAnalysisFundingTrendChart(symbol string, hours int) AnalysisDelta {
	now := time.Now().Truncate(time.Hour)
	start := now.Add(-time.Duration(hours) * time.Hour)
	series := make([]AnalysisSeriesPoint, 0, hours+1)
	latestDelta := 0.0
	for i := 0; i <= hours; i++ {
		endAt := start.Add(time.Duration(i) * time.Hour)
		cur := a.avgFundingInRange(symbol, endAt.Add(-time.Hour).UnixMilli(), endAt.UnixMilli())
		prev := a.avgFundingInRange(symbol, endAt.Add(-2*time.Hour).UnixMilli(), endAt.Add(-time.Hour).UnixMilli())
		delta := cur - prev
		series = append(series, AnalysisSeriesPoint{TS: endAt.UnixMilli(), Value: delta})
		latestDelta = delta
	}
	return AnalysisDelta{
		Label:         "资金费率变化",
		Value:         latestDelta,
		Unit:          "rate",
		Tone:          signedTone(latestDelta),
		Subvalue:      "最近 1 小时均值相对前 1 小时",
		PushDirection: analysisPushDirectionText(clamp(latestDelta*1_000_000, -100, 100)),
		Note:          "折线显示过去 24 小时每小时的资金费率变化；正值偏向上方挤空，负值偏向下方踩踏。",
		Series:        series,
	}
}

func (a *App) buildAnalysisChangeCharts(symbol string, hours int) []AnalysisDelta {
	out := a.buildAnalysisBandTrendCharts(symbol, hours)
	out = append(out, a.buildAnalysisLiquidationTrendChart(symbol, hours))
	out = append(out, a.buildAnalysisFundingTrendChart(symbol, hours))
	return out
}
