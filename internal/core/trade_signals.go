package liqmap

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

type tradeSignalSyncPoint struct {
	TS            int64
	Price         float64
	ShortActive   bool
	ShortDir      string
	ShortStrength float64
	ContActive    bool
	ContDir       string
	ContStrength  float64
}

func normalizeTradeSignalDirection(active bool, dir string) string {
	if !active {
		return ""
	}
	switch strings.ToLower(strings.TrimSpace(dir)) {
	case "up":
		return "up"
	case "down":
		return "down"
	default:
		return ""
	}
}

func tradeSignalLabel(side, action string) string {
	switch strings.ToLower(strings.TrimSpace(side)) + ":" + strings.ToLower(strings.TrimSpace(action)) {
	case "long:open":
		return "多单开仓"
	case "long:add":
		return "多单增仓"
	case "long:reduce":
		return "多单减仓"
	case "long:close":
		return "多单平仓"
	case "short:open":
		return "空单开仓"
	case "short:add":
		return "空单增仓"
	case "short:reduce":
		return "空单减仓"
	case "short:close":
		return "空单平仓"
	default:
		return "交易信号"
	}
}

func appendTradeSignal(out []TradeSignal, point tradeSignalSyncPoint, side, action, reason string, strength float64) []TradeSignal {
	if strength <= 0 {
		strength = math.Max(point.ShortStrength, point.ContStrength)
	}
	return append(out, TradeSignal{
		TS:       point.TS,
		Side:     side,
		Action:   action,
		Price:    point.Price,
		Label:    tradeSignalLabel(side, action),
		Reason:   reason,
		Strength: math.Round(clamp(strength, 0, 100)*10) / 10,
	})
}

func buildTradeSignalsFromSyncPoints(points []tradeSignalSyncPoint) []TradeSignal {
	if len(points) == 0 {
		return nil
	}
	sort.Slice(points, func(i, j int) bool { return points[i].TS < points[j].TS })

	position := ""
	prevShortDir := ""
	prevContDir := ""
	out := make([]TradeSignal, 0, len(points))
	for _, point := range points {
		shortDir := normalizeTradeSignalDirection(point.ShortActive, point.ShortDir)
		contDir := normalizeTradeSignalDirection(point.ContActive, point.ContDir)

		if position == "long" && shortDir == "down" && prevShortDir != "down" {
			out = appendTradeSignal(out, point, "long", "close", "1h/4h 同时转为空同步，多单平仓。", point.ShortStrength)
			position = ""
		}
		if position == "short" && shortDir == "up" && prevShortDir != "up" {
			out = appendTradeSignal(out, point, "short", "close", "1h/4h 同时转为多同步，空单平仓。", point.ShortStrength)
			position = ""
		}

		if shortDir == "up" && prevShortDir != "up" {
			out = appendTradeSignal(out, point, "long", "open", "1h/4h 同时变为多同步，多单开仓。", point.ShortStrength)
			position = "long"
		}
		if shortDir == "down" && prevShortDir != "down" {
			out = appendTradeSignal(out, point, "short", "open", "1h/4h 同时变为空同步，空单开仓。", point.ShortStrength)
			position = "short"
		}

		if position == "long" && contDir == "up" && prevContDir != "up" {
			out = appendTradeSignal(out, point, "long", "add", "12h/24h 同时确认多头趋势，多单增仓。", point.ContStrength)
		}
		if position == "short" && contDir == "down" && prevContDir != "down" {
			out = appendTradeSignal(out, point, "short", "add", "12h/24h 同时确认空头趋势，空单增仓。", point.ContStrength)
		}

		if position == "long" && prevShortDir == "up" && shortDir == "" {
			out = appendTradeSignal(out, point, "long", "reduce", "1h/4h 多同步失效但未反向同步，多单减仓。", math.Max(point.ShortStrength, 35))
		}
		if position == "short" && prevShortDir == "down" && shortDir == "" {
			out = appendTradeSignal(out, point, "short", "reduce", "1h/4h 空同步失效但未反向同步，空单减仓。", math.Max(point.ShortStrength, 35))
		}

		prevShortDir = shortDir
		prevContDir = contDir
	}
	return out
}

func tradeSignalStep(interval string, startTS, endTS int64) time.Duration {
	switch strings.ToLower(strings.TrimSpace(interval)) {
	case "1m":
		return time.Minute
	case "2m":
		return 2 * time.Minute
	case "5m":
		return 5 * time.Minute
	case "10m":
		return 10 * time.Minute
	case "15m":
		return 15 * time.Minute
	case "30m":
		return 30 * time.Minute
	case "1h":
		return time.Hour
	case "4h":
		return 4 * time.Hour
	case "8h":
		return 8 * time.Hour
	case "12h":
		return 12 * time.Hour
	case "1d":
		return 24 * time.Hour
	case "3d":
		return 3 * 24 * time.Hour
	case "7d", "1w":
		return 7 * 24 * time.Hour
	}
	span := time.Duration(endTS-startTS) * time.Millisecond
	switch {
	case span <= 48*time.Hour:
		return 5 * time.Minute
	case span <= 14*24*time.Hour:
		return 30 * time.Minute
	case span <= 90*24*time.Hour:
		return 4 * time.Hour
	default:
		return 24 * time.Hour
	}
}

func tradeSignalSampleTimes(startTS, endTS int64, interval string) []int64 {
	if endTS <= 0 {
		endTS = time.Now().UnixMilli()
	}
	if startTS <= 0 || startTS >= endTS {
		startTS = endTS - int64((24*time.Hour)/time.Millisecond)
	}
	step := tradeSignalStep(interval, startTS, endTS)
	if step <= 0 {
		step = 5 * time.Minute
	}
	maxSamples := 1000
	span := time.Duration(endTS-startTS) * time.Millisecond
	if span > 0 && int(span/step) > maxSamples {
		step = time.Duration(math.Ceil(float64(span) / float64(maxSamples)))
	}
	stepMS := int64(step / time.Millisecond)
	if stepMS <= 0 {
		stepMS = int64((5 * time.Minute) / time.Millisecond)
	}
	times := make([]int64, 0, int((endTS-startTS)/stepMS)+2)
	for ts := startTS; ts <= endTS; ts += stepMS {
		times = append(times, ts)
	}
	if len(times) == 0 || times[len(times)-1] != endTS {
		times = append(times, endTS)
	}
	return times
}

func bucketStrength(first, second LiquidationPeriodBucket) float64 {
	if first.TotalUSD <= 0 || second.TotalUSD <= 0 {
		return 0
	}
	return clamp(math.Min(first.BalanceRatio, second.BalanceRatio)*100, 0, 100)
}

func syncPointFromBuckets(ts int64, price float64, buckets []LiquidationPeriodBucket) tradeSignalSyncPoint {
	byHours := make(map[int]LiquidationPeriodBucket, len(buckets))
	for _, bucket := range buckets {
		byHours[bucket.Hours] = bucket
	}
	shortSignal := buildLiquidationSyncSignal(byHours[1], byHours[4], "", "")
	contSignal := buildLiquidationSyncSignal(byHours[12], byHours[24], "", "")
	return tradeSignalSyncPoint{
		TS:            ts,
		Price:         price,
		ShortActive:   shortSignal.Active,
		ShortDir:      shortSignal.Direction,
		ShortStrength: bucketStrength(byHours[1], byHours[4]),
		ContActive:    contSignal.Active,
		ContDir:       contSignal.Direction,
		ContStrength:  bucketStrength(byHours[12], byHours[24]),
	}
}

func (a *App) liquidationPeriodBucketsAt(symbol string, atTS int64) ([]LiquidationPeriodBucket, float64) {
	hours := []int{1, 4, 12, 24}
	start24 := atTS - int64((24*time.Hour)/time.Millisecond)
	query := `SELECT side, notional_usd, price, event_ts
		FROM liquidation_events
		WHERE event_ts>=? AND event_ts<=?`
	args := []any{start24, atTS}
	if symbol != "ALL" {
		query += ` AND symbol=?`
		args = append(args, symbol)
	}
	query += ` ORDER BY event_ts ASC`
	rows, err := a.db.Query(query, args...)
	if err != nil {
		return a.emptyLiquidationPeriodBuckets(hours), 0
	}
	defer rows.Close()

	var totals [4]float64
	var longs [4]float64
	var shorts [4]float64
	price := 0.0
	for rows.Next() {
		var side string
		var notional, eventPrice float64
		var eventTS int64
		if err := rows.Scan(&side, &notional, &eventPrice, &eventTS); err != nil {
			continue
		}
		if eventPrice > 0 {
			price = eventPrice
		}
		for i, h := range hours {
			if eventTS < atTS-int64((time.Duration(h)*time.Hour)/time.Millisecond) {
				continue
			}
			totals[i] += notional
			if strings.ToLower(strings.TrimSpace(side)) == "short" {
				shorts[i] += notional
			} else {
				longs[i] += notional
			}
		}
	}

	buckets := make([]LiquidationPeriodBucket, 0, len(hours))
	for i, h := range hours {
		dominant := "long"
		push := "down"
		pushLabel := "偏向价格向下推动"
		if shorts[i] > longs[i] {
			dominant = "short"
			push = "up"
			pushLabel = "偏向价格向上推动"
		} else if math.Abs(longs[i]-shorts[i]) < 1e-9 {
			push = "neutral"
			pushLabel = "当前较中性"
		}
		balanceRatio := 0.0
		if totals[i] > 0 {
			balanceRatio = math.Abs(longs[i]-shorts[i]) / totals[i]
		}
		buckets = append(buckets, LiquidationPeriodBucket{
			Label:          fmt.Sprintf("%dH", h),
			Hours:          h,
			TotalUSD:       totals[i],
			LongUSD:        longs[i],
			ShortUSD:       shorts[i],
			DominantSide:   dominant,
			PricePush:      push,
			PricePushLabel: pushLabel,
			BalanceRatio:   balanceRatio,
		})
	}
	return buckets, price
}

func (a *App) TradeSignals(opts TradeSignalOptions) []TradeSignal {
	symbol := normalizeLiquidationSymbolFilter(opts.Symbol)
	times := tradeSignalSampleTimes(opts.StartTS, opts.EndTS, opts.Interval)
	points := make([]tradeSignalSyncPoint, 0, len(times))
	lastPrice := 0.0
	for _, ts := range times {
		buckets, price := a.liquidationPeriodBucketsAt(symbol, ts)
		if price > 0 {
			lastPrice = price
		}
		points = append(points, syncPointFromBuckets(ts, lastPrice, buckets))
	}
	signals := buildTradeSignalsFromSyncPoints(points)
	if opts.StartTS > 0 || opts.EndTS > 0 {
		filtered := signals[:0]
		for _, signal := range signals {
			if opts.StartTS > 0 && signal.TS < opts.StartTS {
				continue
			}
			if opts.EndTS > 0 && signal.TS > opts.EndTS {
				continue
			}
			filtered = append(filtered, signal)
		}
		signals = filtered
	}
	return signals
}
