package liqmap

import (
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
