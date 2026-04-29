package liqmap

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

const (
	analysisDirectionBandWidthRatio    = 0.0035
	analysisDirectionBandWidthFloor    = 8.0
	analysisDirectionDeltaGrossRatio   = 0.02
	analysisDirectionDeltaFloorUSD     = 50_000.0
	analysisDirectionFlatScoreCutoff   = 12.0
	analysisDirectionMinStrongBandMove = 120_000.0
)

type analysisDirectionSnapshot struct {
	Window       string
	Days         int
	CapturedAt   int64
	CurrentPrice float64
	RangeLow     float64
	RangeHigh    float64
	Points       []WebDataSourcePoint
}

type analysisDirectionBand struct {
	Index      int
	Center     float64
	LongTotal  float64
	ShortTotal float64
	Net        float64
	Gross      float64
}

type analysisDirectionBandDelta struct {
	Index        int
	Center       float64
	LatestLong   float64
	LatestShort  float64
	LatestNet    float64
	LatestGross  float64
	PrevLong     float64
	PrevShort    float64
	PrevNet      float64
	PrevGross    float64
	DeltaLong    float64
	DeltaShort   float64
	DeltaNet     float64
	DeltaGross   float64
	Directional  float64
	DistancePct  float64
	Proximity    float64
	Intensity    float64
	Score        float64
	PrimarySide  string
	SupportDir   string
	ThresholdHit bool
}

type analysisDirectionWindowResult struct {
	Window         string
	Days           int
	Weight         float64
	RawWeight      float64
	Score          float64
	EffectiveDelta float64
	EffectiveBands int
	PositiveScore  float64
	NegativeScore  float64
	DominantShare  float64
	Direction      string
	DirectionLabel string
	StrongPositive []analysisDirectionBandDelta
	StrongNegative []analysisDirectionBandDelta
}

type analysisDirectionExplanation struct {
	WindowWeights   map[int]float64
	WindowScores    map[int]float64
	WindowBandCount map[int]int
	PositiveBands   []analysisDirectionBandDelta
	NegativeBands   []analysisDirectionBandDelta
	FinalScore      float64
	FlatThreshold   float64
}

type analysisDirectionDecision struct {
	Direction    string
	Confidence   float64
	Headline     string
	Summary      string
	CurrentPrice float64
	Explain      analysisDirectionExplanation
}

type analysisDirectionSignalSeed struct {
	SignalTS     int64
	GeneratedAt  int64
	CurrentPrice float64
	Symbol       string
}

func analysisDirectionFreshnessFactor(days int) float64 {
	switch days {
	case 1:
		return 1.35
	case 7:
		return 1.0
	case 30:
		return 0.75
	default:
		return 1.0
	}
}

func analysisDirectionWindowLabel(days int) string {
	switch days {
	case 1:
		return "1D"
	case 7:
		return "7D"
	case 30:
		return "30D"
	default:
		return fmt.Sprintf("%dD", days)
	}
}

func analysisDirectionPriceBandWidth(currentPrice float64) float64 {
	return math.Max(analysisDirectionBandWidthFloor, currentPrice*analysisDirectionBandWidthRatio)
}

func analysisDirectionBandIndex(price, currentPrice, bandWidth float64) int {
	if bandWidth <= 0 {
		return 0
	}
	return int(math.Round((price - currentPrice) / bandWidth))
}

func analysisDirectionBandCenter(index int, currentPrice, bandWidth float64) float64 {
	return currentPrice + float64(index)*bandWidth
}

func aggregateAnalysisDirectionBands(points []WebDataSourcePoint, currentPrice, bandWidth float64) map[int]analysisDirectionBand {
	out := make(map[int]analysisDirectionBand)
	for _, point := range points {
		price := point.Price
		value := point.LiqValue
		if !(price > 0) || !(value > 0) {
			continue
		}
		idx := analysisDirectionBandIndex(price, currentPrice, bandWidth)
		band := out[idx]
		band.Index = idx
		band.Center = analysisDirectionBandCenter(idx, currentPrice, bandWidth)
		side := strings.ToLower(strings.TrimSpace(point.Side))
		if side == "short" {
			band.ShortTotal += value
		} else {
			band.LongTotal += value
		}
		band.Net = band.ShortTotal - band.LongTotal
		band.Gross = band.ShortTotal + band.LongTotal
		out[idx] = band
	}
	return out
}

func buildAnalysisDirectionBandDeltas(latest, previous analysisDirectionSnapshot, currentPrice float64) []analysisDirectionBandDelta {
	bandWidth := analysisDirectionPriceBandWidth(currentPrice)
	latestBands := aggregateAnalysisDirectionBands(latest.Points, currentPrice, bandWidth)
	previousBands := aggregateAnalysisDirectionBands(previous.Points, currentPrice, bandWidth)
	keys := map[int]struct{}{}
	for idx := range latestBands {
		keys[idx] = struct{}{}
	}
	for idx := range previousBands {
		keys[idx] = struct{}{}
	}
	windowGross := 0.0
	for _, band := range latestBands {
		windowGross += band.Gross
	}
	threshold := math.Max(windowGross*analysisDirectionDeltaGrossRatio, analysisDirectionDeltaFloorUSD)
	out := make([]analysisDirectionBandDelta, 0, len(keys))
	for idx := range keys {
		curr := latestBands[idx]
		prev := previousBands[idx]
		delta := analysisDirectionBandDelta{
			Index:       idx,
			Center:      analysisDirectionBandCenter(idx, currentPrice, bandWidth),
			LatestLong:  curr.LongTotal,
			LatestShort: curr.ShortTotal,
			LatestNet:   curr.Net,
			LatestGross: curr.Gross,
			PrevLong:    prev.LongTotal,
			PrevShort:   prev.ShortTotal,
			PrevNet:     prev.Net,
			PrevGross:   prev.Gross,
			DeltaLong:   curr.LongTotal - prev.LongTotal,
			DeltaShort:  curr.ShortTotal - prev.ShortTotal,
			DeltaNet:    curr.Net - prev.Net,
			DeltaGross:  curr.Gross - prev.Gross,
		}
		if currentPrice > 0 {
			delta.DistancePct = math.Abs(delta.Center-currentPrice) / currentPrice * 100
		}
		delta.ThresholdHit = delta.LatestGross > 0 && math.Abs(delta.DeltaNet) >= threshold
		if !delta.ThresholdHit {
			continue
		}
		if delta.Center >= currentPrice {
			delta.Directional = delta.DeltaShort - delta.DeltaLong*0.65
			delta.PrimarySide = "short"
		} else {
			delta.Directional = delta.DeltaShort*0.65 - delta.DeltaLong
			delta.PrimarySide = "long"
		}
		delta.SupportDir = "down"
		if delta.Directional > 0 {
			delta.SupportDir = "up"
		}
		delta.Proximity = 1 / (1 + delta.DistancePct/0.6)
		delta.Intensity = math.Log1p(math.Abs(delta.DeltaNet) / 100000.0)
		delta.Score = math.Copysign(1, delta.Directional) * delta.Proximity * delta.Intensity
		out = append(out, delta)
	}
	sort.Slice(out, func(i, j int) bool {
		return math.Abs(out[i].DeltaNet) > math.Abs(out[j].DeltaNet)
	})
	return out
}

func buildAnalysisDirectionWindowResult(days int, latest, previous analysisDirectionSnapshot, currentPrice float64) analysisDirectionWindowResult {
	result := analysisDirectionWindowResult{
		Window: analysisDirectionWindowLabel(days),
		Days:   days,
	}
	deltas := buildAnalysisDirectionBandDeltas(latest, previous, currentPrice)
	if len(deltas) == 0 {
		result.RawWeight = analysisDirectionFreshnessFactor(days) * 0.35
		result.Direction = "flat"
		result.DirectionLabel = "中性"
		return result
	}

	signedScore := 0.0
	scoreAbs := 0.0
	positiveScore := 0.0
	negativeScore := 0.0
	effectiveDelta := 0.0
	positiveBands := make([]analysisDirectionBandDelta, 0, len(deltas))
	negativeBands := make([]analysisDirectionBandDelta, 0, len(deltas))
	for _, delta := range deltas {
		signedScore += delta.Score
		scoreAbs += math.Abs(delta.Score)
		effectiveDelta += math.Abs(delta.DeltaNet)
		if delta.Score >= 0 {
			positiveScore += math.Abs(delta.Score)
			positiveBands = append(positiveBands, delta)
		} else {
			negativeScore += math.Abs(delta.Score)
			negativeBands = append(negativeBands, delta)
		}
	}
	if scoreAbs > 0 {
		result.Score = clamp((signedScore/scoreAbs)*100, -100, 100)
	}
	result.PositiveScore = positiveScore
	result.NegativeScore = negativeScore
	result.EffectiveDelta = effectiveDelta
	result.EffectiveBands = len(deltas)
	if positiveScore+negativeScore > 0 {
		result.DominantShare = math.Max(positiveScore, negativeScore) / (positiveScore + negativeScore)
	}
	if result.Score >= analysisDirectionFlatScoreCutoff {
		result.Direction = "up"
		result.DirectionLabel = "向上"
	} else if result.Score <= -analysisDirectionFlatScoreCutoff {
		result.Direction = "down"
		result.DirectionLabel = "向下"
	} else {
		result.Direction = "flat"
		result.DirectionLabel = "中性"
	}
	result.StrongPositive = topAnalysisDirectionBands(positiveBands, 2)
	result.StrongNegative = topAnalysisDirectionBands(negativeBands, 2)

	consistencyFactor := 0.6 + 0.4*result.DominantShare
	activityFactor := clamp(math.Log1p(result.EffectiveDelta/300000.0), 0.35, 1.8)
	result.RawWeight = analysisDirectionFreshnessFactor(days) * consistencyFactor * activityFactor
	return result
}

func topAnalysisDirectionBands(items []analysisDirectionBandDelta, limit int) []analysisDirectionBandDelta {
	if len(items) == 0 {
		return nil
	}
	sort.Slice(items, func(i, j int) bool {
		return math.Abs(items[i].DeltaNet) > math.Abs(items[j].DeltaNet)
	})
	if limit > 0 && len(items) > limit {
		items = items[:limit]
	}
	out := make([]analysisDirectionBandDelta, len(items))
	copy(out, items)
	return out
}

func normalizeAnalysisDirectionWindowWeights(results []analysisDirectionWindowResult) {
	total := 0.0
	for _, item := range results {
		total += item.RawWeight
	}
	if total <= 0 {
		for i := range results {
			results[i].Weight = 1 / float64(len(results))
		}
		return
	}
	for i := range results {
		results[i].Weight = results[i].RawWeight / total
	}
}

func buildAnalysisDirectionSummary(direction string, confidence, finalScore float64, windows []analysisDirectionWindowResult, positive, negative []analysisDirectionBandDelta) (string, string) {
	headline := "多窗口清算带变化偏空"
	lead := "主导方向向下。"
	if direction == "up" {
		headline = "多窗口清算带变化偏多"
		lead = "主导方向向上。"
	}
	windowParts := make([]string, 0, len(windows))
	for _, item := range windows {
		windowParts = append(windowParts, fmt.Sprintf("%s 权重 %.0f%% / 分数 %+.1f / 有效带 %d", item.Window, item.Weight*100, item.Score, item.EffectiveBands))
	}
	topParts := make([]string, 0, 3)
	for _, item := range topAnalysisDirectionBands(append(append([]analysisDirectionBandDelta{}, positive...), negative...), 3) {
		topParts = append(topParts, formatAnalysisDirectionBandDelta(item))
	}
	confReason := "多窗口同向性一般"
	if confidence >= 80 {
		confReason = "多窗口同向且近价带变化集中"
	} else if confidence >= 60 {
		confReason = "主窗口方向明确，辅助窗口有跟随"
	}
	return headline, fmt.Sprintf("%s 综合得分 %+.1f，置信度 %.1f%%。%s。重点带：%s。%s", lead, finalScore, confidence, strings.Join(windowParts, " | "), strings.Join(topParts, " ; "), confReason)
}

func formatAnalysisDirectionBandDelta(item analysisDirectionBandDelta) string {
	support := "下压"
	if item.SupportDir == "up" {
		support = "上推"
	}
	side := "多头"
	if item.PrimarySide == "short" {
		side = "空头"
	}
	return fmt.Sprintf("%.1f %s带 %s %+0.2fM", item.Center, side, support, item.DeltaNet/1e6)
}

func buildAnalysisDirectionDecision(currentPrice float64, snapshots map[int][2]analysisDirectionSnapshot) (analysisDirectionDecision, bool) {
	results := make([]analysisDirectionWindowResult, 0, 3)
	for _, days := range []int{1, 7, 30} {
		pair, ok := snapshots[days]
		if !ok || len(pair[0].Points) == 0 || len(pair[1].Points) == 0 {
			continue
		}
		results = append(results, buildAnalysisDirectionWindowResult(days, pair[0], pair[1], currentPrice))
	}
	if len(results) == 0 {
		return analysisDirectionDecision{}, false
	}
	normalizeAnalysisDirectionWindowWeights(results)

	finalScore := 0.0
	breadthCount := 0
	positiveBands := make([]analysisDirectionBandDelta, 0, 6)
	negativeBands := make([]analysisDirectionBandDelta, 0, 6)
	weights := map[int]float64{}
	scores := map[int]float64{}
	counts := map[int]int{}
	oneDayAligned := false
	coherenceSum := 0.0
	for _, item := range results {
		finalScore += item.Score * item.Weight
		weights[item.Days] = item.Weight
		scores[item.Days] = item.Score
		counts[item.Days] = item.EffectiveBands
		breadthCount += item.EffectiveBands
		coherenceSum += item.DominantShare * item.Weight
		positiveBands = append(positiveBands, item.StrongPositive...)
		negativeBands = append(negativeBands, item.StrongNegative...)
		if item.Days == 1 && item.Direction != "flat" {
			oneDayAligned = true
		}
	}
	direction := "flat"
	if finalScore >= analysisDirectionFlatScoreCutoff {
		direction = "up"
	} else if finalScore <= -analysisDirectionFlatScoreCutoff {
		direction = "down"
	}
	if direction == "flat" {
		return analysisDirectionDecision{}, false
	}

	agreementBonus := 0.0
	for _, item := range results {
		if item.Direction == direction {
			agreementBonus += item.Weight * 10
		} else if item.Direction != "flat" {
			agreementBonus -= item.Weight * 6
		}
	}
	if oneDayAligned {
		agreementBonus += 6
	}
	breadthBonus := math.Min(10, float64(breadthCount)*1.1)
	fragmentationPenalty := math.Max(0, (1-coherenceSum)*18)
	confidence := clamp(22+math.Abs(finalScore)*0.64+agreementBonus+breadthBonus-fragmentationPenalty, 18, 92)
	headline, summary := buildAnalysisDirectionSummary(direction, confidence, finalScore, results, positiveBands, negativeBands)

	return analysisDirectionDecision{
		Direction:    direction,
		Confidence:   math.Round(confidence*10) / 10,
		Headline:     headline,
		Summary:      summary,
		CurrentPrice: currentPrice,
		Explain: analysisDirectionExplanation{
			WindowWeights:   weights,
			WindowScores:    scores,
			WindowBandCount: counts,
			PositiveBands:   topAnalysisDirectionBands(positiveBands, 3),
			NegativeBands:   topAnalysisDirectionBands(negativeBands, 3),
			FinalScore:      finalScore,
			FlatThreshold:   analysisDirectionFlatScoreCutoff,
		},
	}, true
}

func (a *App) loadAnalysisDirectionSnapshotBefore(days int, beforeOrAt int64, offset int) (analysisDirectionSnapshot, bool) {
	var (
		snapshotID int64
		capturedAt int64
		rangeLow   float64
		rangeHigh  float64
		payloadRaw string
	)
	query := `SELECT id, captured_at, range_low, range_high, payload_json
		FROM webdatasource_snapshots
		WHERE symbol='ETH' AND window_days=?
			AND EXISTS (SELECT 1 FROM webdatasource_points WHERE snapshot_id=webdatasource_snapshots.id LIMIT 1)`
	args := []any{days}
	if beforeOrAt > 0 {
		query += ` AND captured_at<=?`
		args = append(args, beforeOrAt)
	}
	query += ` ORDER BY captured_at DESC LIMIT 1 OFFSET ?`
	args = append(args, offset)
	err := a.db.QueryRow(query, args...).Scan(&snapshotID, &capturedAt, &rangeLow, &rangeHigh, &payloadRaw)
	if err != nil {
		return analysisDirectionSnapshot{}, false
	}
	rows, err := a.db.Query(`SELECT exchange, side, price, liq_value FROM webdatasource_points WHERE snapshot_id=?`, snapshotID)
	if err != nil {
		return analysisDirectionSnapshot{}, false
	}
	defer rows.Close()
	points := make([]WebDataSourcePoint, 0, 512)
	for rows.Next() {
		var point WebDataSourcePoint
		if err := rows.Scan(&point.Exchange, &point.Side, &point.Price, &point.LiqValue); err != nil {
			return analysisDirectionSnapshot{}, false
		}
		points = append(points, point)
	}
	out := analysisDirectionSnapshot{
		Window:     analysisDirectionWindowLabel(days),
		Days:       days,
		CapturedAt: capturedAt,
		RangeLow:   rangeLow,
		RangeHigh:  rangeHigh,
		Points:     points,
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

func (a *App) loadAnalysisDirectionSnapshot(days, offset int) (analysisDirectionSnapshot, bool) {
	return a.loadAnalysisDirectionSnapshotBefore(days, 0, offset)
}

func (a *App) buildAnalysisDirectionSignalRecordFromSeed(seed analysisDirectionSignalSeed) (AnalysisSignalRecord, bool) {
	pairs := map[int][2]analysisDirectionSnapshot{}
	currentPrice := seed.CurrentPrice
	for _, days := range []int{1, 7, 30} {
		latest, okLatest := a.loadAnalysisDirectionSnapshotBefore(days, seed.SignalTS, 0)
		previous, okPrevious := a.loadAnalysisDirectionSnapshotBefore(days, seed.SignalTS, 1)
		if !okLatest || !okPrevious {
			continue
		}
		if latest.CurrentPrice > 0 {
			currentPrice = latest.CurrentPrice
		}
		pairs[days] = [2]analysisDirectionSnapshot{latest, previous}
	}
	decision, ok := buildAnalysisDirectionDecision(currentPrice, pairs)
	if !ok {
		return AnalysisSignalRecord{}, false
	}
	symbol := strings.TrimSpace(seed.Symbol)
	if symbol == "" {
		symbol = defaultSymbol
	}
	return AnalysisSignalRecord{
		SignalTS:          seed.SignalTS,
		Symbol:            symbol,
		SourceGroup:       5,
		Direction:         decision.Direction,
		Confidence:        decision.Confidence,
		SignalPrice:       currentPrice,
		AnalysisGenerated: seed.GeneratedAt,
		Headline:          decision.Headline,
		Summary:           decision.Summary,
		VerifyHorizonMin:  analysisSignalVerifyHorizonMin,
	}, true
}

func (a *App) buildAnalysisDirectionSignalRecord(snapshot AnalysisSnapshot) (AnalysisSignalRecord, bool) {
	return a.buildAnalysisDirectionSignalRecordFromSeed(analysisDirectionSignalSeed{
		SignalTS:     time.Now().UnixMilli(),
		GeneratedAt:  snapshot.GeneratedAt,
		CurrentPrice: snapshot.CurrentPrice,
		Symbol:       snapshot.Symbol,
	})
}

func (a *App) listAnalysisDirectionSignalSeeds(hours int) ([]analysisDirectionSignalSeed, error) {
	if hours <= 0 {
		hours = 24
	}
	nowTS := time.Now().UnixMilli()
	sinceTS := nowTS - int64(hours)*int64(time.Hour/time.Millisecond)
	rows, err := a.db.Query(`SELECT captured_at, payload_json, range_low, range_high
		FROM webdatasource_snapshots
		WHERE symbol='ETH' AND window_days=1 AND captured_at>=?
			AND EXISTS (SELECT 1 FROM webdatasource_points WHERE snapshot_id=webdatasource_snapshots.id LIMIT 1)
		ORDER BY captured_at ASC`, sinceTS)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]analysisDirectionSignalSeed, 0, 288)
	for rows.Next() {
		var capturedAt int64
		var payloadRaw string
		var rangeLow, rangeHigh float64
		if err := rows.Scan(&capturedAt, &payloadRaw, &rangeLow, &rangeHigh); err != nil {
			return nil, err
		}
		currentPrice := 0.0
		if strings.TrimSpace(payloadRaw) != "" {
			var payload map[string]any
			if err := json.Unmarshal([]byte(payloadRaw), &payload); err == nil {
				currentPrice = webDataSourcePayloadCurrentPrice(payload, rangeLow, rangeHigh)
			}
		}
		if currentPrice <= 0 && rangeHigh > rangeLow {
			currentPrice = math.Round(((rangeHigh+rangeLow)/2)*10) / 10
		}
		out = append(out, analysisDirectionSignalSeed{
			SignalTS:     capturedAt,
			GeneratedAt:  capturedAt,
			CurrentPrice: currentPrice,
			Symbol:       defaultSymbol,
		})
	}
	return out, rows.Err()
}

func (a *App) BackfillAnalysisDirectionSignals(hours int) (int, error) {
	seeds, err := a.listAnalysisDirectionSignalSeeds(hours)
	if err != nil {
		return 0, err
	}
	if len(seeds) == 0 {
		return 0, nil
	}
	records := make([]AnalysisSignalRecord, 0, len(seeds))
	sinceTS := seeds[0].SignalTS
	for _, seed := range seeds {
		record, ok := a.buildAnalysisDirectionSignalRecordFromSeed(seed)
		if !ok {
			continue
		}
		records = append(records, record)
	}
	tx, err := a.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM analysis_direction_signals WHERE symbol=? AND signal_ts>=?`, defaultSymbol, sinceTS); err != nil {
		return 0, err
	}
	stmt, err := tx.Prepare(`INSERT INTO analysis_direction_signals(signal_ts, symbol, source_group, direction, confidence, signal_price, analysis_generated_at, headline, summary, verify_horizon_min)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return 0, err
	}
	defer stmt.Close()
	for _, record := range records {
		if _, err := stmt.Exec(
			record.SignalTS,
			record.Symbol,
			record.SourceGroup,
			record.Direction,
			record.Confidence,
			record.SignalPrice,
			record.AnalysisGenerated,
			record.Headline,
			record.Summary,
			record.VerifyHorizonMin,
		); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return len(records), nil
}
