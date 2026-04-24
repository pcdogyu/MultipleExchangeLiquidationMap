package liqmap

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"
)

type analysisWindowPeak struct {
	Window       string
	Days         int
	Weight       float64
	GeneratedAt  int64
	CurrentPrice float64
	RangeLow     float64
	RangeHigh    float64
	LongPrice    float64
	LongValue    float64
	ShortPrice   float64
	ShortValue   float64
}

type analysisPeakPoint struct {
	Window      string
	Days        int
	Weight      float64
	GeneratedAt int64
	Price       float64
	Value       float64
}

func (a *App) loadAnalysisWindowPeak(days, offset int) (analysisWindowPeak, bool) {
	var (
		snapshotID int64
		capturedAt int64
		rangeLow   float64
		rangeHigh  float64
		payloadRaw string
	)
	err := a.db.QueryRow(`SELECT id, captured_at, range_low, range_high, payload_json
		FROM webdatasource_snapshots
		WHERE symbol='ETH' AND window_days=?
			AND EXISTS (SELECT 1 FROM webdatasource_points WHERE snapshot_id=webdatasource_snapshots.id LIMIT 1)
		ORDER BY captured_at DESC
		LIMIT 1 OFFSET ?`, days, offset).
		Scan(&snapshotID, &capturedAt, &rangeLow, &rangeHigh, &payloadRaw)
	if err != nil {
		return analysisWindowPeak{}, false
	}

	rows, err := a.db.Query(`SELECT side, price, SUM(liq_value) total
		FROM webdatasource_points
		WHERE snapshot_id=?
		GROUP BY side, price`, snapshotID)
	if err != nil {
		return analysisWindowPeak{}, false
	}
	defer rows.Close()

	out := analysisWindowPeak{
		Window:      map[int]string{1: "1D", 7: "7D", 30: "30D"}[days],
		Days:        days,
		Weight:      map[int]float64{1: 0.25, 7: 0.35, 30: 0.40}[days],
		GeneratedAt: capturedAt,
		RangeLow:    rangeLow,
		RangeHigh:   rangeHigh,
	}
	if out.Window == "" {
		out.Window = fmt.Sprintf("%dD", days)
	}
	if out.Weight <= 0 {
		out.Weight = 1
	}

	for rows.Next() {
		var side string
		var price, total float64
		if err := rows.Scan(&side, &price, &total); err != nil {
			continue
		}
		side = strings.ToLower(strings.TrimSpace(side))
		if side == "short" {
			if total > out.ShortValue {
				out.ShortValue = total
				out.ShortPrice = price
			}
			continue
		}
		if total > out.LongValue {
			out.LongValue = total
			out.LongPrice = price
		}
	}

	if strings.TrimSpace(payloadRaw) != "" {
		var payload map[string]any
		if err := json.Unmarshal([]byte(payloadRaw), &payload); err == nil {
			out.CurrentPrice = webDataSourcePayloadCurrentPrice(payload, rangeLow, rangeHigh)
		}
	}
	if out.CurrentPrice <= 0 && rangeHigh > rangeLow {
		out.CurrentPrice = math.Round(((rangeHigh+rangeLow)/2)*10) / 10
	}
	return out, true
}

func analysisPeakPointsBySide(latest map[int]analysisWindowPeak, side string) []analysisPeakPoint {
	out := make([]analysisPeakPoint, 0, len(latest))
	for _, days := range []int{1, 7, 30} {
		item, ok := latest[days]
		if !ok {
			continue
		}
		point := analysisPeakPoint{
			Window:      item.Window,
			Days:        item.Days,
			Weight:      item.Weight,
			GeneratedAt: item.GeneratedAt,
		}
		if side == "short" {
			point.Price = item.ShortPrice
			point.Value = item.ShortValue
		} else {
			point.Price = item.LongPrice
			point.Value = item.LongValue
		}
		if point.Price > 0 && point.Value > 0 {
			out = append(out, point)
		}
	}
	return out
}

func analysisWeightedPrice(points []analysisPeakPoint) float64 {
	total := 0.0
	sum := 0.0
	for _, point := range points {
		weight := point.Weight * point.Value
		sum += point.Price * weight
		total += weight
	}
	if total <= 0 {
		return 0
	}
	return sum / total
}

func analysisPriceSpread(points []analysisPeakPoint) float64 {
	if len(points) == 0 {
		return 0
	}
	minPrice := points[0].Price
	maxPrice := points[0].Price
	for _, point := range points[1:] {
		if point.Price < minPrice {
			minPrice = point.Price
		}
		if point.Price > maxPrice {
			maxPrice = point.Price
		}
	}
	return maxPrice - minPrice
}

func analysisWeightedValue(points []analysisPeakPoint) float64 {
	total := 0.0
	for _, point := range points {
		total += point.Value * point.Weight
	}
	return total
}

func analysisFormatWindows(points []analysisPeakPoint) string {
	if len(points) == 0 {
		return "-"
	}
	names := make([]string, 0, len(points))
	for _, point := range points {
		names = append(names, point.Window)
	}
	return strings.Join(names, " / ")
}

func analysisDistanceTone(distance float64, currentPrice float64) string {
	switch {
	case distance <= math.Max(20, currentPrice*0.008):
		return "high"
	case distance <= math.Max(45, currentPrice*0.018):
		return "medium"
	default:
		return "low"
	}
}

func analysisOverlapTone(score float64) string {
	switch {
	case score >= 75:
		return "high"
	case score >= 45:
		return "medium"
	default:
		return "low"
	}
}

func analysisBiasTone(score float64) string {
	switch {
	case score >= 12:
		return "up"
	case score <= -12:
		return "down"
	default:
		return "flat"
	}
}

func analysisMigrationTone(speed float64) string {
	switch {
	case speed >= 18:
		return "high"
	case speed >= 8:
		return "medium"
	default:
		return "low"
	}
}

func analysisClusterName(side string) string {
	if side == "short" {
		return "上方空头清算带"
	}
	return "下方多头清算带"
}

func (a *App) buildAnalysisIndicators(currentPrice float64) []AnalysisIndicator {
	latest := map[int]analysisWindowPeak{}
	previous := map[int]analysisWindowPeak{}
	for _, days := range []int{1, 7, 30} {
		if item, ok := a.loadAnalysisWindowPeak(days, 0); ok {
			latest[days] = item
		}
		if item, ok := a.loadAnalysisWindowPeak(days, 1); ok {
			previous[days] = item
		}
	}
	if len(latest) == 0 {
		return nil
	}

	longPoints := analysisPeakPointsBySide(latest, "long")
	shortPoints := analysisPeakPointsBySide(latest, "short")
	longAgg := analysisWeightedValue(longPoints)
	shortAgg := analysisWeightedValue(shortPoints)
	dominantSide := "long"
	dominantPoints := longPoints
	dominantAgg := longAgg
	oppositeAgg := shortAgg
	if shortAgg > longAgg {
		dominantSide = "short"
		dominantPoints = shortPoints
		dominantAgg = shortAgg
		oppositeAgg = longAgg
	}
	if len(dominantPoints) == 0 {
		return nil
	}

	clusterPrice := analysisWeightedPrice(dominantPoints)
	spread := analysisPriceSpread(dominantPoints)
	coverage := float64(len(dominantPoints)) / 3 * 100
	tolerance := math.Max(25, currentPrice*0.012)
	proximity := clamp(100-(spread/tolerance)*100, 0, 100)
	overlapScore := clamp(coverage*0.55+proximity*0.45, 0, 100)

	distance := math.Abs(clusterPrice - currentPrice)
	distancePct := 0.0
	if currentPrice > 0 {
		distancePct = distance / currentPrice * 100
	}

	speedWeighted := 0.0
	speedWeight := 0.0
	directionWeighted := 0.0
	directionDetails := make([]string, 0, len(dominantPoints))
	for _, point := range dominantPoints {
		prev, ok := previous[point.Days]
		if !ok {
			continue
		}
		prevPrice := prev.LongPrice
		prevValue := prev.LongValue
		if dominantSide == "short" {
			prevPrice = prev.ShortPrice
			prevValue = prev.ShortValue
		}
		if prevPrice <= 0 || prevValue <= 0 || prev.GeneratedAt <= 0 || point.GeneratedAt <= prev.GeneratedAt {
			continue
		}
		hours := float64(point.GeneratedAt-prev.GeneratedAt) / float64(time.Hour/time.Millisecond)
		if hours <= 0 {
			continue
		}
		delta := point.Price - prevPrice
		weight := point.Weight * point.Value
		speedWeighted += math.Abs(delta) / hours * weight
		speedWeight += weight
		directionWeighted += delta * weight
		directionDetails = append(directionDetails, fmt.Sprintf("%s %+.1f", point.Window, delta))
	}
	migrationSpeed := 0.0
	migrationDirection := "变化不大"
	if speedWeight > 0 {
		migrationSpeed = speedWeighted / speedWeight
		if directionWeighted > 0 {
			migrationDirection = "主带上移"
		} else if directionWeighted < 0 {
			migrationDirection = "主带下移"
		}
	} else {
		migrationDirection = "样本不足"
	}
	migrationDetail := strings.Join(directionDetails, " | ")
	if migrationDetail == "" {
		migrationDetail = "暂无可比较窗口"
	}

	biasScore := 0.0
	if dominantAgg+oppositeAgg > 0 {
		biasScore = (shortAgg - longAgg) / (shortAgg + longAgg) * 100
	}
	biasDirection := "多空较均衡"
	if biasScore >= 12 {
		biasDirection = "偏向上方挤空"
	} else if biasScore <= -12 {
		biasDirection = "偏向下方踩踏"
	}

	return []AnalysisIndicator{
		{
			Label:    "多周期重合强度",
			Value:    fmt.Sprintf("%.0f/100", overlapScore),
			Subvalue: fmt.Sprintf("%s | %s", analysisClusterName(dominantSide), analysisFormatWindows(dominantPoints)),
			Tone:     analysisOverlapTone(overlapScore),
			Note:     fmt.Sprintf("最高柱跨周期最大价差 %.1f，参与窗口 %d/3。", spread, len(dominantPoints)),
		},
		{
			Label:    "主清算带迁移速度",
			Value:    fmt.Sprintf("%.1f 点/小时", migrationSpeed),
			Subvalue: migrationDirection,
			Tone:     analysisMigrationTone(migrationSpeed),
			Note:     fmt.Sprintf("对比上一份同窗口快照；%s。", migrationDetail),
		},
		{
			Label:    "现价到主清算带距离",
			Value:    fmt.Sprintf("%.1f 点", distance),
			Subvalue: fmt.Sprintf("主带 %.1f | %.2f%%", clusterPrice, distancePct),
			Tone:     analysisDistanceTone(distance, currentPrice),
			Note:     fmt.Sprintf("当前主带为%s，越近越容易触发放大波动。", analysisClusterName(dominantSide)),
		},
		{
			Label:    "多空清算压力偏向",
			Value:    fmt.Sprintf("%+.0f", biasScore),
			Subvalue: biasDirection,
			Tone:     analysisBiasTone(biasScore),
			Note:     fmt.Sprintf("正值代表上方空头清算堆积更强，负值代表下方多头清算堆积更强。加权强度 %.2fM / %.2fM。", shortAgg/1e6, longAgg/1e6),
		},
	}
}
