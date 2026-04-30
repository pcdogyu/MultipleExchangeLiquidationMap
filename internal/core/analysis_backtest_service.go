package liqmap

import (
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const analysisSignalVerifyHorizonMin = 5

var analysisBacktestHorizons = []int{5, 15, 30, 60}

const analysisLiquidationBacktestSampleInterval = "5m"
const analysisLiquidationBacktestSourceGroup = 8

const (
	analysisSecondFactorAll           = "all"
	analysisSecondFactorVolumeSpike   = "volume_spike"
	analysisSecondFactorVolumeNormal  = "volume_normal"
	analysisSecondFactorVolumeLow     = "volume_low"
	analysisSecondFactorInsufficient  = "insufficient"
	analysisBacktestQualityAll        = "all"
	analysisBacktestQualityHigh       = "high"
	analysisBacktestStrategyAll       = "all"
	analysisBacktestStrategyPreferred = "preferred"
	analysisBacktestNoiseNone         = "none"
	analysisBacktestNoisePersistence  = "persistence"
)

var analysisDirectionSummaryScoreRe = regexp.MustCompile(`分数\s*([+-]?\d+(?:\.\d+)?)`)

type analysisBacktestCandle struct {
	TS          int64
	O           float64
	H           float64
	L           float64
	C           float64
	QuoteVolume float64
}

type analysisBacktestIntervalConfig struct {
	Label    string
	Source   string
	BucketMS int64
	Direct   bool
}

func normalizeAnalysisBacktestInterval(interval string) analysisBacktestIntervalConfig {
	switch strings.ToLower(strings.TrimSpace(interval)) {
	case "1m":
		return analysisBacktestIntervalConfig{Label: "1m", Source: "1m", BucketMS: int64(time.Minute / time.Millisecond), Direct: true}
	case "15m":
		return analysisBacktestIntervalConfig{Label: "15m", Source: "15m", BucketMS: int64(15 * time.Minute / time.Millisecond), Direct: true}
	case "30m":
		return analysisBacktestIntervalConfig{Label: "30m", Source: "30m", BucketMS: int64(30 * time.Minute / time.Millisecond), Direct: true}
	case "1h":
		return analysisBacktestIntervalConfig{Label: "1h", Source: "1h", BucketMS: int64(time.Hour / time.Millisecond), Direct: true}
	case "4h":
		return analysisBacktestIntervalConfig{Label: "4h", Source: "1h", BucketMS: int64(4 * time.Hour / time.Millisecond), Direct: false}
	case "8h":
		return analysisBacktestIntervalConfig{Label: "8h", Source: "1h", BucketMS: int64(8 * time.Hour / time.Millisecond), Direct: false}
	case "12h":
		return analysisBacktestIntervalConfig{Label: "12h", Source: "1h", BucketMS: int64(12 * time.Hour / time.Millisecond), Direct: false}
	case "24h":
		return analysisBacktestIntervalConfig{Label: "24h", Source: "1h", BucketMS: int64(24 * time.Hour / time.Millisecond), Direct: false}
	case "48h":
		return analysisBacktestIntervalConfig{Label: "48h", Source: "1h", BucketMS: int64(48 * time.Hour / time.Millisecond), Direct: false}
	case "72h":
		return analysisBacktestIntervalConfig{Label: "72h", Source: "1h", BucketMS: int64(72 * time.Hour / time.Millisecond), Direct: false}
	case "", "5m":
		fallthrough
	default:
		return analysisBacktestIntervalConfig{Label: "5m", Source: "5m", BucketMS: int64(5 * time.Minute / time.Millisecond), Direct: true}
	}
}

func normalizeAnalysisDirection(direction string) string {
	switch strings.ToLower(strings.TrimSpace(direction)) {
	case "up":
		return "up"
	case "down":
		return "down"
	default:
		return "flat"
	}
}

func parseFlexibleFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case int32:
		return float64(n), true
	case jsonNumber:
		f, err := strconv.ParseFloat(string(n), 64)
		return f, err == nil
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(n), 64)
		return f, err == nil
	default:
		return 0, false
	}
}

type jsonNumber string

func parseFlexibleInt64(v any) (int64, bool) {
	switch n := v.(type) {
	case int64:
		return n, true
	case int:
		return int64(n), true
	case float64:
		return int64(n), true
	case string:
		i, err := strconv.ParseInt(strings.TrimSpace(n), 10, 64)
		return i, err == nil
	default:
		if f, ok := parseFlexibleFloat(v); ok {
			return int64(f), true
		}
		return 0, false
	}
}

func parseAnalysisCandleQuoteVolume(row []any) float64 {
	if len(row) > 7 {
		if v, ok := parseFlexibleFloat(row[7]); ok && v > 0 {
			return v
		}
	}
	if len(row) > 5 {
		if v, ok := parseFlexibleFloat(row[5]); ok && v > 0 {
			return v
		}
	}
	return 0
}

func parseAnalysisBacktestCandles(rows any) []analysisBacktestCandle {
	list, ok := rows.([][]any)
	if !ok {
		if generic, ok := rows.([]any); ok {
			list = make([][]any, 0, len(generic))
			for _, item := range generic {
				if row, ok := item.([]any); ok {
					list = append(list, row)
				}
			}
		}
	}
	out := make([]analysisBacktestCandle, 0, len(list))
	for _, row := range list {
		if len(row) < 5 {
			continue
		}
		ts, okTS := parseFlexibleInt64(row[0])
		open, okO := parseFlexibleFloat(row[1])
		high, okH := parseFlexibleFloat(row[2])
		low, okL := parseFlexibleFloat(row[3])
		closePrice, okC := parseFlexibleFloat(row[4])
		if !okTS || !okO || !okH || !okL || !okC {
			continue
		}
		out = append(out, analysisBacktestCandle{
			TS:          ts,
			O:           open,
			H:           high,
			L:           low,
			C:           closePrice,
			QuoteVolume: parseAnalysisCandleQuoteVolume(row),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].TS < out[j].TS })
	return out
}

func floorToFiveMinute(ts int64) int64 {
	const step = int64(5 * time.Minute / time.Millisecond)
	if ts <= 0 {
		return 0
	}
	return ts - (ts % step)
}

func bucketStart(ts, bucketMS int64) int64 {
	if ts <= 0 || bucketMS <= 0 {
		return ts
	}
	return ts - (ts % bucketMS)
}

func aggregateAnalysisBacktestCandles(candles []analysisBacktestCandle, bucketMS int64) []analysisBacktestCandle {
	if bucketMS <= 0 || len(candles) == 0 {
		return nil
	}
	out := make([]analysisBacktestCandle, 0, len(candles))
	var current *analysisBacktestCandle
	for _, candle := range candles {
		ts := bucketStart(candle.TS, bucketMS)
		if current == nil || current.TS != ts {
			if current != nil {
				out = append(out, *current)
			}
			copyCandle := analysisBacktestCandle{TS: ts, O: candle.O, H: candle.H, L: candle.L, C: candle.C, QuoteVolume: candle.QuoteVolume}
			current = &copyCandle
			continue
		}
		if candle.H > current.H {
			current.H = candle.H
		}
		if candle.L < current.L {
			current.L = candle.L
		}
		current.C = candle.C
		current.QuoteVolume += candle.QuoteVolume
	}
	if current != nil {
		out = append(out, *current)
	}
	return out
}

func findCandleCloseForTS(candles []analysisBacktestCandle, ts int64) (float64, bool) {
	if ts <= 0 {
		return 0, false
	}
	want := floorToFiveMinute(ts)
	for _, candle := range candles {
		if candle.TS == want {
			return candle.C, true
		}
	}
	return 0, false
}

func normalizedAnalysisVerifyHorizonMin(v int) int {
	if v != analysisSignalVerifyHorizonMin {
		return analysisSignalVerifyHorizonMin
	}
	return v
}

func analysisBacktestHorizonsForSignal(records []AnalysisSignalRecord, index int) []int {
	horizons := append([]int(nil), analysisBacktestHorizons...)
	if index < 0 || index >= len(records)-1 {
		return horizons
	}
	current := records[index]
	currentDirection := normalizeAnalysisDirection(current.Direction)
	if currentDirection == "flat" {
		return horizons
	}

	oppositeTS := int64(0)
	for i := index + 1; i < len(records); i++ {
		nextDirection := normalizeAnalysisDirection(records[i].Direction)
		if nextDirection == "flat" || nextDirection == currentDirection {
			continue
		}
		oppositeTS = records[i].SignalTS
		break
	}
	if oppositeTS <= 0 {
		return horizons
	}

	out := make([]int, 0, len(horizons))
	const minuteMS = int64(time.Minute / time.Millisecond)
	for _, horizonMin := range horizons {
		dueTS := current.SignalTS + int64(horizonMin)*minuteMS
		if dueTS < oppositeTS {
			out = append(out, horizonMin)
		}
	}
	if len(out) == 0 {
		return []int{analysisSignalVerifyHorizonMin}
	}
	return out
}

func normalizeAnalysisSecondFactorKey(v string) string {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "", analysisSecondFactorAll, "aligned", "counter", "range":
		return analysisSecondFactorAll
	case analysisSecondFactorVolumeSpike:
		return analysisSecondFactorVolumeSpike
	case analysisSecondFactorVolumeNormal:
		return analysisSecondFactorVolumeNormal
	case analysisSecondFactorVolumeLow:
		return analysisSecondFactorVolumeLow
	case analysisSecondFactorInsufficient:
		return analysisSecondFactorInsufficient
	default:
		return analysisSecondFactorAll
	}
}

func analysisSecondFactorLabel(key string) string {
	switch normalizeAnalysisSecondFactorKey(key) {
	case analysisSecondFactorVolumeSpike:
		return "放量"
	case analysisSecondFactorVolumeNormal:
		return "正常"
	case analysisSecondFactorVolumeLow:
		return "缩量"
	case analysisSecondFactorInsufficient:
		return "样本不足"
	default:
		return "全部"
	}
}

func findCandleIndexForTS(candles []analysisBacktestCandle, ts int64) int {
	want := floorToFiveMinute(ts)
	for i := len(candles) - 1; i >= 0; i-- {
		if candles[i].TS == want {
			return i
		}
		if candles[i].TS < want {
			break
		}
	}
	return -1
}

func classifyAnalysisSecondFactor(record AnalysisSignalRecord, candles []analysisBacktestCandle) (string, string) {
	const lookbackCandles = 12
	idx := findCandleIndexForTS(candles, record.SignalTS)
	if idx < lookbackCandles {
		return analysisSecondFactorInsufficient, analysisSecondFactorLabel(analysisSecondFactorInsufficient)
	}
	currentVolume := candles[idx].QuoteVolume
	if !(currentVolume > 0) {
		return analysisSecondFactorInsufficient, analysisSecondFactorLabel(analysisSecondFactorInsufficient)
	}
	sum := 0.0
	for i := idx - lookbackCandles; i < idx; i++ {
		if !(candles[i].QuoteVolume > 0) {
			return analysisSecondFactorInsufficient, analysisSecondFactorLabel(analysisSecondFactorInsufficient)
		}
		sum += candles[i].QuoteVolume
	}
	avg := sum / lookbackCandles
	if !(avg > 0) {
		return analysisSecondFactorInsufficient, analysisSecondFactorLabel(analysisSecondFactorInsufficient)
	}
	ratio := currentVolume / avg
	if ratio >= 1.2 {
		return analysisSecondFactorVolumeSpike, analysisSecondFactorLabel(analysisSecondFactorVolumeSpike)
	}
	if ratio < 0.8 {
		return analysisSecondFactorVolumeLow, analysisSecondFactorLabel(analysisSecondFactorVolumeLow)
	}
	return analysisSecondFactorVolumeNormal, analysisSecondFactorLabel(analysisSecondFactorVolumeNormal)
}

func buildAnalysisSignalHorizonResult(record AnalysisSignalRecord, direction string, horizonMin int, candles []analysisBacktestCandle, nowTS int64) AnalysisSignalHorizonResult {
	out := AnalysisSignalHorizonResult{
		HorizonMin:  horizonMin,
		VerifyDueTS: record.SignalTS + int64(horizonMin)*int64(time.Minute/time.Millisecond),
		Result:      "pending",
	}
	if nowTS < out.VerifyDueTS {
		return out
	}
	verifyClose, ok := findCandleCloseForTS(candles, out.VerifyDueTS)
	if !ok {
		out.Result = "no_data"
		return out
	}
	out.VerifyClosePrice = verifyClose
	out.DeltaPrice = verifyClose - record.SignalPrice
	if record.SignalPrice > 0 {
		out.DeltaPct = (out.DeltaPrice / record.SignalPrice) * 100
	}
	switch direction {
	case "up":
		if out.DeltaPrice > 0 {
			out.Result = "correct"
		} else {
			out.Result = "wrong"
		}
	case "down":
		if out.DeltaPrice < 0 {
			out.Result = "correct"
		} else {
			out.Result = "wrong"
		}
	default:
		out.Result = "no_data"
	}
	return out
}

func buildAnalysisSignalResult(record AnalysisSignalRecord, candles []analysisBacktestCandle, nowTS int64, horizons []int) AnalysisSignalResult {
	secondFactorKey, secondFactorLabel := classifyAnalysisSecondFactor(record, candles)
	direction := normalizeAnalysisDirection(record.Direction)
	if len(horizons) == 0 {
		horizons = analysisBacktestHorizons
	}
	result := AnalysisSignalResult{
		ID:                record.ID,
		SignalTS:          record.SignalTS,
		Symbol:            record.Symbol,
		SourceGroup:       record.SourceGroup,
		Direction:         direction,
		Confidence:        record.Confidence,
		SignalPrice:       record.SignalPrice,
		AnalysisGenerated: record.AnalysisGenerated,
		Headline:          record.Headline,
		Summary:           record.Summary,
		VerifyHorizonMin:  normalizedAnalysisVerifyHorizonMin(record.VerifyHorizonMin),
		SecondFactorKey:   secondFactorKey,
		SecondFactorLabel: secondFactorLabel,
		VerifyDueTS:       record.SignalTS + int64(normalizedAnalysisVerifyHorizonMin(record.VerifyHorizonMin))*int64(time.Minute/time.Millisecond),
		Result:            "pending",
	}
	result.Horizons = make([]AnalysisSignalHorizonResult, 0, len(horizons))
	for _, horizonMin := range horizons {
		outcome := buildAnalysisSignalHorizonResult(record, direction, horizonMin, candles, nowTS)
		result.Horizons = append(result.Horizons, outcome)
		if horizonMin == normalizedAnalysisVerifyHorizonMin(record.VerifyHorizonMin) {
			result.VerifyDueTS = outcome.VerifyDueTS
			result.VerifyClosePrice = outcome.VerifyClosePrice
			result.Result = outcome.Result
			result.DeltaPrice = outcome.DeltaPrice
			result.DeltaPct = outcome.DeltaPct
		}
	}
	return result
}

func analysisDirectionWeightForBucket(hours int) float64 {
	switch hours {
	case 1:
		return 8
	case 4:
		return 4
	case 12:
		return 2
	case 24:
		return 1
	default:
		return 1
	}
}

func analysisDirectionSignFromBucket(bucket LiquidationPeriodBucket) float64 {
	switch strings.ToLower(strings.TrimSpace(bucket.PricePush)) {
	case "up":
		return 1
	case "down":
		return -1
	default:
		if strings.ToLower(strings.TrimSpace(bucket.DominantSide)) == "short" {
			return 1
		}
		if strings.ToLower(strings.TrimSpace(bucket.DominantSide)) == "long" {
			return -1
		}
		return 0
	}
}

func buildLiquidationOnlyDirectionSignal(snapshot AnalysisSnapshot, period LiquidationPeriodSummary) (AnalysisSignalRecord, bool) {
	record := AnalysisSignalRecord{
		SignalTS:          time.Now().UnixMilli(),
		Symbol:            strings.TrimSpace(snapshot.Symbol),
		SourceGroup:       5,
		SignalPrice:       snapshot.CurrentPrice,
		AnalysisGenerated: snapshot.GeneratedAt,
		VerifyHorizonMin:  analysisSignalVerifyHorizonMin,
	}
	if record.Symbol == "" {
		record.Symbol = defaultSymbol
	}

	var weightedScore float64
	var activeWeight float64
	var activePeriods int
	var strongPeriods int
	details := make([]string, 0, len(period.Buckets))
	for _, bucket := range period.Buckets {
		if bucket.TotalUSD <= 0 {
			continue
		}
		weight := analysisDirectionWeightForBucket(bucket.Hours)
		sign := analysisDirectionSignFromBucket(bucket)
		if sign == 0 {
			continue
		}
		ratio := clamp(bucket.BalanceRatio, 0, 1)
		weightedScore += sign * weight * ratio
		activeWeight += weight
		activePeriods++
		if ratio >= 0.18 {
			strongPeriods++
		}
		details = append(details, fmt.Sprintf("%s %s %.0f%%", bucket.Label, bucket.PricePushLabel, ratio*100))
	}
	if activeWeight <= 0 {
		return AnalysisSignalRecord{}, false
	}

	normalized := weightedScore / activeWeight
	strength := math.Abs(normalized)
	if strength < 0.12 {
		return AnalysisSignalRecord{}, false
	}

	direction := "down"
	title := "单因子清算柱偏空"
	bias := "多周期清算柱整体偏向下压。"
	if normalized > 0 {
		direction = "up"
		title = "单因子清算柱偏多"
		bias = "多周期清算柱整体偏向上推。"
	}

	confidence := clamp(strength*100*0.88+float64(strongPeriods)*4, 18, 92)
	record.Direction = direction
	record.Confidence = math.Round(confidence*10) / 10
	record.Headline = title
	record.Summary = fmt.Sprintf("%s 形态 %s，活跃周期 %d 个，强共振 %d 个。%s", bias, strings.TrimSpace(period.Pattern.Code), activePeriods, strongPeriods, strings.Join(details, " | "))
	return record, true
}

func (a *App) listAnalysisDirectionSignals(sinceTS int64, limit, offset int) ([]AnalysisSignalRecord, error) {
	if limit <= 0 {
		limit = 200
	}
	if offset < 0 {
		offset = 0
	}
	rows, err := a.db.Query(`SELECT id, signal_ts, symbol, source_group, direction, confidence, signal_price, analysis_generated_at, headline, summary, verify_horizon_min
		FROM analysis_direction_signals
		WHERE signal_ts>=?
		ORDER BY signal_ts DESC, id DESC
		LIMIT ? OFFSET ?`, sinceTS, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]AnalysisSignalRecord, 0, limit)
	for rows.Next() {
		var item AnalysisSignalRecord
		if err := rows.Scan(&item.ID, &item.SignalTS, &item.Symbol, &item.SourceGroup, &item.Direction, &item.Confidence, &item.SignalPrice, &item.AnalysisGenerated, &item.Headline, &item.Summary, &item.VerifyHorizonMin); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (a *App) listAnalysisDirectionSignalsPage(limit, page int) ([]AnalysisSignalRecord, error) {
	if limit <= 0 {
		limit = 50
	}
	if page <= 0 {
		page = 1
	}
	offset := (page - 1) * limit
	rows, err := a.db.Query(`SELECT id, signal_ts, symbol, source_group, direction, confidence, signal_price, analysis_generated_at, headline, summary, verify_horizon_min
		FROM analysis_direction_signals
		ORDER BY signal_ts DESC, id DESC
		LIMIT ? OFFSET ?`, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]AnalysisSignalRecord, 0, limit)
	for rows.Next() {
		var item AnalysisSignalRecord
		if err := rows.Scan(&item.ID, &item.SignalTS, &item.Symbol, &item.SourceGroup, &item.Direction, &item.Confidence, &item.SignalPrice, &item.AnalysisGenerated, &item.Headline, &item.Summary, &item.VerifyHorizonMin); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func analysisBacktestSignalLimit(hours int) int {
	if hours <= 0 {
		return 500
	}
	limit := hours * 12
	if limit < 500 {
		return 500
	}
	if limit > 5000 {
		return 5000
	}
	return limit
}

func buildAnalysisBacktestSummary(results []AnalysisSignalResult, hours int) AnalysisBacktestSummary {
	out := AnalysisBacktestSummary{WindowHours: hours, TotalSignals: len(results)}
	for _, item := range results {
		switch item.Result {
		case "correct":
			out.CorrectCount++
		case "wrong":
			out.WrongCount++
		case "pending":
			out.PendingCount++
		case "no_data":
			out.NoDataCount++
		}
	}
	verified := out.CorrectCount + out.WrongCount
	if verified > 0 {
		out.CorrectRate = math.Round((float64(out.CorrectCount)/float64(verified))*1000) / 10
	}
	return out
}

func buildAnalysisBacktestHorizonStats(results []AnalysisSignalResult) []AnalysisBacktestHorizonStat {
	out := make([]AnalysisBacktestHorizonStat, 0, len(analysisBacktestHorizons))
	for _, horizonMin := range analysisBacktestHorizons {
		stat := AnalysisBacktestHorizonStat{
			HorizonMin: horizonMin,
			Label:      fmt.Sprintf("%dm", horizonMin),
		}
		if horizonMin == 60 {
			stat.Label = "1h"
		}
		for _, item := range results {
			for _, horizon := range item.Horizons {
				if horizon.HorizonMin != horizonMin {
					continue
				}
				stat.TotalSignals++
				switch horizon.Result {
				case "correct":
					stat.CorrectCount++
				case "wrong":
					stat.WrongCount++
				case "pending":
					stat.PendingCount++
				case "no_data":
					stat.NoDataCount++
				}
				break
			}
		}
		verified := stat.CorrectCount + stat.WrongCount
		if verified > 0 {
			stat.CorrectRate = math.Round((float64(stat.CorrectCount)/float64(verified))*1000) / 10
		}
		out = append(out, stat)
	}
	return out
}

type analysisConfidenceBucketDef struct {
	Label        string
	MinInclusive float64
	MaxExclusive float64
}

func analysisConfidenceBucketDefs() []analysisConfidenceBucketDef {
	return []analysisConfidenceBucketDef{
		{Label: "<70", MinInclusive: 0, MaxExclusive: 70},
		{Label: "70-80", MinInclusive: 70, MaxExclusive: 80},
		{Label: "80-85", MinInclusive: 80, MaxExclusive: 85},
		{Label: "85-90", MinInclusive: 85, MaxExclusive: 90},
		{Label: "90+", MinInclusive: 90, MaxExclusive: 0},
	}
}

func buildAnalysisBacktestConfidenceBuckets(results []AnalysisSignalResult) []AnalysisBacktestConfidenceBucket {
	defs := analysisConfidenceBucketDefs()
	out := make([]AnalysisBacktestConfidenceBucket, 0, len(defs))
	for _, def := range defs {
		bucket := AnalysisBacktestConfidenceBucket{
			Label:        def.Label,
			MinInclusive: def.MinInclusive,
			MaxExclusive: def.MaxExclusive,
		}
		for _, item := range results {
			confidence := item.Confidence
			if confidence < def.MinInclusive {
				continue
			}
			if def.MaxExclusive > 0 && confidence >= def.MaxExclusive {
				continue
			}
			bucket.TotalSignals++
			switch item.Result {
			case "correct":
				bucket.CorrectCount++
			case "wrong":
				bucket.WrongCount++
			case "pending":
				bucket.PendingCount++
			case "no_data":
				bucket.NoDataCount++
			}
		}
		verified := bucket.CorrectCount + bucket.WrongCount
		if verified > 0 {
			bucket.CorrectRate = math.Round((float64(bucket.CorrectCount)/float64(verified))*1000) / 10
		}
		out = append(out, bucket)
	}
	return out
}

func buildAnalysisBacktestConfidenceFactorBuckets(results []AnalysisSignalResult) []AnalysisBacktestConfidenceFactorBucket {
	defs := analysisConfidenceBucketDefs()
	factors := []string{
		analysisSecondFactorVolumeSpike,
		analysisSecondFactorVolumeNormal,
		analysisSecondFactorVolumeLow,
	}
	out := make([]AnalysisBacktestConfidenceFactorBucket, 0, len(defs)*len(factors))
	for _, def := range defs {
		for _, factor := range factors {
			bucket := AnalysisBacktestConfidenceFactorBucket{
				Label:        def.Label,
				MinInclusive: def.MinInclusive,
				MaxExclusive: def.MaxExclusive,
				FactorKey:    factor,
				FactorLabel:  analysisSecondFactorLabel(factor),
			}
			for _, item := range results {
				confidence := item.Confidence
				if confidence < def.MinInclusive {
					continue
				}
				if def.MaxExclusive > 0 && confidence >= def.MaxExclusive {
					continue
				}
				if normalizeAnalysisSecondFactorKey(item.SecondFactorKey) != factor {
					continue
				}
				bucket.TotalSignals++
				switch item.Result {
				case "correct":
					bucket.CorrectCount++
				case "wrong":
					bucket.WrongCount++
				case "pending":
					bucket.PendingCount++
				case "no_data":
					bucket.NoDataCount++
				}
			}
			verified := bucket.CorrectCount + bucket.WrongCount
			if verified > 0 {
				bucket.CorrectRate = math.Round((float64(bucket.CorrectCount)/float64(verified))*1000) / 10
			}
			out = append(out, bucket)
		}
	}
	return out
}

func analysisConfidenceBucketForValue(confidence float64) analysisConfidenceBucketDef {
	defs := analysisConfidenceBucketDefs()
	for _, def := range defs {
		if confidence < def.MinInclusive {
			continue
		}
		if def.MaxExclusive > 0 && confidence >= def.MaxExclusive {
			continue
		}
		return def
	}
	return defs[0]
}

func normalizeAnalysisBacktestStrategy(v string) string {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case analysisBacktestStrategyPreferred:
		return analysisBacktestStrategyPreferred
	default:
		return analysisBacktestStrategyAll
	}
}

func analysisBacktestHorizonCorrectRate(results []AnalysisSignalResult, horizonMin int) (float64, int) {
	correct := 0
	wrong := 0
	for _, item := range results {
		for _, horizon := range item.Horizons {
			if horizon.HorizonMin != horizonMin {
				continue
			}
			switch horizon.Result {
			case "correct":
				correct++
			case "wrong":
				wrong++
			}
			break
		}
	}
	verified := correct + wrong
	if verified == 0 {
		return 0, 0
	}
	return math.Round((float64(correct)/float64(verified))*1000) / 10, verified
}

func buildAnalysisBacktestStrategyGroups(results []AnalysisSignalResult) []AnalysisBacktestStrategyGroup {
	defs := analysisConfidenceBucketDefs()
	factors := []string{
		analysisSecondFactorVolumeSpike,
		analysisSecondFactorVolumeNormal,
		analysisSecondFactorVolumeLow,
	}
	grouped := make(map[string][]AnalysisSignalResult, len(defs)*len(factors))
	for _, item := range results {
		key := normalizeAnalysisSecondFactorKey(item.SecondFactorKey)
		if key == analysisSecondFactorAll || key == analysisSecondFactorInsufficient {
			continue
		}
		def := analysisConfidenceBucketForValue(item.Confidence)
		groupKey := key + "|" + def.Label
		grouped[groupKey] = append(grouped[groupKey], item)
	}

	weights := map[int]float64{5: 0.4, 15: 0.3, 30: 0.2, 60: 0.1}
	out := make([]AnalysisBacktestStrategyGroup, 0, len(defs)*len(factors))
	for _, factor := range factors {
		for _, def := range defs {
			items := grouped[factor+"|"+def.Label]
			group := AnalysisBacktestStrategyGroup{
				FactorKey:    factor,
				FactorLabel:  analysisSecondFactorLabel(factor),
				Label:        def.Label,
				MinInclusive: def.MinInclusive,
				MaxExclusive: def.MaxExclusive,
				TotalSignals: len(items),
				Reason:       "样本不足",
			}
			weightedScore := 0.0
			activeWeight := 0.0
			weakHorizon := false
			for _, horizonMin := range analysisBacktestHorizons {
				rate, verified := analysisBacktestHorizonCorrectRate(items, horizonMin)
				if horizonMin == 5 {
					group.FiveMinuteCorrectRate = rate
					group.SampleCount = verified
				}
				if verified <= 0 {
					continue
				}
				if verified >= 5 && rate < 40 {
					weakHorizon = true
				}
				weight := weights[horizonMin]
				weightedScore += rate * weight
				activeWeight += weight
			}
			if activeWeight > 0 {
				group.CompositeScore = math.Round((weightedScore/activeWeight)*10) / 10
			}
			switch {
			case group.SampleCount < 5:
				group.Reason = "5m 已验证样本少于 5 条"
			case group.FiveMinuteCorrectRate < 52:
				group.Reason = "5m 胜率低于 52%"
			case group.CompositeScore < 52:
				group.Reason = "综合多周期得分低于 52%"
			case weakHorizon:
				group.Reason = "存在样本充足但胜率低于 40% 的周期"
			default:
				group.Selected = true
				group.Reason = "入选"
			}
			out = append(out, group)
		}
	}
	return out
}

func filterAnalysisSignalsByStrategyGroups(results []AnalysisSignalResult, groups []AnalysisBacktestStrategyGroup, mode string) ([]AnalysisSignalResult, bool) {
	if normalizeAnalysisBacktestStrategy(mode) != analysisBacktestStrategyPreferred {
		return append([]AnalysisSignalResult(nil), results...), false
	}
	allowed := map[string]bool{}
	for _, group := range groups {
		if !group.Selected {
			continue
		}
		allowed[group.FactorKey+"|"+group.Label] = true
	}
	if len(allowed) == 0 {
		return append([]AnalysisSignalResult(nil), results...), false
	}
	out := make([]AnalysisSignalResult, 0, len(results))
	for _, item := range results {
		key := normalizeAnalysisSecondFactorKey(item.SecondFactorKey)
		def := analysisConfidenceBucketForValue(item.Confidence)
		if allowed[key+"|"+def.Label] {
			out = append(out, item)
		}
	}
	return out, true
}

func buildAnalysisBacktest2FAFactorOptions(results []AnalysisSignalResult, hours int) []AnalysisBacktest2FAFactorSummary {
	order := []string{
		analysisSecondFactorAll,
		analysisSecondFactorVolumeSpike,
		analysisSecondFactorVolumeNormal,
		analysisSecondFactorVolumeLow,
	}
	grouped := map[string][]AnalysisSignalResult{
		analysisSecondFactorAll: results,
	}
	for _, item := range results {
		key := normalizeAnalysisSecondFactorKey(item.SecondFactorKey)
		if key == analysisSecondFactorAll || key == analysisSecondFactorInsufficient {
			continue
		}
		grouped[key] = append(grouped[key], item)
	}
	out := make([]AnalysisBacktest2FAFactorSummary, 0, len(order))
	for _, key := range order {
		items := grouped[key]
		summary := buildAnalysisBacktestSummary(items, hours)
		out = append(out, AnalysisBacktest2FAFactorSummary{
			Key:          key,
			Label:        analysisSecondFactorLabel(key),
			TotalSignals: summary.TotalSignals,
			CorrectCount: summary.CorrectCount,
			WrongCount:   summary.WrongCount,
			PendingCount: summary.PendingCount,
			NoDataCount:  summary.NoDataCount,
			CorrectRate:  summary.CorrectRate,
		})
	}
	return out
}

func defaultAnalysisSecondFactorSelection(factor string) string {
	if strings.TrimSpace(factor) == "" {
		return analysisSecondFactorAll
	}
	return normalizeAnalysisSecondFactorKey(factor)
}

func analysisSecondFactorConfidenceDelta(key string) float64 {
	switch normalizeAnalysisSecondFactorKey(key) {
	case analysisSecondFactorVolumeSpike:
		return 8
	case analysisSecondFactorVolumeLow:
		return -8
	default:
		return 0
	}
}

func buildAnalysisDualFactorCandidate(item AnalysisSignalResult) (AnalysisSignalResult, bool) {
	key := normalizeAnalysisSecondFactorKey(item.SecondFactorKey)
	if key == analysisSecondFactorAll || key == analysisSecondFactorInsufficient {
		return AnalysisSignalResult{}, false
	}
	item.Confidence = math.Round(clamp(item.Confidence+analysisSecondFactorConfidenceDelta(key), 18, 92)*10) / 10
	if label := strings.TrimSpace(item.SecondFactorLabel); label != "" {
		if headline := strings.TrimSpace(item.Headline); headline != "" {
			item.Headline = headline + " · " + label
		} else {
			item.Headline = "双因子 · " + label
		}
		if summary := strings.TrimSpace(item.Summary); summary != "" {
			item.Summary = "第二因子 " + label + "。 " + summary
		}
	}
	return item, true
}

func filterAnalysisSignalsByConfidence(results []AnalysisSignalResult, minConfidence float64) []AnalysisSignalResult {
	if minConfidence <= 0 {
		return append([]AnalysisSignalResult(nil), results...)
	}
	out := make([]AnalysisSignalResult, 0, len(results))
	for _, item := range results {
		if item.Confidence <= minConfidence {
			continue
		}
		out = append(out, item)
	}
	return out
}

func normalizeAnalysisBacktestQualityMode(v string) string {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case analysisBacktestQualityHigh:
		return analysisBacktestQualityHigh
	default:
		return analysisBacktestQualityAll
	}
}

func analysisSignalWindowScoreAgreement(item AnalysisSignalResult) (int, int) {
	direction := normalizeAnalysisDirection(item.Direction)
	if direction == "flat" {
		return 0, 0
	}
	matches := analysisDirectionSummaryScoreRe.FindAllStringSubmatch(item.Summary, -1)
	aligned := 0
	conflict := 0
	for _, match := range matches {
		if len(match) < 2 {
			continue
		}
		score, err := strconv.ParseFloat(match[1], 64)
		if err != nil || math.Abs(score) < analysisDirectionFlatScoreCutoff {
			continue
		}
		scoreDirection := "down"
		if score > 0 {
			scoreDirection = "up"
		}
		if scoreDirection == direction {
			aligned++
		} else {
			conflict++
		}
	}
	return aligned, conflict
}

func analysisSignalHasMultiWindowAgreement(item AnalysisSignalResult) bool {
	aligned, conflict := analysisSignalWindowScoreAgreement(item)
	return aligned >= 3 && conflict == 0
}

func analysisSignalIsHighQuality(item AnalysisSignalResult) bool {
	direction := normalizeAnalysisDirection(item.Direction)
	if direction != "up" && direction != "down" {
		return false
	}
	if item.Confidence < 70 {
		return false
	}
	if direction == "down" && item.Confidence < 80 {
		return false
	}
	if item.Confidence >= 85 && !analysisSignalHasMultiWindowAgreement(item) {
		return false
	}
	return true
}

func filterAnalysisSignalsByQuality(results []AnalysisSignalResult, mode string) []AnalysisSignalResult {
	if normalizeAnalysisBacktestQualityMode(mode) != analysisBacktestQualityHigh {
		return append([]AnalysisSignalResult(nil), results...)
	}
	out := make([]AnalysisSignalResult, 0, len(results))
	for _, item := range results {
		if analysisSignalIsHighQuality(item) {
			out = append(out, item)
		}
	}
	return out
}

func normalizeAnalysisBacktestNoiseStrategy(v string) string {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case analysisBacktestNoisePersistence:
		return analysisBacktestNoisePersistence
	default:
		return analysisBacktestNoiseNone
	}
}

func analysisBacktestPersistenceLabel(score float64) string {
	if score >= 65 {
		return "长周期延续"
	}
	if score >= 50 {
		return "中性观察"
	}
	return "短线噪声"
}

func analysisBacktestDirectionAligned(direction string, value float64) bool {
	if direction == "up" {
		return value > 0
	}
	if direction == "down" {
		return value < 0
	}
	return false
}

func analysisBacktestEMA(values []float64, period int) float64 {
	if len(values) == 0 {
		return 0
	}
	if period <= 1 {
		return values[len(values)-1]
	}
	k := 2 / float64(period+1)
	ema := values[0]
	for _, value := range values[1:] {
		ema = value*k + ema*(1-k)
	}
	return ema
}

func scoreAnalysisSignalPersistence(item AnalysisSignalResult, candles []analysisBacktestCandle) AnalysisSignalResult {
	score := 50.0
	reasons := []string{}
	direction := normalizeAnalysisDirection(item.Direction)
	aligned, conflict := analysisSignalWindowScoreAgreement(item)
	switch {
	case conflict > 0:
		score -= 25
		reasons = append(reasons, "存在反向窗口 -25")
	case aligned >= 3:
		score += 20
		reasons = append(reasons, "三窗口同向 +20")
	case aligned >= 2:
		score += 12
		reasons = append(reasons, "双窗口同向 +12")
	default:
		reasons = append(reasons, "窗口同向不足")
	}

	switch normalizeAnalysisSecondFactorKey(item.SecondFactorKey) {
	case analysisSecondFactorVolumeSpike:
		score += 12
		reasons = append(reasons, "放量 +12")
	case analysisSecondFactorVolumeNormal:
		score += 4
		reasons = append(reasons, "成交量正常 +4")
	case analysisSecondFactorVolumeLow:
		score -= 14
		reasons = append(reasons, "缩量 -14")
	default:
		score -= 8
		reasons = append(reasons, "量能样本不足 -8")
	}

	idx := findCandleIndexForTS(candles, item.SignalTS)
	if idx >= 12 && direction != "flat" {
		current := candles[idx]
		lookback := candles[idx-12 : idx+1]
		closes := make([]float64, 0, len(lookback))
		for _, candle := range lookback {
			closes = append(closes, candle.C)
		}
		firstEMA := analysisBacktestEMA(closes[:7], 5)
		lastEMA := analysisBacktestEMA(closes[6:], 5)
		if analysisBacktestDirectionAligned(direction, lastEMA-firstEMA) {
			score += 10
			reasons = append(reasons, "EMA斜率同向 +10")
		}
		if analysisBacktestDirectionAligned(direction, current.C-candles[idx-3].C) {
			score += 8
			reasons = append(reasons, "近3根净变化同向 +8")
		}

		rangeSize := current.H - current.L
		bodySize := math.Abs(current.C - current.O)
		upperWick := current.H - math.Max(current.O, current.C)
		lowerWick := math.Min(current.O, current.C) - current.L
		if rangeSize > 0 && bodySize/rangeSize < 0.25 && upperWick/rangeSize > 0.28 && lowerWick/rangeSize > 0.28 {
			score -= 10
			reasons = append(reasons, "当前K线长影小实体 -10")
		}

		switches := 0
		prevSign := 0
		for i := idx - 11; i <= idx; i++ {
			move := candles[i].C - candles[i-1].C
			sign := 0
			if move > 0 {
				sign = 1
			} else if move < 0 {
				sign = -1
			}
			if sign != 0 && prevSign != 0 && sign != prevSign {
				switches++
			}
			if sign != 0 {
				prevSign = sign
			}
		}
		if switches >= 7 {
			score -= 8
			reasons = append(reasons, "12根内方向切换过多 -8")
		}
	} else {
		score -= 8
		reasons = append(reasons, "趋势样本不足 -8")
	}

	item.PersistenceScore = math.Round(clamp(score, 0, 100)*10) / 10
	item.PersistenceLabel = analysisBacktestPersistenceLabel(item.PersistenceScore)
	item.PersistenceReason = strings.Join(reasons, " | ")
	return item
}

func applyAnalysisPersistenceCooldown(results []AnalysisSignalResult) []AnalysisSignalResult {
	out := make([]AnalysisSignalResult, 0, len(results))
	const sameDirectionMS = int64(15 * time.Minute / time.Millisecond)
	const oppositeDirectionMS = int64(10 * time.Minute / time.Millisecond)
	for _, item := range results {
		replaced := false
		skip := false
		for i := len(out) - 1; i >= 0; i-- {
			prev := out[i]
			delta := item.SignalTS - prev.SignalTS
			if delta < 0 {
				delta = -delta
			}
			if item.Direction == prev.Direction && delta <= sameDirectionMS {
				if item.PersistenceScore > prev.PersistenceScore {
					out[i] = item
					replaced = true
				} else {
					skip = true
				}
				break
			}
			if item.Direction != prev.Direction && delta <= oppositeDirectionMS {
				if item.PersistenceScore > prev.PersistenceScore {
					out[i] = item
					replaced = true
				} else {
					skip = true
				}
				break
			}
		}
		if !skip && !replaced {
			out = append(out, item)
		}
	}
	return out
}

func filterAnalysisSignalsByNoiseStrategy(results []AnalysisSignalResult, candles []analysisBacktestCandle, strategy string) []AnalysisSignalResult {
	if normalizeAnalysisBacktestNoiseStrategy(strategy) != analysisBacktestNoisePersistence {
		return append([]AnalysisSignalResult(nil), results...)
	}
	scored := make([]AnalysisSignalResult, 0, len(results))
	for _, item := range results {
		item = scoreAnalysisSignalPersistence(item, candles)
		if item.PersistenceScore >= 65 {
			scored = append(scored, item)
		}
	}
	return applyAnalysisPersistenceCooldown(scored)
}

func filterAnalysisSignalsBySecondFactor(results []AnalysisSignalResult, factor string) []AnalysisSignalResult {
	key := normalizeAnalysisSecondFactorKey(factor)
	if key == analysisSecondFactorAll {
		return append([]AnalysisSignalResult(nil), results...)
	}
	out := make([]AnalysisSignalResult, 0, len(results))
	for _, item := range results {
		if normalizeAnalysisSecondFactorKey(item.SecondFactorKey) == key {
			out = append(out, item)
		}
	}
	return out
}

func liquidationBacktestSignalRecord(signal TradeSignal, id int64) (AnalysisSignalRecord, bool) {
	if strings.ToLower(strings.TrimSpace(signal.Action)) != "open" {
		return AnalysisSignalRecord{}, false
	}

	direction := ""
	headline := ""
	switch strings.ToLower(strings.TrimSpace(signal.Side)) {
	case "long":
		direction = "up"
		headline = "开多"
	case "short":
		direction = "down"
		headline = "开空"
	default:
		return AnalysisSignalRecord{}, false
	}

	symbol := defaultSymbol
	if symbol == "" {
		symbol = "ETHUSDT"
	}
	summary := strings.TrimSpace(signal.Reason)
	if summary == "" {
		summary = "1h/4h 状态更新为同向同步。"
	}

	return AnalysisSignalRecord{
		ID:                id,
		SignalTS:          signal.TS,
		Symbol:            symbol,
		SourceGroup:       analysisLiquidationBacktestSourceGroup,
		Direction:         direction,
		Confidence:        signal.Strength,
		SignalPrice:       signal.Price,
		AnalysisGenerated: signal.TS,
		Headline:          headline,
		Summary:           summary,
		VerifyHorizonMin:  analysisSignalVerifyHorizonMin,
	}, true
}

func buildLiquidationBacktestSignalRecords(signals []TradeSignal, sinceTS int64, minConfidence float64) []AnalysisSignalRecord {
	out := make([]AnalysisSignalRecord, 0, len(signals))
	for _, signal := range signals {
		if sinceTS > 0 && signal.TS < sinceTS {
			continue
		}
		record, ok := liquidationBacktestSignalRecord(signal, int64(len(out)+1))
		if !ok {
			continue
		}
		if minConfidence > 0 && record.Confidence <= minConfidence {
			continue
		}
		out = append(out, record)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].SignalTS == out[j].SignalTS {
			return out[i].ID < out[j].ID
		}
		return out[i].SignalTS < out[j].SignalTS
	})
	return out
}
func candleToMap(c analysisBacktestCandle) map[string]any {
	return map[string]any{
		"ts": c.TS,
		"o":  c.O,
		"h":  c.H,
		"l":  c.L,
		"c":  c.C,
	}
}

func (a *App) fetchAnalysisBacktestChartCandles(hours int, interval string, sinceTS, endTS int64) ([]map[string]any, string, string, error) {
	cfg := normalizeAnalysisBacktestInterval(interval)
	durationMS := endTS - sinceTS
	if durationMS <= 0 {
		durationMS = int64(hours) * int64(time.Hour/time.Millisecond)
	}
	startTS := sinceTS
	if cfg.Direct && cfg.Label == "1m" {
		startTS = sinceTS - int64(30*time.Minute/time.Millisecond)
	}
	limit := int(math.Ceil(float64(durationMS)/float64(cfg.BucketMS))) + 24
	if limit < 320 {
		limit = 320
	}
	raw, err := a.FetchKlines(cfg.Source, limit, startTS, endTS)
	if err != nil {
		return nil, "", "", err
	}
	base := parseAnalysisBacktestCandles(raw["rows"])
	chartCandles := base
	if !cfg.Direct {
		chartCandles = aggregateAnalysisBacktestCandles(base, cfg.BucketMS)
	}
	out := make([]map[string]any, 0, len(chartCandles))
	for _, candle := range chartCandles {
		if candle.TS < sinceTS || candle.TS > endTS {
			continue
		}
		out = append(out, candleToMap(candle))
	}
	return out, cfg.Source, cfg.Label, nil
}

func (a *App) buildAnalysisBacktestResults(hours int, minConfidence float64, qualityMode string, noiseStrategy string) ([]AnalysisSignalResult, int64, int64, error) {
	if hours <= 0 {
		hours = 24
	}
	now := time.Now()
	nowTS := now.UnixMilli()
	sinceTS := now.Add(-time.Duration(hours) * time.Hour).UnixMilli()
	records, err := a.listAnalysisDirectionSignals(sinceTS, analysisBacktestSignalLimit(hours), 0)
	if err != nil {
		return nil, 0, 0, err
	}
	const baseStep = int64(5 * time.Minute / time.Millisecond)
	startTS := floorToFiveMinute(sinceTS - int64(30*time.Minute/time.Millisecond))
	endTS := floorToFiveMinute(nowTS) + int64(time.Hour/time.Millisecond) + baseStep
	limit := int(math.Ceil(float64(endTS-startTS)/float64(baseStep))) + 24
	if limit < 320 {
		limit = 320
	}
	raw, err := a.FetchKlines("5m", limit, startTS, endTS)
	if err != nil {
		return nil, 0, 0, err
	}
	candles := parseAnalysisBacktestCandles(raw["rows"])
	sort.Slice(records, func(i, j int) bool {
		if records[i].SignalTS == records[j].SignalTS {
			return records[i].ID < records[j].ID
		}
		return records[i].SignalTS < records[j].SignalTS
	})
	results := make([]AnalysisSignalResult, 0, len(records))
	for i, record := range records {
		if record.Confidence <= minConfidence {
			continue
		}
		results = append(results, buildAnalysisSignalResult(record, candles, nowTS, analysisBacktestHorizonsForSignal(records, i)))
	}
	results = filterAnalysisSignalsByQuality(results, qualityMode)
	results = filterAnalysisSignalsByNoiseStrategy(results, candles, noiseStrategy)
	return results, sinceTS, floorToFiveMinute(nowTS) + baseStep, nil
}

func (a *App) buildLiquidationBacktestResults(hours int, minConfidence float64) ([]AnalysisSignalResult, int64, int64, error) {
	if hours <= 0 {
		hours = 24
	}
	now := time.Now()
	nowTS := now.UnixMilli()
	sinceTS := now.Add(-time.Duration(hours) * time.Hour).UnixMilli()

	const baseStep = int64(5 * time.Minute / time.Millisecond)
	sampleStartTS := floorToFiveMinute(sinceTS - baseStep)
	sampleEndTS := floorToFiveMinute(nowTS)
	tradeSignals := a.TradeSignals(TradeSignalOptions{
		StartTS:  sampleStartTS,
		EndTS:    sampleEndTS,
		Symbol:   defaultSymbol,
		Interval: analysisLiquidationBacktestSampleInterval,
	})
	records := buildLiquidationBacktestSignalRecords(tradeSignals, sinceTS, minConfidence)

	startTS := floorToFiveMinute(sinceTS - int64(30*time.Minute/time.Millisecond))
	endTS := floorToFiveMinute(nowTS) + int64(time.Hour/time.Millisecond) + baseStep
	limit := int(math.Ceil(float64(endTS-startTS)/float64(baseStep))) + 24
	if limit < 320 {
		limit = 320
	}
	raw, err := a.FetchKlines("5m", limit, startTS, endTS)
	if err != nil {
		return nil, 0, 0, err
	}
	candles := parseAnalysisBacktestCandles(raw["rows"])

	results := make([]AnalysisSignalResult, 0, len(records))
	for i, record := range records {
		results = append(results, buildAnalysisSignalResult(record, candles, nowTS, analysisBacktestHorizonsForSignal(records, i)))
	}
	return results, sinceTS, floorToFiveMinute(nowTS) + baseStep, nil
}
func (a *App) AnalysisBacktest(hours int, interval string, minConfidence float64, qualityMode string, noiseStrategy string) (AnalysisBacktestPageResponse, error) {
	selectedQuality := normalizeAnalysisBacktestQualityMode(qualityMode)
	selectedNoiseStrategy := normalizeAnalysisBacktestNoiseStrategy(noiseStrategy)
	results, sinceTS, chartEndTS, err := a.buildAnalysisBacktestResults(hours, minConfidence, selectedQuality, selectedNoiseStrategy)
	if err != nil {
		return AnalysisBacktestPageResponse{}, err
	}
	candles, source, chartInterval, err := a.fetchAnalysisBacktestChartCandles(hours, interval, sinceTS, chartEndTS)
	if err != nil {
		return AnalysisBacktestPageResponse{}, err
	}
	return AnalysisBacktestPageResponse{
		Candles:           candles,
		Signals:           results,
		Summary:           buildAnalysisBacktestSummary(results, hours),
		HorizonStats:      buildAnalysisBacktestHorizonStats(results),
		ConfidenceBuckets: buildAnalysisBacktestConfidenceBuckets(results),
		QualityMode:       selectedQuality,
		NoiseStrategy:     selectedNoiseStrategy,
		ChartSource:       source,
		ChartInterval:     chartInterval,
	}, nil
}

func (a *App) AnalysisBacktestLiquidation(hours int, interval string, minConfidence float64) (AnalysisBacktestPageResponse, error) {
	results, sinceTS, chartEndTS, err := a.buildLiquidationBacktestResults(hours, minConfidence)
	if err != nil {
		return AnalysisBacktestPageResponse{}, err
	}
	candles, source, chartInterval, err := a.fetchAnalysisBacktestChartCandles(hours, interval, sinceTS, chartEndTS)
	if err != nil {
		return AnalysisBacktestPageResponse{}, err
	}
	return AnalysisBacktestPageResponse{
		Candles:           candles,
		Signals:           results,
		Summary:           buildAnalysisBacktestSummary(results, hours),
		HorizonStats:      buildAnalysisBacktestHorizonStats(results),
		ConfidenceBuckets: buildAnalysisBacktestConfidenceBuckets(results),
		ChartSource:       source,
		ChartInterval:     chartInterval,
	}, nil
}
func (a *App) AnalysisBacktest2FA(hours int, interval string, factor string, minConfidence float64, strategy string) (AnalysisBacktest2FAResponse, error) {
	baseResults, sinceTS, chartEndTS, err := a.buildAnalysisBacktestResults(hours, 0, analysisBacktestQualityAll, analysisBacktestNoiseNone)
	if err != nil {
		return AnalysisBacktest2FAResponse{}, err
	}
	candidates := make([]AnalysisSignalResult, 0, len(baseResults))
	for _, item := range baseResults {
		candidate, ok := buildAnalysisDualFactorCandidate(item)
		if !ok {
			continue
		}
		candidates = append(candidates, candidate)
	}
	strategyMode := normalizeAnalysisBacktestStrategy(strategy)
	strategyGroups := buildAnalysisBacktestStrategyGroups(candidates)
	strategyFiltered, strategyActive := filterAnalysisSignalsByStrategyGroups(candidates, strategyGroups, strategyMode)
	selectedFactor := defaultAnalysisSecondFactorSelection(factor)
	factorFilteredAll := filterAnalysisSignalsBySecondFactor(strategyFiltered, selectedFactor)
	confFiltered := filterAnalysisSignalsByConfidence(strategyFiltered, minConfidence)
	filtered := filterAnalysisSignalsBySecondFactor(confFiltered, selectedFactor)
	candles, source, chartInterval, err := a.fetchAnalysisBacktestChartCandles(hours, interval, sinceTS, chartEndTS)
	if err != nil {
		return AnalysisBacktest2FAResponse{}, err
	}
	return AnalysisBacktest2FAResponse{
		Candles:                 candles,
		Signals:                 filtered,
		Summary:                 buildAnalysisBacktestSummary(filtered, hours),
		HorizonStats:            buildAnalysisBacktestHorizonStats(filtered),
		ConfidenceBuckets:       buildAnalysisBacktestConfidenceBuckets(factorFilteredAll),
		ConfidenceFactorBuckets: buildAnalysisBacktestConfidenceFactorBuckets(candidates),
		StrategyMode:            strategyMode,
		StrategyActive:          strategyActive,
		StrategyGroups:          strategyGroups,
		SelectedFactor:          selectedFactor,
		FactorOptions:           buildAnalysisBacktest2FAFactorOptions(confFiltered, hours),
		ChartSource:             source,
		ChartInterval:           chartInterval,
	}, nil
}

func (a *App) AnalysisBacktestHistory(limit, page int) (AnalysisBacktestHistoryResponse, error) {
	if limit <= 0 {
		limit = 50
	}
	if page <= 0 {
		page = 1
	}
	records, err := a.listAnalysisDirectionSignalsPage(limit, page)
	if err != nil {
		return AnalysisBacktestHistoryResponse{}, err
	}
	nowTS := time.Now().UnixMilli()
	startTS := int64(0)
	endTS := int64(0)
	for i, record := range records {
		if i == 0 || record.SignalTS < startTS || startTS == 0 {
			startTS = record.SignalTS
		}
		verifyDueTS := record.SignalTS + int64(normalizedAnalysisVerifyHorizonMin(record.VerifyHorizonMin))*int64(time.Minute/time.Millisecond)
		if verifyDueTS > endTS {
			endTS = verifyDueTS
		}
	}
	candles := []analysisBacktestCandle{}
	if startTS > 0 {
		raw, err := a.FetchKlines("5m", 1000, floorToFiveMinute(startTS-int64(5*time.Minute/time.Millisecond)), endTS+int64(5*time.Minute/time.Millisecond))
		if err == nil {
			candles = parseAnalysisBacktestCandles(raw["rows"])
		}
	}
	results := make([]AnalysisSignalResult, 0, len(records))
	ordered := append([]AnalysisSignalRecord(nil), records...)
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].SignalTS == ordered[j].SignalTS {
			return ordered[i].ID < ordered[j].ID
		}
		return ordered[i].SignalTS < ordered[j].SignalTS
	})
	horizonsByID := map[int64][]int{}
	for i := range ordered {
		horizonsByID[ordered[i].ID] = analysisBacktestHorizonsForSignal(ordered, i)
	}
	for _, record := range records {
		results = append(results, buildAnalysisSignalResult(record, candles, nowTS, horizonsByID[record.ID]))
	}
	return AnalysisBacktestHistoryResponse{
		Page:    page,
		Limit:   limit,
		Signals: results,
	}, nil
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func (a *App) debugAnalysisBacktestSummary(hours int) string {
	resp, err := a.AnalysisBacktest(hours, "5m", 0, analysisBacktestQualityAll, analysisBacktestNoiseNone)
	if err != nil {
		return err.Error()
	}
	return fmt.Sprintf("signals=%d correct=%d wrong=%d pending=%d no_data=%d",
		resp.Summary.TotalSignals,
		resp.Summary.CorrectCount,
		resp.Summary.WrongCount,
		resp.Summary.PendingCount,
		resp.Summary.NoDataCount,
	)
}
