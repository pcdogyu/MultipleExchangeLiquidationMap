package liqmap

import (
	"database/sql"
	"math"
	"strconv"
	"strings"
)

func (a *App) orderBookView(ex, mode string, limit int) (any, error) {
	ex = strings.ToLower(strings.TrimSpace(ex))
	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode == "" {
		mode = "weighted"
	}
	if mode != "weighted" && mode != "merged" {
		return nil, BadRequestError{Message: "unknown mode"}
	}
	if limit <= 0 {
		limit = 20
	}
	if ex != "" {
		book := a.ob.get(ex)
		if book == nil {
			return nil, BadRequestError{Message: "unknown exchange"}
		}
		return book.view(limit), nil
	}
	weights := a.orderBookWeights()
	if mode == "merged" {
		weights = map[string]float64{"binance": 1, "bybit": 1, "okx": 1}
	}
	return map[string]any{
		"mode":    mode,
		"binance": a.ob.get("binance").view(limit),
		"bybit":   a.ob.get("bybit").view(limit),
		"okx":     a.ob.get("okx").view(limit),
		"weights": weights,
		"merged":  a.mergedOrderBook(limit, weights),
	}, nil
}

func (a *App) orderBookWeights() map[string]float64 {
	weights := map[string]float64{"binance": 1, "bybit": 1, "okx": 1}
	rows, err := a.db.Query(`SELECT LOWER(exchange), oi_value_usd FROM market_state WHERE symbol=?`, defaultSymbol)
	if err != nil {
		return weights
	}
	defer rows.Close()
	sum := 0.0
	for rows.Next() {
		var ex string
		var oi sql.NullFloat64
		if err := rows.Scan(&ex, &oi); err != nil {
			continue
		}
		if !oi.Valid || oi.Float64 <= 0 {
			continue
		}
		weights[ex] = oi.Float64
		sum += oi.Float64
	}
	if sum <= 0 {
		weights["binance"], weights["bybit"], weights["okx"] = 1.0/3, 1.0/3, 1.0/3
		return weights
	}
	for k, v := range weights {
		weights[k] = v / sum
	}
	return weights
}

func (a *App) mergedOrderBook(limit int, weights map[string]float64) map[string]any {
	type obv struct {
		ex   string
		bids []Level
		asks []Level
	}
	makeView := func(ex string) obv {
		b := a.ob.get(ex)
		if b == nil {
			return obv{ex: ex}
		}
		b.mu.RLock()
		bidMap := copySide(b.Bids)
		askMap := copySide(b.Asks)
		b.mu.RUnlock()
		return obv{ex: ex, bids: sideTop(bidMap, 400, true), asks: sideTop(askMap, 400, false)}
	}
	views := []obv{makeView("binance"), makeView("bybit"), makeView("okx")}
	bidMap := map[string]float64{}
	askMap := map[string]float64{}
	perExBid := map[string]map[string]float64{"binance": {}, "okx": {}, "bybit": {}}
	perExAsk := map[string]map[string]float64{"binance": {}, "okx": {}, "bybit": {}}
	weightedBestBid := 0.0
	weightedBestAsk := 0.0
	sumWBid := 0.0
	sumWAsk := 0.0
	for _, v := range views {
		w := weights[v.ex]
		if w <= 0 {
			continue
		}
		if len(v.bids) > 0 {
			weightedBestBid += v.bids[0].Price * w
			sumWBid += w
		}
		if len(v.asks) > 0 {
			weightedBestAsk += v.asks[0].Price * w
			sumWAsk += w
		}
		for _, lv := range v.bids {
			key := strconv.FormatFloat(math.Round(lv.Price*10)/10, 'f', 1, 64)
			q := lv.Qty * w
			bidMap[key] += q
			perExBid[v.ex][key] += q
		}
		for _, lv := range v.asks {
			key := strconv.FormatFloat(math.Round(lv.Price*10)/10, 'f', 1, 64)
			q := lv.Qty * w
			askMap[key] += q
			perExAsk[v.ex][key] += q
		}
	}
	bestBid := 0.0
	bestAsk := 0.0
	if sumWBid > 0 {
		bestBid = weightedBestBid / sumWBid
	}
	if sumWAsk > 0 {
		bestAsk = weightedBestAsk / sumWAsk
	}
	mid := 0.0
	if bestBid > 0 && bestAsk > 0 {
		mid = (bestBid + bestAsk) / 2
	}
	bids := sideTop(bidMap, limit, true)
	asks := sideTop(askMap, limit, false)
	perExchange := map[string]any{
		"binance": map[string]any{"bids": sideTop(perExBid["binance"], limit, true), "asks": sideTop(perExAsk["binance"], limit, false)},
		"okx":     map[string]any{"bids": sideTop(perExBid["okx"], limit, true), "asks": sideTop(perExAsk["okx"], limit, false)},
		"bybit":   map[string]any{"bids": sideTop(perExBid["bybit"], limit, true), "asks": sideTop(perExAsk["bybit"], limit, false)},
	}
	return map[string]any{
		"best_bid":     bestBid,
		"best_ask":     bestAsk,
		"mid":          mid,
		"bids":         bids,
		"asks":         asks,
		"per_exchange": perExchange,
		"depth_curve":  buildDepthCurve(bids, asks, mid),
	}
}

func buildDepthCurve(bids, asks []Level, mid float64) []map[string]float64 {
	if mid <= 0 {
		return nil
	}
	steps := []float64{5, 10, 20, 30, 50, 75, 100}
	out := make([]map[string]float64, 0, len(steps))
	for _, bps := range steps {
		bidNotional := 0.0
		askNotional := 0.0
		for _, lv := range bids {
			dist := (mid - lv.Price) / mid * 10000
			if dist <= bps && dist >= 0 {
				bidNotional += lv.Price * lv.Qty
			}
		}
		for _, lv := range asks {
			dist := (lv.Price - mid) / mid * 10000
			if dist <= bps && dist >= 0 {
				askNotional += lv.Price * lv.Qty
			}
		}
		out = append(out, map[string]float64{
			"bps":          bps,
			"bid_notional": bidNotional,
			"ask_notional": askNotional,
		})
	}
	return out
}
