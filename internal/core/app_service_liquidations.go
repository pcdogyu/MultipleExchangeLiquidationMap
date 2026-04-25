package liqmap

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

func normalizeLiquidationSymbolFilter(symbol string) string {
	symbol = strings.ToUpper(strings.TrimSpace(symbol))
	if idx := strings.Index(symbol, "_"); idx > 0 {
		symbol = symbol[:idx]
	}
	switch symbol {
	case "", "ETH", defaultSymbol:
		return defaultSymbol
	case "ALL", "*":
		return "ALL"
	default:
		return strings.ReplaceAll(symbol, "-", "")
	}
}

func (a *App) mergeLiquidationSymbols(symbols []string) {
	a.liqSymbolsMu.Lock()
	defer a.liqSymbolsMu.Unlock()
	if a.liqSymbols == nil {
		a.liqSymbols = map[string]struct{}{}
	}
	for _, symbol := range symbols {
		symbol = normalizeLiquidationSymbolFilter(symbol)
		if symbol == "" || symbol == "ALL" {
			continue
		}
		a.liqSymbols[symbol] = struct{}{}
	}
}

func (a *App) snapshotLiquidationSymbols() []string {
	a.liqSymbolsMu.RLock()
	defer a.liqSymbolsMu.RUnlock()
	if len(a.liqSymbols) == 0 {
		return nil
	}
	out := make([]string, 0, len(a.liqSymbols))
	for symbol := range a.liqSymbols {
		if symbol == "" || symbol == "ALL" {
			continue
		}
		out = append(out, symbol)
	}
	sort.Strings(out)
	return out
}

func (a *App) QueryLiquidations(opts LiquidationListOptions) []EventRow {
	opts.Symbol = normalizeLiquidationSymbolFilter(opts.Symbol)
	return a.loadLiquidations(opts)
}

func (a *App) RetryLiquidationExchange(exchange string) error {
	exchange = strings.ToLower(strings.TrimSpace(exchange))
	switch exchange {
	case "binance", "bybit", "okx":
	default:
		return fmt.Errorf("unsupported exchange: %s", exchange)
	}

	a.apiGuardMu.Lock()
	if a.apiGuards == nil {
		a.apiGuards = map[string]*ExchangeAPIGuard{}
	}
	guard := a.apiGuards[exchange]
	if guard == nil {
		guard = &ExchangeAPIGuard{}
		a.apiGuards[exchange] = guard
	}
	guard.ConsecutiveHTTPError = 0
	guard.LastHTTPErrorKey = ""
	guard.LastHTTPErrorText = ""
	guard.PausedUntil = time.Time{}
	guard.PauseReason = ""
	retryCh := a.retrySignals[exchange]
	a.apiGuardMu.Unlock()

	a.liqWSMu.Lock()
	if state := a.liqWS[exchange]; state != nil {
		state.LastError = ""
	}
	a.liqWSMu.Unlock()

	if retryCh != nil {
		select {
		case retryCh <- struct{}{}:
		default:
		}
	}
	return nil
}

func (a *App) ListLiquidations(limit, offset int, startTS, endTS int64) []EventRow {
	return a.QueryLiquidations(LiquidationListOptions{
		Limit:   limit,
		Offset:  offset,
		StartTS: startTS,
		EndTS:   endTS,
		Symbol:  defaultSymbol,
	})
}

func (a *App) LiquidationSymbols(limit int) []string {
	if limit <= 0 {
		limit = 200
	}
	rows, err := a.db.Query(`SELECT symbol, MAX(event_ts) AS latest_ts
		FROM liquidation_events
		GROUP BY symbol
		ORDER BY latest_ts DESC, symbol ASC
		LIMIT ?`, limit)
	if err != nil {
		return []string{defaultSymbol}
	}
	defer rows.Close()

	out := make([]string, 0, limit)
	for rows.Next() {
		var symbol string
		var latestTS int64
		if err := rows.Scan(&symbol, &latestTS); err != nil {
			continue
		}
		symbol = normalizeLiquidationSymbolFilter(symbol)
		if symbol == "" || symbol == "ALL" {
			continue
		}
		out = append(out, symbol)
	}
	if len(out) == 0 {
		out = []string{defaultSymbol}
	}
	out = append(out, a.snapshotLiquidationSymbols()...)
	seen := map[string]struct{}{}
	deduped := make([]string, 0, len(out))
	for _, symbol := range out {
		if _, ok := seen[symbol]; ok {
			continue
		}
		seen[symbol] = struct{}{}
		deduped = append(deduped, symbol)
	}
	return deduped
}

func (a *App) LiquidationWSStatuses() []LiquidationWSStatus {
	exchanges := []string{"binance", "bybit", "okx"}
	now := time.Now()

	a.liqWSMu.RLock()
	defer a.liqWSMu.RUnlock()

	out := make([]LiquidationWSStatus, 0, len(exchanges))
	for _, exchange := range exchanges {
		state := a.liqWS[exchange]
		item := LiquidationWSStatus{Exchange: exchange}
		if state != nil {
			item.ActiveConns = state.ActiveConns
			item.Connected = state.ActiveConns > 0
			item.SubscribedTopics = state.SubscribedTopics
			item.Mode = state.Mode
			item.LastEventTS = state.LastEventTS
			item.LastConnectTS = state.LastConnectTS
			item.LastDisconnectTS = state.LastDisconnectTS
			item.LastError = state.LastError
			if item.LastEventTS > 0 && now.UnixMilli()-item.LastEventTS > int64((2*time.Minute)/time.Millisecond) && item.ActiveConns == 0 {
				item.Connected = false
			}
		}
		if until, reason, ok := a.exchangePauseState(exchange); ok {
			item.PausedUntil = until.UnixMilli()
			item.PauseReason = reason
		}
		out = append(out, item)
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].Exchange < out[j].Exchange
	})
	return out
}

func (a *App) LiquidationPeriodSummary(opts LiquidationListOptions) LiquidationPeriodSummary {
	symbol := normalizeLiquidationSymbolFilter(opts.Symbol)
	now := time.Now()
	hours := []int{1, 4, 12, 24}
	thresholds := make([]int64, 0, len(hours))
	for _, h := range hours {
		thresholds = append(thresholds, now.Add(-time.Duration(h)*time.Hour).UnixMilli())
	}

	query := `SELECT
		COALESCE(SUM(CASE WHEN event_ts >= ? THEN notional_usd ELSE 0 END), 0),
		COALESCE(SUM(CASE WHEN event_ts >= ? AND LOWER(side)='long' THEN notional_usd ELSE 0 END), 0),
		COALESCE(SUM(CASE WHEN event_ts >= ? AND LOWER(side)='short' THEN notional_usd ELSE 0 END), 0),
		COALESCE(SUM(CASE WHEN event_ts >= ? THEN notional_usd ELSE 0 END), 0),
		COALESCE(SUM(CASE WHEN event_ts >= ? AND LOWER(side)='long' THEN notional_usd ELSE 0 END), 0),
		COALESCE(SUM(CASE WHEN event_ts >= ? AND LOWER(side)='short' THEN notional_usd ELSE 0 END), 0),
		COALESCE(SUM(CASE WHEN event_ts >= ? THEN notional_usd ELSE 0 END), 0),
		COALESCE(SUM(CASE WHEN event_ts >= ? AND LOWER(side)='long' THEN notional_usd ELSE 0 END), 0),
		COALESCE(SUM(CASE WHEN event_ts >= ? AND LOWER(side)='short' THEN notional_usd ELSE 0 END), 0),
		COALESCE(SUM(CASE WHEN event_ts >= ? THEN notional_usd ELSE 0 END), 0),
		COALESCE(SUM(CASE WHEN event_ts >= ? AND LOWER(side)='long' THEN notional_usd ELSE 0 END), 0),
		COALESCE(SUM(CASE WHEN event_ts >= ? AND LOWER(side)='short' THEN notional_usd ELSE 0 END), 0)
		FROM liquidation_events
		WHERE event_ts >= ?`
	args := []any{
		thresholds[0], thresholds[0], thresholds[0],
		thresholds[1], thresholds[1], thresholds[1],
		thresholds[2], thresholds[2], thresholds[2],
		thresholds[3], thresholds[3], thresholds[3],
		thresholds[3],
	}
	if symbol != "ALL" {
		query += ` AND symbol=?`
		args = append(args, symbol)
	}
	if opts.MinValue > 0 {
		switch strings.ToLower(strings.TrimSpace(opts.FilterField)) {
		case "qty":
			query += ` AND qty >= ?`
			args = append(args, opts.MinValue)
		case "amount", "notional", "notional_usd":
			query += ` AND notional_usd >= ?`
			args = append(args, opts.MinValue)
		}
	}

	var totals [4]float64
	var longs [4]float64
	var shorts [4]float64
	err := a.db.QueryRow(query, args...).Scan(
		&totals[0], &longs[0], &shorts[0],
		&totals[1], &longs[1], &shorts[1],
		&totals[2], &longs[2], &shorts[2],
		&totals[3], &longs[3], &shorts[3],
	)
	if err != nil {
		return LiquidationPeriodSummary{
			Buckets: a.emptyLiquidationPeriodBuckets(hours),
			Pattern: a.buildLiquidationPattern(a.emptyLiquidationPeriodBuckets(hours)),
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
	return LiquidationPeriodSummary{
		Buckets: buckets,
		Pattern: a.buildLiquidationPattern(buckets),
	}
}

func (a *App) emptyLiquidationPeriodBuckets(hours []int) []LiquidationPeriodBucket {
	out := make([]LiquidationPeriodBucket, 0, len(hours))
	for _, h := range hours {
		out = append(out, LiquidationPeriodBucket{
			Label:          fmt.Sprintf("%dH", h),
			Hours:          h,
			DominantSide:   "long",
			PricePush:      "neutral",
			PricePushLabel: "当前较中性",
		})
	}
	return out
}

func (a *App) buildLiquidationPattern(buckets []LiquidationPeriodBucket) LiquidationPeriodPattern {
	if len(buckets) == 0 {
		return LiquidationPeriodPattern{
			Code:         "----",
			Label:        "四周期形态",
			TrendBias:    "neutral",
			Summary:      "暂无可用清算数据。",
			PatternCount: 16,
		}
	}
	weights := []float64{8, 4, 2, 1}
	tokens := make([]string, 0, len(buckets))
	longCount := 0
	shortCount := 0
	score := 0.0
	for i, bucket := range buckets {
		weight := 1.0
		if i < len(weights) {
			weight = weights[i]
		}
		if bucket.DominantSide == "short" {
			tokens = append(tokens, "空")
			shortCount++
			score += weight
		} else {
			tokens = append(tokens, "多")
			longCount++
			score -= weight
		}
	}
	code := strings.Join(tokens, "")
	trendBias := "neutral"
	summary := "四个周期分歧较大，走势更偏向震荡。"
	switch {
	case score >= 10:
		trendBias = "up_strong"
		summary = "四个周期以空单清算主导为主，短中长周期都更容易演化成上冲挤空。"
	case score >= 4:
		trendBias = "up"
		summary = "短中周期偏向空单清算主导，价格更容易继续向上试探。"
	case score > 0:
		trendBias = "up_light"
		summary = "结构轻微偏向空单清算主导，价格有上推倾向，但延续性一般。"
	case score <= -10:
		trendBias = "down_strong"
		summary = "四个周期以多单清算主导为主，短中长周期都更容易演化成向下踩踏。"
	case score <= -4:
		trendBias = "down"
		summary = "短中周期偏向多单清算主导，价格更容易继续向下试探。"
	case score < 0:
		trendBias = "down_light"
		summary = "结构轻微偏向多单清算主导，价格有下压倾向，但延续性一般。"
	}
	if code == "多多空空" {
		summary = "短线两档由多单清算主导，但中长周期仍有空单挤压残留，常见于反弹后的再下压结构。"
	} else if code == "空空多多" {
		summary = "短线两档由空单清算主导，但中长周期仍保留多单踩踏痕迹，常见于下跌后的修复上推结构。"
	}
	label := fmt.Sprintf("当前形态 %s（1H / 4H / 12H / 24H，共 16 种）", code)
	if longCount == 4 {
		label = "当前形态 多多多多（1H / 4H / 12H / 24H，共 16 种）"
	}
	if shortCount == 4 {
		label = "当前形态 空空空空（1H / 4H / 12H / 24H，共 16 种）"
	}
	return LiquidationPeriodPattern{
		Code:         code,
		Label:        label,
		TrendBias:    trendBias,
		Score:        score,
		Summary:      summary,
		PatternCount: 16,
	}
}
