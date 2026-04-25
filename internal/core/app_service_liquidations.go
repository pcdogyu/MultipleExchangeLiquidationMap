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
		buckets := a.emptyLiquidationPeriodBuckets(hours)
		return LiquidationPeriodSummary{
			Buckets: buckets,
			Pattern: a.buildLiquidationPattern(buckets),
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
	score := 0.0
	hasNeutral := false

	for i, bucket := range buckets {
		weight := 1.0
		if i < len(weights) {
			weight = weights[i]
		}
		switch strings.ToLower(strings.TrimSpace(bucket.PricePush)) {
		case "up":
			tokens = append(tokens, "多")
			score += weight
		case "down":
			tokens = append(tokens, "空")
			score -= weight
		default:
			hasNeutral = true
			if strings.ToLower(strings.TrimSpace(bucket.DominantSide)) == "short" {
				tokens = append(tokens, "多")
				score += weight
			} else {
				tokens = append(tokens, "空")
				score -= weight
			}
		}
	}

	code := strings.Join(tokens, "")
	label := fmt.Sprintf("当前形态 %s（1H / 4H / 12H / 24H，共 16 种）", code)
	if hasNeutral {
		label = fmt.Sprintf("当前形态 %s（含均衡周期，仍按当前兜底方向展示）", code)
	}

	return LiquidationPeriodPattern{
		Code:         code,
		Label:        label,
		TrendBias:    liquidationPatternTrendBias(score),
		Score:        score,
		Summary:      liquidationPatternSummary(code, hasNeutral),
		PatternCount: 16,
	}
}

func liquidationPatternTrendBias(score float64) string {
	switch {
	case score >= 10:
		return "up_strong"
	case score >= 4:
		return "up"
	case score > 0:
		return "up_light"
	case score <= -10:
		return "down_strong"
	case score <= -4:
		return "down"
	case score < 0:
		return "down_light"
	default:
		return "neutral"
	}
}

func liquidationPatternSummary(code string, hasNeutral bool) string {
	summaries := map[string]string{
		"多多多多": "1H、4H、12H、24H 四个周期全部偏向上推，说明短线到日内主导力量都在向上挤压。它通常意味着空头清算持续释放，价格更容易延续上冲，是最完整的多头推进结构。",
		"多多多空": "短线到 12 小时都偏向上推，只有 24 小时保留少量下压残留。它通常表示上推动能已经接管盘面，但更长周期仍有旧压力带，常见于上冲延续中的中途分歧。",
		"多多空多": "1H、4H 和 24H 偏向上推，只有 12H 存在一段中期回压。它通常表示主趋势仍偏多，过程中夹杂一次中周期回撤，但多数时间框架仍支持价格继续向上试探。",
		"多多空空": "1H、4H 偏向上推，12H、24H 偏向下压。它通常表示短线反弹和挤空更强，但中长周期仍保留下压结构，价格容易先上冲，随后进入更剧烈的拉扯。",
		"多空多多": "1H、12H、24H 偏向上推，只有 4H 出现局部回压。它通常表示上推仍是主结构，4 小时级别只是中途震荡或回踩，若回压不能放大，价格更容易恢复上行。",
		"多空多空": "1H、12H 偏向上推，4H、24H 偏向下压。它通常表示短线与中长线力量交错，既有上推动能，也有旧下压带压制，走势更容易先冲后压、反复震荡。",
		"多空空多": "1H、24H 偏向上推，4H、12H 偏向下压。它通常表示最短和最长周期想把价格往上抬，但中间层级仍有明显压制，结构偏修复反弹，不属于顺畅单边。",
		"多空空空": "只有 1H 偏向上推，其余三个周期都偏向下压。它通常表示当前反弹更多是超短线修复，中期到日内主导方向仍未翻转，价格上冲后再次承压的概率更高。",
		"空多多多": "只有 1H 偏向下压，4H、12H、24H 都偏向上推。它通常表示短线出现一次急踩或洗盘，但中期结构并未转弱，若下压不能扩散，价格往往会回到上推轨道。",
		"空多多空": "1H、24H 偏向下压，4H、12H 偏向上推。它通常表示当前有短线回踩和长周期旧压力，但中间主交易区仍在向上修复，走势常见先压后拉的反复切换。",
		"空多空多": "1H、12H 偏向下压，4H、24H 偏向上推。它通常表示短线回撤与中长线修复并存，市场方向不够单一，价格更可能在关键位附近来回拉扯而不是直接走单边。",
		"空多空空": "1H、12H、24H 偏向下压，只有 4H 偏向上推。它通常表示大方向仍然偏空，4 小时级别的反抽更像局部修复；若反抽不能扩大，价格更容易继续往下试探。",
		"空空多多": "1H、4H 偏向下压，12H、24H 偏向上推。它通常表示短线踩踏更强，但中长周期仍有修复和上抬基础，价格容易先下探，再面临较强的回拉或修复结构。",
		"空空多空": "1H、4H、24H 偏向下压，只有 12H 仍保留一段中期上推残留。它通常表示短线和大框架都在向下施压，中期虽然有过修复，但主导权仍然偏空，属于强下压结构。",
		"空空空多": "1H、4H、12H 偏向下压，只有 24H 偏向上推。它通常表示最近几个主要交易周期都在朝下扩散，只有更长周期还留有旧支撑，价格更容易先踩踏后再观察是否修复。",
		"空空空空": "1H、4H、12H、24H 四个周期全部偏向下压，说明短线到日内主导力量都在向下踩踏。它通常意味着多单清算持续释放，价格更容易延续下探，是最完整的空头扩散结构。",
	}

	summary := summaries[code]
	if summary == "" {
		summary = "四个周期的方向信号分布较杂，当前结构更偏向震荡切换，价格大概率围绕关键位反复拉扯。"
	}
	if hasNeutral {
		summary += " 其中至少有一个周期接近均衡，本次形态码按当前兜底方向展示，解读时应降低单边信号权重。"
	}
	return summary
}
