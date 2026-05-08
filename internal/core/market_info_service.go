package liqmap

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"sort"
	"strings"
	"time"
)

const (
	defaultMarketInfoPeriod = "5m"
	defaultMarketInfoLimit  = 288
	marketInfoCacheFreshAge = 15 * time.Second
)

var marketInfoWindows = []struct {
	label   string
	minutes int
}{
	{label: "5m", minutes: 5},
	{label: "1h", minutes: 60},
	{label: "4h", minutes: 240},
	{label: "24h", minutes: 1440},
}

func (a *App) MarketInfo(symbol, period string, limit int) (MarketInfoResponse, error) {
	symbol = normalizeMarketInfoSymbol(symbol)
	period = normalizeMarketInfoPeriod(period)
	limit = normalizeMarketInfoLimit(limit)

	if cached, ok, err := a.loadMarketInfoCache(symbol, period, limit); err != nil {
		return a.buildMarketInfoLocalCache(symbol, period, limit, "cache_error")
	} else if ok {
		refreshAfter := cached.CacheUpdatedAt
		if cached.CacheStatus != "ready" {
			refreshAfter = 0
		}
		cached.Refreshing = a.triggerMarketInfoRefresh(symbol, period, limit, refreshAfter)
		return cached, nil
	}

	resp, err := a.buildMarketInfoLocalCache(symbol, period, limit, "local")
	if err != nil {
		return resp, err
	}
	resp.Refreshing = a.triggerMarketInfoRefresh(symbol, period, limit, 0)
	if len(resp.Series.OI) == 0 && len(resp.Series.LongShort) == 0 && resp.Current.UpdatedTS == 0 {
		resp.Warnings = append(resp.Warnings, "本地市场信息缓存为空，正在后台拉取最新数据。")
	}
	return resp, nil
}

func newMarketInfoResponse(symbol, period string, limit int) MarketInfoResponse {
	return MarketInfoResponse{
		Exchange:    "binance",
		Symbol:      symbol,
		Period:      period,
		Limit:       limit,
		GeneratedAt: time.Now().UnixMilli(),
		Gamma: MarketInfoGammaStatus{
			Status:  "pending_options_chain",
			Label:   "Gamma / GEX 待接入",
			Message: "第一版先展示 Binance USDⓈ-M 永续合约市场信息。Gamma 属于期权链希腊值，需要接入期权 OI、strike、expiry 和 greeks 后再计算，暂不混入永续指标。",
		},
	}
}

func (a *App) buildMarketInfoLocalCache(symbol, period string, limit int, status string) (MarketInfoResponse, error) {
	resp := newMarketInfoResponse(symbol, period, limit)
	resp.CacheStatus = status
	resp.CacheUpdatedAt = resp.GeneratedAt
	if current, ok, err := a.loadBinanceMarketInfoCurrent(symbol); err != nil {
		return resp, err
	} else if ok {
		resp.Current = current
		resp.Current.CurrentDataQuality = "db"
	}

	lookbackMinutes := periodMinutes(period) * limit
	if lookbackMinutes < 24*60 {
		lookbackMinutes = 24 * 60
	}
	oiHistory, dbRatioHistory, err := a.loadBinanceOISnapshotHistory(symbol, resp.GeneratedAt-int64(lookbackMinutes)*60*1000)
	if err != nil {
		return resp, err
	}
	resp.Series.OI = oiHistory
	resp.Series.LongShort = dbRatioHistory
	return finalizeMarketInfoResponse(resp), nil
}

func (a *App) fetchMarketInfoLive(symbol, period string, limit int, onBatch func(MarketInfoResponse)) (MarketInfoResponse, error) {
	resp, err := a.buildMarketInfoLocalCache(symbol, period, limit, "refreshing")
	if err != nil {
		return resp, err
	}
	publish := func() {
		resp.GeneratedAt = time.Now().UnixMilli()
		resp.CacheStatus = "refreshing"
		resp.CacheUpdatedAt = resp.GeneratedAt
		resp = finalizeMarketInfoResponse(resp)
		if onBatch != nil {
			onBatch(resp)
		}
	}

	if current, tickerWarnings, err := a.fetchBinanceMarketInfoCurrent(symbol); err == nil {
		resp.Current = mergeMarketInfoCurrent(resp.Current, current)
		resp.Current.CurrentDataQuality = "live"
		resp.Warnings = append(resp.Warnings, tickerWarnings...)
	} else {
		resp.Warnings = append(resp.Warnings, "Binance 当前市场信息接口不可用，已使用本地缓存: "+err.Error())
	}
	publish()

	lookbackMinutes := periodMinutes(period) * limit
	if lookbackMinutes < 24*60 {
		lookbackMinutes = 24 * 60
	}
	oiHistory, dbRatioHistory, err := a.loadBinanceOISnapshotHistory(symbol, resp.GeneratedAt-int64(lookbackMinutes)*60*1000)
	if err != nil {
		return resp, err
	}

	if oi, err := a.fetchBinanceOIHistory(symbol, period, limit); err == nil && len(oi) > 0 {
		resp.Series.OI = oi
	} else {
		if err != nil {
			resp.Warnings = append(resp.Warnings, "Binance OI 历史接口不可用，已尝试使用本地快照: "+err.Error())
		}
		resp.Series.OI = oiHistory
	}
	publish()

	if globalRatio, err := a.fetchBinanceRatioHistory(symbol, period, limit, "globalLongShortAccountRatio", "全账户多空比"); err == nil && len(globalRatio) > 0 {
		resp.Series.LongShort = globalRatio
	} else {
		if err != nil {
			resp.Warnings = append(resp.Warnings, "Binance 全账户多空比接口不可用，已尝试使用本地快照: "+err.Error())
		}
		resp.Series.LongShort = dbRatioHistory
	}
	publish()

	if topRatio, err := a.fetchBinanceRatioHistory(symbol, period, limit, "topLongShortPositionRatio", "顶级交易员持仓比"); err == nil && len(topRatio) > 0 {
		resp.Series.TopPosition = topRatio
	} else if err != nil {
		resp.Warnings = append(resp.Warnings, "Binance 顶级交易员持仓比接口不可用: "+err.Error())
	}
	publish()

	if taker, err := a.fetchBinanceTakerHistory(symbol, period, limit); err == nil && len(taker) > 0 {
		resp.Series.Taker = taker
	} else if err != nil {
		resp.Warnings = append(resp.Warnings, "Binance 主动买卖量/CVD 接口不可用: "+err.Error())
	}
	publish()

	gamma, gammaWarnings := a.fetchBinanceOptionsGamma(symbol, resp.Current.MarkPrice)
	resp.Gamma = gamma
	resp.Warnings = append(resp.Warnings, gammaWarnings...)
	publish()
	resp.CacheStatus = "ready"
	resp.CacheUpdatedAt = resp.GeneratedAt
	resp.Refreshing = false
	return finalizeMarketInfoResponse(resp), nil
}

func finalizeMarketInfoResponse(resp MarketInfoResponse) MarketInfoResponse {
	resp.Current = fillMarketInfoCurrentFromSeries(resp.Current, resp.Series)
	resp.Windows = buildMarketInfoWindows(resp.Series)
	if resp.Current.NetPositionBasis == "" {
		resp.Current.NetPositionBasis = "top_position_ratio"
	}
	if resp.Current.NetPositionLabel == "" {
		resp.Current.NetPositionLabel = marketInfoNetPositionLabel(resp.Current.NetPositionUSD)
	}
	return resp
}

func marketInfoCacheKey(symbol, period string, limit int) string {
	return strings.ToUpper(symbol) + "|" + strings.ToLower(period) + "|" + fmt.Sprint(limit)
}

func (a *App) triggerMarketInfoRefresh(symbol, period string, limit int, cacheUpdatedAt int64) bool {
	if cacheUpdatedAt > 0 && time.Since(time.UnixMilli(cacheUpdatedAt)) < marketInfoCacheFreshAge {
		return false
	}
	key := marketInfoCacheKey(symbol, period, limit)
	now := time.Now()

	a.marketInfoRefreshMu.Lock()
	if a.marketInfoRefreshes == nil {
		a.marketInfoRefreshes = map[string]time.Time{}
	}
	if startedAt, ok := a.marketInfoRefreshes[key]; ok && now.Sub(startedAt) < 5*time.Minute {
		a.marketInfoRefreshMu.Unlock()
		return true
	}
	a.marketInfoRefreshes[key] = now
	a.marketInfoRefreshMu.Unlock()

	go func() {
		defer func() {
			a.marketInfoRefreshMu.Lock()
			delete(a.marketInfoRefreshes, key)
			a.marketInfoRefreshMu.Unlock()
		}()
		if err := a.refreshMarketInfoCache(symbol, period, limit); err != nil {
			resp, localErr := a.buildMarketInfoLocalCache(symbol, period, limit, "error")
			if localErr == nil {
				resp.Warnings = append(resp.Warnings, "市场信息后台刷新失败: "+err.Error())
				_ = a.saveMarketInfoCache(resp, "error", err.Error())
			}
		}
	}()
	return true
}

func (a *App) refreshMarketInfoCache(symbol, period string, limit int) error {
	resp, err := a.fetchMarketInfoLive(symbol, period, limit, func(batch MarketInfoResponse) {
		_ = a.saveMarketInfoCache(batch, "refreshing", "")
	})
	if err != nil {
		return err
	}
	return a.saveMarketInfoCache(resp, "ready", "")
}

func (a *App) loadMarketInfoCache(symbol, period string, limit int) (MarketInfoResponse, bool, error) {
	var generatedAt, refreshedAt int64
	var status, errorMessage, payload string
	err := a.db.QueryRow(`SELECT generated_at, refreshed_at, status, error_message, payload_json
		FROM market_info_snapshots
		WHERE exchange='binance' AND symbol=? AND period=? AND limit_count=?
		LIMIT 1`, symbol, period, limit).Scan(&generatedAt, &refreshedAt, &status, &errorMessage, &payload)
	if err == sql.ErrNoRows {
		return MarketInfoResponse{}, false, nil
	}
	if err != nil {
		return MarketInfoResponse{}, false, err
	}
	var resp MarketInfoResponse
	if err := json.Unmarshal([]byte(payload), &resp); err != nil {
		return MarketInfoResponse{}, false, err
	}
	if resp.Exchange == "" {
		resp.Exchange = "binance"
	}
	resp.Symbol = symbol
	resp.Period = period
	resp.Limit = limit
	if generatedAt > 0 {
		resp.GeneratedAt = generatedAt
	}
	if status == "" {
		status = "ready"
	}
	resp.CacheStatus = status
	resp.CacheUpdatedAt = generatedAt
	resp.Refreshing = status == "refreshing"
	if resp.Current.CurrentDataQuality == "" || resp.Current.CurrentDataQuality == "live" {
		resp.Current.CurrentDataQuality = "cache"
	}
	if errorMessage != "" {
		resp.Warnings = append(resp.Warnings, "市场信息后台刷新失败: "+errorMessage)
	}
	if refreshedAt > resp.CacheUpdatedAt {
		resp.CacheUpdatedAt = refreshedAt
	}
	return finalizeMarketInfoResponse(resp), true, nil
}

func (a *App) saveMarketInfoCache(resp MarketInfoResponse, status, errorMessage string) error {
	if resp.Exchange == "" {
		resp.Exchange = "binance"
	}
	if status == "" {
		status = "ready"
	}
	resp = finalizeMarketInfoResponse(resp)
	resp.CacheStatus = status
	resp.Refreshing = status == "refreshing"
	resp.CacheUpdatedAt = resp.GeneratedAt
	payload, err := json.Marshal(resp)
	if err != nil {
		return err
	}
	refreshedAt := time.Now().UnixMilli()
	_, err = a.db.Exec(`INSERT INTO market_info_snapshots(
			exchange, symbol, period, limit_count, generated_at, refreshed_at, status, error_message, payload_json
		) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(exchange, symbol, period, limit_count) DO UPDATE SET
			generated_at=excluded.generated_at,
			refreshed_at=excluded.refreshed_at,
			status=excluded.status,
			error_message=excluded.error_message,
			payload_json=excluded.payload_json`,
		resp.Exchange, resp.Symbol, resp.Period, resp.Limit, resp.GeneratedAt, refreshedAt, status, errorMessage, string(payload))
	return err
}

func normalizeMarketInfoSymbol(raw string) string {
	s := strings.ToUpper(strings.TrimSpace(raw))
	if s == "" {
		return defaultSymbol
	}
	var b strings.Builder
	for _, r := range s {
		if (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 {
		return defaultSymbol
	}
	return b.String()
}

func normalizeMarketInfoPeriod(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "5m", "15m", "30m", "1h", "2h", "4h", "6h", "12h", "1d":
		return strings.ToLower(strings.TrimSpace(raw))
	default:
		return defaultMarketInfoPeriod
	}
}

func normalizeMarketInfoLimit(limit int) int {
	if limit <= 0 {
		return defaultMarketInfoLimit
	}
	if limit < 2 {
		return 2
	}
	if limit > 500 {
		return 500
	}
	return limit
}

func periodMinutes(period string) int {
	switch normalizeMarketInfoPeriod(period) {
	case "15m":
		return 15
	case "30m":
		return 30
	case "1h":
		return 60
	case "2h":
		return 120
	case "4h":
		return 240
	case "6h":
		return 360
	case "12h":
		return 720
	case "1d":
		return 1440
	default:
		return 5
	}
}

func (a *App) loadBinanceMarketInfoCurrent(symbol string) (MarketInfoCurrent, bool, error) {
	rows, err := a.db.Query(`SELECT mark_price, oi_qty, oi_value_usd, funding_rate, long_short_ratio, updated_ts
		FROM market_state WHERE LOWER(exchange)='binance' AND symbol=? LIMIT 1`, symbol)
	if err != nil {
		return MarketInfoCurrent{}, false, err
	}
	defer rows.Close()
	if !rows.Next() {
		return MarketInfoCurrent{}, false, rows.Err()
	}
	var current MarketInfoCurrent
	var oiQty, oiValue, funding, lsr sql.NullFloat64
	if err := rows.Scan(&current.MarkPrice, &oiQty, &oiValue, &funding, &lsr, &current.UpdatedTS); err != nil {
		return MarketInfoCurrent{}, false, err
	}
	if oiQty.Valid {
		current.OIQty = oiQty.Float64
	}
	if oiValue.Valid {
		current.OIValueUSD = oiValue.Float64
	}
	if funding.Valid {
		current.FundingRate = &funding.Float64
	}
	if lsr.Valid && lsr.Float64 > 0 {
		current.LongShortRatio = &lsr.Float64
	}
	current.NetPositionBasis = "global_account_ratio"
	return current, true, rows.Err()
}

func (a *App) loadBinanceOISnapshotHistory(symbol string, startTS int64) ([]MarketInfoOIPoint, []MarketInfoRatioPoint, error) {
	rows, err := a.db.Query(`SELECT mark_price, oi_value_usd, long_short_ratio, updated_ts
		FROM oi_snapshots
		WHERE LOWER(exchange)='binance' AND symbol=? AND updated_ts>=?
		ORDER BY updated_ts`, symbol, startTS)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	oi := make([]MarketInfoOIPoint, 0, 256)
	ratios := make([]MarketInfoRatioPoint, 0, 256)
	for rows.Next() {
		var mark, oiValue float64
		var lsr sql.NullFloat64
		var ts int64
		if err := rows.Scan(&mark, &oiValue, &lsr, &ts); err != nil {
			return nil, nil, err
		}
		if mark > 0 && oiValue > 0 {
			oi = append(oi, MarketInfoOIPoint{
				TS:         ts,
				OIQty:      oiValue / mark,
				OIValueUSD: oiValue,
			})
		}
		if lsr.Valid && lsr.Float64 > 0 {
			longShare, shortShare := marketInfoSharesFromRatio(lsr.Float64)
			ratios = append(ratios, MarketInfoRatioPoint{
				TS:          ts,
				Ratio:       lsr.Float64,
				LongShare:   longShare,
				ShortShare:  shortShare,
				SourceLabel: "本地全账户多空比",
			})
		}
	}
	return oi, ratios, rows.Err()
}

func (a *App) fetchBinanceMarketInfoCurrent(symbol string) (MarketInfoCurrent, []string, error) {
	var premium struct {
		MarkPrice            string `json:"markPrice"`
		LastFundingRate      string `json:"lastFundingRate"`
		EstimatedSettlePrice string `json:"estimatedSettlePrice"`
		NextFundingTime      int64  `json:"nextFundingTime"`
		Time                 int64  `json:"time"`
	}
	var oi struct {
		OpenInterest string `json:"openInterest"`
		Time         int64  `json:"time"`
	}
	var ticker struct {
		PriceChangePercent string `json:"priceChangePercent"`
		HighPrice          string `json:"highPrice"`
		LowPrice           string `json:"lowPrice"`
		QuoteVolume        string `json:"quoteVolume"`
		CloseTime          int64  `json:"closeTime"`
	}
	var book struct {
		BidPrice string `json:"bidPrice"`
		AskPrice string `json:"askPrice"`
		Time     int64  `json:"time"`
	}
	if err := a.fetchJSON("https://fapi.binance.com/fapi/v1/premiumIndex?symbol="+url.QueryEscape(symbol), &premium); err != nil {
		return MarketInfoCurrent{}, nil, err
	}
	if err := a.fetchJSON("https://fapi.binance.com/fapi/v1/openInterest?symbol="+url.QueryEscape(symbol), &oi); err != nil {
		return MarketInfoCurrent{}, nil, err
	}
	warnings := make([]string, 0, 2)
	if err := a.fetchJSON("https://fapi.binance.com/fapi/v1/ticker/24hr?symbol="+url.QueryEscape(symbol), &ticker); err != nil {
		warnings = append(warnings, "Binance 24h 行情接口不可用: "+err.Error())
	}
	if err := a.fetchJSON("https://fapi.binance.com/fapi/v1/ticker/bookTicker?symbol="+url.QueryEscape(symbol), &book); err != nil {
		warnings = append(warnings, "Binance 盘口价差接口不可用: "+err.Error())
	}
	mark := parseFloat(premium.MarkPrice)
	oiQty := parseFloat(oi.OpenInterest)
	funding := parseFloat(premium.LastFundingRate)
	ts := premium.Time
	if ts <= 0 {
		ts = time.Now().UnixMilli()
	}
	current := MarketInfoCurrent{
		MarkPrice:       mark,
		OIQty:           oiQty,
		OIValueUSD:      oiQty * mark,
		FundingRate:     &funding,
		UpdatedTS:       ts,
		NextFundingTime: premium.NextFundingTime,
		EstimatedSettle: parseFloat(premium.EstimatedSettlePrice),
	}
	current.PriceChangePct24h = parseFloat(ticker.PriceChangePercent) / 100
	current.QuoteVolume24h = parseFloat(ticker.QuoteVolume)
	current.High24h = parseFloat(ticker.HighPrice)
	current.Low24h = parseFloat(ticker.LowPrice)
	current.BidPrice = parseFloat(book.BidPrice)
	current.AskPrice = parseFloat(book.AskPrice)
	if current.BidPrice > 0 && current.AskPrice > 0 {
		current.BidAskSpread = current.AskPrice - current.BidPrice
		mid := (current.BidPrice + current.AskPrice) / 2
		if mid > 0 {
			current.BidAskSpreadPct = current.BidAskSpread / mid
		}
	}
	if ticker.CloseTime > current.UpdatedTS {
		current.UpdatedTS = ticker.CloseTime
	}
	if book.Time > current.UpdatedTS {
		current.UpdatedTS = book.Time
	}
	return current, warnings, nil
}

func (a *App) fetchBinanceOIHistory(symbol, period string, limit int) ([]MarketInfoOIPoint, error) {
	u := fmt.Sprintf("https://fapi.binance.com/futures/data/openInterestHist?symbol=%s&period=%s&limit=%d",
		url.QueryEscape(symbol), url.QueryEscape(normalizeMarketInfoPeriod(period)), normalizeMarketInfoLimit(limit))
	var rows []map[string]any
	if err := a.fetchJSON(u, &rows); err != nil {
		return nil, err
	}
	out := make([]MarketInfoOIPoint, 0, len(rows))
	for _, row := range rows {
		ts := marketInfoParseTS(row["timestamp"])
		qty := parseAnyFloat(row["sumOpenInterest"])
		value := parseAnyFloat(row["sumOpenInterestValue"])
		if ts <= 0 || !(qty > 0 || value > 0) {
			continue
		}
		out = append(out, MarketInfoOIPoint{
			TS:         ts,
			OIQty:      qty,
			OIValueUSD: value,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].TS < out[j].TS })
	return out, nil
}

func (a *App) fetchBinanceRatioHistory(symbol, period string, limit int, endpoint, label string) ([]MarketInfoRatioPoint, error) {
	u := fmt.Sprintf("https://fapi.binance.com/futures/data/%s?symbol=%s&period=%s&limit=%d",
		url.PathEscape(endpoint), url.QueryEscape(symbol), url.QueryEscape(normalizeMarketInfoPeriod(period)), normalizeMarketInfoLimit(limit))
	var rows []map[string]any
	if err := a.fetchJSON(u, &rows); err != nil {
		return nil, err
	}
	out := make([]MarketInfoRatioPoint, 0, len(rows))
	for _, row := range rows {
		ts := marketInfoParseTS(row["timestamp"])
		ratio := parseAnyFloat(row["longShortRatio"])
		longShare := parseAnyFloat(row["longAccount"])
		if !(longShare > 0) {
			longShare = parseAnyFloat(row["longPosition"])
		}
		shortShare := parseAnyFloat(row["shortAccount"])
		if !(shortShare > 0) {
			shortShare = parseAnyFloat(row["shortPosition"])
		}
		if !(ratio > 0) && longShare > 0 && shortShare > 0 {
			ratio = longShare / shortShare
		}
		if !(longShare > 0 && shortShare > 0) && ratio > 0 {
			longShare, shortShare = marketInfoSharesFromRatio(ratio)
		}
		if ts <= 0 || !(ratio > 0) {
			continue
		}
		out = append(out, MarketInfoRatioPoint{
			TS:          ts,
			Ratio:       ratio,
			LongShare:   longShare,
			ShortShare:  shortShare,
			SourceLabel: label,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].TS < out[j].TS })
	return out, nil
}

func (a *App) fetchBinanceTakerHistory(symbol, period string, limit int) ([]MarketInfoTakerPoint, error) {
	u := fmt.Sprintf("https://fapi.binance.com/futures/data/takerlongshortRatio?symbol=%s&period=%s&limit=%d",
		url.QueryEscape(symbol), url.QueryEscape(normalizeMarketInfoPeriod(period)), normalizeMarketInfoLimit(limit))
	var rows []map[string]any
	if err := a.fetchJSON(u, &rows); err != nil {
		return nil, err
	}
	raw := make([]MarketInfoTakerPoint, 0, len(rows))
	for _, row := range rows {
		ts := marketInfoParseTS(row["timestamp"])
		buyVol := parseAnyFloat(row["buyVol"])
		sellVol := parseAnyFloat(row["sellVol"])
		ratio := parseAnyFloat(row["buySellRatio"])
		if !(ratio > 0) && sellVol > 0 {
			ratio = buyVol / sellVol
		}
		if ts <= 0 || !(buyVol > 0 || sellVol > 0) {
			continue
		}
		raw = append(raw, MarketInfoTakerPoint{
			TS:           ts,
			BuyVol:       buyVol,
			SellVol:      sellVol,
			BuySellRatio: ratio,
			Delta:        buyVol - sellVol,
		})
	}
	sort.Slice(raw, func(i, j int) bool { return raw[i].TS < raw[j].TS })
	cvd := 0.0
	for i := range raw {
		cvd += raw[i].Delta
		raw[i].CVD = cvd
	}
	return raw, nil
}

type marketInfoOptionMeta struct {
	Symbol     string
	Underlying string
	ExpiryTS   int64
	Expiration string
	Strike     float64
	Side       string
	Unit       float64
}

type marketInfoOptionMark struct {
	Gamma     float64
	MarkPrice float64
	UpdatedTS int64
}

func (a *App) fetchBinanceOptionsGamma(symbol string, spot float64) (MarketInfoGammaStatus, []string) {
	underlyingAsset := marketInfoUnderlyingAsset(symbol)
	underlying := underlyingAsset + "USDT"
	status := MarketInfoGammaStatus{
		Status:     "unavailable",
		Label:      "Gamma / GEX",
		Underlying: underlying,
		SpotPrice:  spot,
		Method:     "Binance Options mark gamma * openInterest * contractUnit * spot^2 * 1%，CALL 计正、PUT 计负；这是公开期权链估算，不代表交易所或做市商真实净仓。",
		Message:    "Gamma 数据暂不可用。",
	}
	if underlyingAsset == "" || !(spot > 0) {
		status.Message = "缺少有效标的价格，暂不能计算 Gamma / GEX。"
		return status, nil
	}

	metas, expirations, err := a.fetchBinanceOptionMetas(underlying)
	if err != nil {
		status.Message = "Binance Options 合约列表不可用。"
		return status, []string{"Binance Options 合约列表接口不可用: " + err.Error()}
	}
	if len(metas) == 0 || len(expirations) == 0 {
		status.Message = "没有找到可用的 " + underlying + " 期权合约。"
		return status, nil
	}

	marks, markWarnings := a.fetchBinanceOptionMarks()
	warnings := append([]string{}, markWarnings...)
	if len(marks) == 0 {
		status.Message = "Binance Options greeks 不可用。"
		return status, warnings
	}

	metaBySymbol := make(map[string]marketInfoOptionMeta, len(metas))
	selectedExpirations := map[string]struct{}{}
	for _, exp := range expirations {
		selectedExpirations[exp] = struct{}{}
	}
	for _, meta := range metas {
		if _, ok := selectedExpirations[meta.Expiration]; ok {
			metaBySymbol[meta.Symbol] = meta
		}
	}

	levelByStrike := map[float64]*MarketInfoGammaLevel{}
	expiryByCode := map[string]*MarketInfoGammaExpiry{}
	latestTS := int64(0)
	for _, meta := range metas {
		if _, ok := selectedExpirations[meta.Expiration]; !ok {
			continue
		}
		exp := expiryByCode[meta.Expiration]
		if exp == nil {
			exp = &MarketInfoGammaExpiry{Expiration: meta.Expiration, ExpiryTS: meta.ExpiryTS}
			expiryByCode[meta.Expiration] = exp
		}
	}

	for _, expCode := range expirations {
		oiRows, err := a.fetchBinanceOptionOpenInterest(underlyingAsset, expCode)
		if err != nil {
			warnings = append(warnings, "Binance Options OI "+expCode+" 不可用: "+err.Error())
			continue
		}
		for _, row := range oiRows {
			sym := strings.TrimSpace(fmt.Sprint(row["symbol"]))
			meta, ok := metaBySymbol[sym]
			if !ok {
				continue
			}
			mark, ok := marks[sym]
			if !ok || !(mark.Gamma > 0) {
				continue
			}
			oi := parseAnyFloat(row["sumOpenInterest"])
			if !(oi > 0) {
				continue
			}
			if ts := marketInfoParseTS(row["timestamp"]); ts > latestTS {
				latestTS = ts
			}
			if mark.UpdatedTS > latestTS {
				latestTS = mark.UpdatedTS
			}
			unit := meta.Unit
			if !(unit > 0) {
				unit = 1
			}
			gex := marketInfoGammaExposureUSD(spot, mark.Gamma, oi, unit)
			level := levelByStrike[meta.Strike]
			if level == nil {
				level = &MarketInfoGammaLevel{Strike: meta.Strike}
				levelByStrike[meta.Strike] = level
			}
			exp := expiryByCode[meta.Expiration]
			if exp == nil {
				exp = &MarketInfoGammaExpiry{Expiration: meta.Expiration, ExpiryTS: meta.ExpiryTS}
				expiryByCode[meta.Expiration] = exp
			}
			level.Contracts++
			exp.Contracts++
			status.Contracts++
			status.TotalOI += oi * unit
			switch marketInfoOptionSide(meta) {
			case "put":
				level.PutOI += oi * unit
				level.PutGEX -= gex
				exp.PutOI += oi * unit
				exp.PutGEX -= gex
				status.PutOI += oi * unit
				status.PutGEXUSD -= gex
			default:
				level.CallOI += oi * unit
				level.CallGEX += gex
				exp.CallOI += oi * unit
				exp.CallGEX += gex
				status.CallOI += oi * unit
				status.CallGEXUSD += gex
			}
		}
	}

	levels := make([]MarketInfoGammaLevel, 0, len(levelByStrike))
	for _, level := range levelByStrike {
		level.NetGEX = level.CallGEX + level.PutGEX
		level.AbsGEX = math.Abs(level.CallGEX) + math.Abs(level.PutGEX)
		if level.Contracts > 0 {
			levels = append(levels, *level)
		}
	}
	sort.Slice(levels, func(i, j int) bool { return levels[i].Strike < levels[j].Strike })
	if len(levels) > 140 {
		sort.Slice(levels, func(i, j int) bool { return levels[i].AbsGEX > levels[j].AbsGEX })
		levels = append([]MarketInfoGammaLevel(nil), levels[:140]...)
		sort.Slice(levels, func(i, j int) bool { return levels[i].Strike < levels[j].Strike })
	}

	expiries := make([]MarketInfoGammaExpiry, 0, len(expiryByCode))
	for _, exp := range expiryByCode {
		exp.NetGEX = exp.CallGEX + exp.PutGEX
		exp.AbsGEX = math.Abs(exp.CallGEX) + math.Abs(exp.PutGEX)
		if exp.Contracts > 0 {
			expiries = append(expiries, *exp)
		}
	}
	sort.Slice(expiries, func(i, j int) bool { return expiries[i].ExpiryTS < expiries[j].ExpiryTS })

	status.Levels = levels
	status.Expiries = expiries
	status.Expirations = len(expiries)
	status.UpdatedTS = latestTS
	maxPositiveGEX, maxNegativeGEX, maxAbsGEX := 0.0, 0.0, 0.0
	for _, level := range levels {
		status.NetGEXUSD += level.NetGEX
		status.AbsGEXUSD += level.AbsGEX
		if level.NetGEX > maxPositiveGEX {
			maxPositiveGEX = level.NetGEX
			status.MaxPositiveStrike = level.Strike
		}
		if level.NetGEX < maxNegativeGEX {
			maxNegativeGEX = level.NetGEX
			status.MaxNegativeStrike = level.Strike
		}
		if level.AbsGEX > maxAbsGEX {
			maxAbsGEX = level.AbsGEX
			status.GammaWallStrike = level.Strike
		}
	}

	if status.Contracts == 0 {
		status.Message = "未从 Binance Options 聚合到有效 OI + gamma 合约。"
		if len(warnings) > 0 {
			status.Status = "partial"
		}
		return status, warnings
	}
	if len(warnings) > 0 {
		status.Status = "partial"
	} else {
		status.Status = "ready"
	}
	status.Message = fmt.Sprintf("已聚合 %s %d 个期权合约、%d 个到期日，估算 1%% 标的价格变动 GEX。", underlying, status.Contracts, status.Expirations)
	return status, warnings
}

func (a *App) fetchBinanceOptionMetas(underlying string) ([]marketInfoOptionMeta, []string, error) {
	var resp struct {
		OptionSymbols []map[string]any `json:"optionSymbols"`
	}
	if err := a.fetchJSON("https://eapi.binance.com/eapi/v1/exchangeInfo", &resp); err != nil {
		return nil, nil, err
	}
	nowTS := time.Now().UnixMilli()
	metas := make([]marketInfoOptionMeta, 0, len(resp.OptionSymbols))
	expSet := map[string]int64{}
	for _, row := range resp.OptionSymbols {
		rowUnderlying := strings.ToUpper(strings.TrimSpace(fmt.Sprint(row["underlying"])))
		if rowUnderlying != strings.ToUpper(underlying) {
			continue
		}
		sym := strings.TrimSpace(fmt.Sprint(row["symbol"]))
		expiryTS := marketInfoParseTS(row["expiryDate"])
		if sym == "" || expiryTS < nowTS-24*60*60*1000 {
			continue
		}
		expCode := marketInfoOptionExpirationCode(expiryTS)
		if expCode == "" {
			continue
		}
		unit := parseAnyFloat(row["unit"])
		if !(unit > 0) {
			unit = 1
		}
		meta := marketInfoOptionMeta{
			Symbol:     sym,
			Underlying: rowUnderlying,
			ExpiryTS:   expiryTS,
			Expiration: expCode,
			Strike:     parseAnyFloat(row["strikePrice"]),
			Side:       strings.ToLower(strings.TrimSpace(fmt.Sprint(row["side"]))),
			Unit:       unit,
		}
		if meta.Strike <= 0 {
			meta.Strike = marketInfoStrikeFromOptionSymbol(sym)
		}
		metas = append(metas, meta)
		expSet[expCode] = expiryTS
	}
	expirations := make([]string, 0, len(expSet))
	for exp := range expSet {
		expirations = append(expirations, exp)
	}
	sort.Slice(expirations, func(i, j int) bool { return expSet[expirations[i]] < expSet[expirations[j]] })
	if len(expirations) > 12 {
		expirations = expirations[:12]
	}
	return metas, expirations, nil
}

func (a *App) fetchBinanceOptionMarks() (map[string]marketInfoOptionMark, []string) {
	var rows []map[string]any
	if err := a.fetchJSON("https://eapi.binance.com/eapi/v1/mark", &rows); err != nil {
		return nil, []string{"Binance Options mark/greeks 接口不可用: " + err.Error()}
	}
	out := make(map[string]marketInfoOptionMark, len(rows))
	for _, row := range rows {
		sym := strings.TrimSpace(fmt.Sprint(row["symbol"]))
		if sym == "" {
			continue
		}
		out[sym] = marketInfoOptionMark{
			Gamma:     parseAnyFloat(row["gamma"]),
			MarkPrice: parseAnyFloat(row["markPrice"]),
			UpdatedTS: marketInfoParseTS(row["time"]),
		}
	}
	return out, nil
}

func (a *App) fetchBinanceOptionOpenInterest(underlyingAsset, expiration string) ([]map[string]any, error) {
	u := fmt.Sprintf("https://eapi.binance.com/eapi/v1/openInterest?underlyingAsset=%s&expiration=%s",
		url.QueryEscape(underlyingAsset), url.QueryEscape(expiration))
	var rows []map[string]any
	if err := a.fetchJSON(u, &rows); err != nil {
		return nil, err
	}
	return rows, nil
}

func marketInfoParseTS(v any) int64 {
	return int64(parseAnyFloat(v))
}

func marketInfoUnderlyingAsset(symbol string) string {
	s := normalizeMarketInfoSymbol(symbol)
	if strings.HasSuffix(s, "USDT") {
		return strings.TrimSuffix(s, "USDT")
	}
	if strings.HasSuffix(s, "USDC") {
		return strings.TrimSuffix(s, "USDC")
	}
	if strings.HasSuffix(s, "BUSD") {
		return strings.TrimSuffix(s, "BUSD")
	}
	if idx := strings.Index(s, "-"); idx > 0 {
		return s[:idx]
	}
	return s
}

func marketInfoOptionExpirationCode(expiryTS int64) string {
	if expiryTS <= 0 {
		return ""
	}
	return time.UnixMilli(expiryTS).UTC().Format("060102")
}

func marketInfoGammaExposureUSD(spot, gamma, oi, unit float64) float64 {
	if !(spot > 0 && gamma > 0 && oi > 0 && unit > 0) {
		return 0
	}
	return gamma * oi * unit * spot * spot * 0.01
}

func marketInfoOptionSide(meta marketInfoOptionMeta) string {
	side := strings.ToLower(strings.TrimSpace(meta.Side))
	if strings.Contains(side, "put") || strings.HasSuffix(strings.ToUpper(meta.Symbol), "-P") {
		return "put"
	}
	return "call"
}

func marketInfoStrikeFromOptionSymbol(symbol string) float64 {
	parts := strings.Split(symbol, "-")
	if len(parts) < 4 {
		return 0
	}
	return parseFloat(parts[2])
}

func marketInfoSharesFromRatio(ratio float64) (float64, float64) {
	if !(ratio > 0) {
		return 0, 0
	}
	shortShare := 1 / (1 + ratio)
	longShare := ratio * shortShare
	return longShare, shortShare
}

func mergeMarketInfoCurrent(base, live MarketInfoCurrent) MarketInfoCurrent {
	if live.MarkPrice > 0 {
		base.MarkPrice = live.MarkPrice
	}
	if live.PriceChangePct24h != 0 {
		base.PriceChangePct24h = live.PriceChangePct24h
	}
	if live.QuoteVolume24h > 0 {
		base.QuoteVolume24h = live.QuoteVolume24h
	}
	if live.High24h > 0 {
		base.High24h = live.High24h
	}
	if live.Low24h > 0 {
		base.Low24h = live.Low24h
	}
	if live.BidPrice > 0 {
		base.BidPrice = live.BidPrice
	}
	if live.AskPrice > 0 {
		base.AskPrice = live.AskPrice
	}
	if live.BidAskSpread > 0 {
		base.BidAskSpread = live.BidAskSpread
	}
	if live.BidAskSpreadPct > 0 {
		base.BidAskSpreadPct = live.BidAskSpreadPct
	}
	if live.OIQty > 0 {
		base.OIQty = live.OIQty
	}
	if live.OIValueUSD > 0 {
		base.OIValueUSD = live.OIValueUSD
	}
	if live.FundingRate != nil {
		base.FundingRate = live.FundingRate
	}
	if live.UpdatedTS > 0 {
		base.UpdatedTS = live.UpdatedTS
	}
	if live.NextFundingTime > 0 {
		base.NextFundingTime = live.NextFundingTime
	}
	if live.EstimatedSettle > 0 {
		base.EstimatedSettle = live.EstimatedSettle
	}
	return base
}

func fillMarketInfoCurrentFromSeries(current MarketInfoCurrent, series MarketInfoSeries) MarketInfoCurrent {
	if len(series.OI) > 0 {
		last := series.OI[len(series.OI)-1]
		if current.OIQty <= 0 && last.OIQty > 0 {
			current.OIQty = last.OIQty
		}
		if current.OIValueUSD <= 0 && last.OIValueUSD > 0 {
			current.OIValueUSD = last.OIValueUSD
		}
		if current.UpdatedTS <= 0 {
			current.UpdatedTS = last.TS
		}
	}
	if len(series.LongShort) > 0 {
		last := series.LongShort[len(series.LongShort)-1]
		current.LongShortRatio = &last.Ratio
	}
	if len(series.TopPosition) > 0 {
		last := series.TopPosition[len(series.TopPosition)-1]
		current.TopPositionRatio = &last.Ratio
		current.NetPositionUSD = marketInfoNetPositionUSD(current.OIValueUSD, last)
		current.NetPositionBasis = "top_position_ratio"
	} else if len(series.LongShort) > 0 {
		last := series.LongShort[len(series.LongShort)-1]
		current.NetPositionUSD = marketInfoNetPositionUSD(current.OIValueUSD, last)
		current.NetPositionBasis = "global_account_ratio"
	}
	if len(series.Taker) > 0 {
		last := series.Taker[len(series.Taker)-1]
		current.TakerBuyVol = last.BuyVol
		current.TakerSellVol = last.SellVol
		current.TakerBuySellRatio = last.BuySellRatio
		current.CVD = last.CVD
	}
	current.NetPositionLabel = marketInfoNetPositionLabel(current.NetPositionUSD)
	return current
}

func buildMarketInfoWindows(series MarketInfoSeries) []MarketInfoWindow {
	now := latestMarketInfoTS(series)
	if now <= 0 {
		now = time.Now().UnixMilli()
	}
	windows := make([]MarketInfoWindow, 0, len(marketInfoWindows))
	for _, spec := range marketInfoWindows {
		cutoff := now - int64(spec.minutes)*60*1000
		latestOI, okLatestOI := latestMarketInfoOI(series.OI)
		baseOI, okBaseOI := marketInfoOIAtOrBefore(series.OI, cutoff)
		latestGlobal, okLatestGlobal := latestMarketInfoRatio(series.LongShort)
		baseGlobal, okBaseGlobal := marketInfoRatioAtOrBefore(series.LongShort, cutoff)
		latestTop, okLatestTop := latestMarketInfoRatio(series.TopPosition)
		baseTop, okBaseTop := marketInfoRatioAtOrBefore(series.TopPosition, cutoff)
		latestTaker, okLatestTaker := latestMarketInfoTaker(series.Taker)
		baseTaker, okBaseTaker := marketInfoTakerAtOrBefore(series.Taker, cutoff)

		w := MarketInfoWindow{Label: spec.label, Minutes: spec.minutes}
		if okLatestOI && okBaseOI {
			w.OIDeltaUSD = latestOI.OIValueUSD - baseOI.OIValueUSD
			if baseOI.OIValueUSD > 0 {
				w.OIDeltaPct = w.OIDeltaUSD / baseOI.OIValueUSD
			}
		}
		if okLatestGlobal && okBaseGlobal {
			w.LongShortDelta = latestGlobal.Ratio - baseGlobal.Ratio
		}
		if okLatestTop && okBaseTop && okLatestOI && okBaseOI {
			w.TopPositionDelta = latestTop.Ratio - baseTop.Ratio
			w.NetPositionDeltaUSD = marketInfoNetPositionUSD(latestOI.OIValueUSD, latestTop) - marketInfoNetPositionUSD(baseOI.OIValueUSD, baseTop)
		} else if okLatestGlobal && okBaseGlobal && okLatestOI && okBaseOI {
			w.NetPositionDeltaUSD = marketInfoNetPositionUSD(latestOI.OIValueUSD, latestGlobal) - marketInfoNetPositionUSD(baseOI.OIValueUSD, baseGlobal)
		}
		if okLatestTaker && okBaseTaker {
			w.CVDDelta = latestTaker.CVD - baseTaker.CVD
			w.TakerDelta = w.CVDDelta
		}
		w.Verdict = marketInfoWindowVerdict(w)
		windows = append(windows, w)
	}
	return windows
}

func latestMarketInfoTS(series MarketInfoSeries) int64 {
	latest := int64(0)
	if len(series.OI) > 0 && series.OI[len(series.OI)-1].TS > latest {
		latest = series.OI[len(series.OI)-1].TS
	}
	if len(series.LongShort) > 0 && series.LongShort[len(series.LongShort)-1].TS > latest {
		latest = series.LongShort[len(series.LongShort)-1].TS
	}
	if len(series.TopPosition) > 0 && series.TopPosition[len(series.TopPosition)-1].TS > latest {
		latest = series.TopPosition[len(series.TopPosition)-1].TS
	}
	if len(series.Taker) > 0 && series.Taker[len(series.Taker)-1].TS > latest {
		latest = series.Taker[len(series.Taker)-1].TS
	}
	return latest
}

func latestMarketInfoOI(points []MarketInfoOIPoint) (MarketInfoOIPoint, bool) {
	if len(points) == 0 {
		return MarketInfoOIPoint{}, false
	}
	return points[len(points)-1], true
}

func marketInfoOIAtOrBefore(points []MarketInfoOIPoint, ts int64) (MarketInfoOIPoint, bool) {
	if len(points) == 0 {
		return MarketInfoOIPoint{}, false
	}
	best := points[0]
	for _, pt := range points {
		if pt.TS <= ts {
			best = pt
			continue
		}
		break
	}
	return best, true
}

func latestMarketInfoRatio(points []MarketInfoRatioPoint) (MarketInfoRatioPoint, bool) {
	if len(points) == 0 {
		return MarketInfoRatioPoint{}, false
	}
	return points[len(points)-1], true
}

func marketInfoRatioAtOrBefore(points []MarketInfoRatioPoint, ts int64) (MarketInfoRatioPoint, bool) {
	if len(points) == 0 {
		return MarketInfoRatioPoint{}, false
	}
	best := points[0]
	for _, pt := range points {
		if pt.TS <= ts {
			best = pt
			continue
		}
		break
	}
	return best, true
}

func latestMarketInfoTaker(points []MarketInfoTakerPoint) (MarketInfoTakerPoint, bool) {
	if len(points) == 0 {
		return MarketInfoTakerPoint{}, false
	}
	return points[len(points)-1], true
}

func marketInfoTakerAtOrBefore(points []MarketInfoTakerPoint, ts int64) (MarketInfoTakerPoint, bool) {
	if len(points) == 0 {
		return MarketInfoTakerPoint{}, false
	}
	best := points[0]
	for _, pt := range points {
		if pt.TS <= ts {
			best = pt
			continue
		}
		break
	}
	return best, true
}

func marketInfoNetPositionUSD(oiUSD float64, ratio MarketInfoRatioPoint) float64 {
	if !(oiUSD > 0) {
		return 0
	}
	longShare, shortShare := ratio.LongShare, ratio.ShortShare
	if !(longShare > 0 && shortShare > 0) && ratio.Ratio > 0 {
		longShare, shortShare = marketInfoSharesFromRatio(ratio.Ratio)
	}
	return (longShare - shortShare) * oiUSD
}

func marketInfoNetPositionLabel(v float64) string {
	if math.Abs(v) < 1 {
		return "净多净空不明显"
	}
	if v > 0 {
		return "估算净多"
	}
	return "估算净空"
}

func marketInfoWindowVerdict(w MarketInfoWindow) string {
	oiUp := w.OIDeltaUSD > 0
	oiDown := w.OIDeltaUSD < 0
	cvdUp := w.CVDDelta > 0
	cvdDown := w.CVDDelta < 0
	netUp := w.NetPositionDeltaUSD > 0
	netDown := w.NetPositionDeltaUSD < 0
	switch {
	case oiUp && cvdUp && netUp:
		return "增仓上攻，多头主动增强"
	case oiUp && cvdDown && netDown:
		return "增仓下压，空头主动增强"
	case oiDown && cvdUp:
		return "减仓反弹，偏空回补"
	case oiDown && cvdDown:
		return "减仓下跌，多头撤退"
	case netUp:
		return "估算净多增加"
	case netDown:
		return "估算净空增加"
	case math.Abs(w.CVDDelta) > 0:
		return "主动买卖有变化，持仓结构暂不明显"
	default:
		return "结构变化不明显"
	}
}
