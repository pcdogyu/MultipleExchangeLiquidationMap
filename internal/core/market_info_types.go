package liqmap

type MarketInfoPoint struct {
	TS    int64   `json:"ts"`
	Value float64 `json:"value"`
}

type MarketInfoOIPoint struct {
	TS         int64   `json:"ts"`
	OIQty      float64 `json:"oi_qty"`
	OIValueUSD float64 `json:"oi_value_usd"`
}

type MarketInfoRatioPoint struct {
	TS          int64   `json:"ts"`
	Ratio       float64 `json:"ratio"`
	LongShare   float64 `json:"long_share"`
	ShortShare  float64 `json:"short_share"`
	SourceLabel string  `json:"source_label"`
}

type MarketInfoTakerPoint struct {
	TS           int64   `json:"ts"`
	BuyVol       float64 `json:"buy_vol"`
	SellVol      float64 `json:"sell_vol"`
	BuySellRatio float64 `json:"buy_sell_ratio"`
	Delta        float64 `json:"delta"`
	CVD          float64 `json:"cvd"`
}

type MarketInfoCurrent struct {
	MarkPrice          float64  `json:"mark_price"`
	PriceChangePct24h  float64  `json:"price_change_pct_24h,omitempty"`
	QuoteVolume24h     float64  `json:"quote_volume_24h,omitempty"`
	High24h            float64  `json:"high_24h,omitempty"`
	Low24h             float64  `json:"low_24h,omitempty"`
	BidPrice           float64  `json:"bid_price,omitempty"`
	AskPrice           float64  `json:"ask_price,omitempty"`
	BidAskSpread       float64  `json:"bid_ask_spread,omitempty"`
	BidAskSpreadPct    float64  `json:"bid_ask_spread_pct,omitempty"`
	OIQty              float64  `json:"oi_qty"`
	OIValueUSD         float64  `json:"oi_value_usd"`
	FundingRate        *float64 `json:"funding_rate,omitempty"`
	LongShortRatio     *float64 `json:"long_short_ratio,omitempty"`
	TopPositionRatio   *float64 `json:"top_position_ratio,omitempty"`
	TakerBuyVol        float64  `json:"taker_buy_vol"`
	TakerSellVol       float64  `json:"taker_sell_vol"`
	TakerBuySellRatio  float64  `json:"taker_buy_sell_ratio"`
	CVD                float64  `json:"cvd"`
	NetPositionUSD     float64  `json:"net_position_usd"`
	NetPositionBasis   string   `json:"net_position_basis"`
	NetPositionLabel   string   `json:"net_position_label"`
	UpdatedTS          int64    `json:"updated_ts"`
	NextFundingTime    int64    `json:"next_funding_time,omitempty"`
	EstimatedSettle    float64  `json:"estimated_settle_price,omitempty"`
	CurrentDataQuality string   `json:"current_data_quality"`
}

type MarketInfoWindow struct {
	Label               string  `json:"label"`
	Minutes             int     `json:"minutes"`
	OIDeltaUSD          float64 `json:"oi_delta_usd"`
	OIDeltaPct          float64 `json:"oi_delta_pct"`
	LongShortDelta      float64 `json:"long_short_delta"`
	TopPositionDelta    float64 `json:"top_position_delta"`
	CVDDelta            float64 `json:"cvd_delta"`
	TakerDelta          float64 `json:"taker_delta"`
	NetPositionDeltaUSD float64 `json:"net_position_delta_usd"`
	Verdict             string  `json:"verdict"`
}

type MarketInfoSeries struct {
	OI          []MarketInfoOIPoint    `json:"oi"`
	LongShort   []MarketInfoRatioPoint `json:"long_short"`
	TopPosition []MarketInfoRatioPoint `json:"top_position"`
	Taker       []MarketInfoTakerPoint `json:"taker"`
}

type MarketInfoGammaStatus struct {
	Status            string                  `json:"status"`
	Label             string                  `json:"label"`
	Message           string                  `json:"message"`
	Method            string                  `json:"method,omitempty"`
	Underlying        string                  `json:"underlying,omitempty"`
	SpotPrice         float64                 `json:"spot_price,omitempty"`
	UpdatedTS         int64                   `json:"updated_ts,omitempty"`
	Contracts         int                     `json:"contracts,omitempty"`
	Expirations       int                     `json:"expirations,omitempty"`
	TotalOI           float64                 `json:"total_oi,omitempty"`
	CallOI            float64                 `json:"call_oi,omitempty"`
	PutOI             float64                 `json:"put_oi,omitempty"`
	NetGEXUSD         float64                 `json:"net_gex_usd,omitempty"`
	AbsGEXUSD         float64                 `json:"abs_gex_usd,omitempty"`
	CallGEXUSD        float64                 `json:"call_gex_usd,omitempty"`
	PutGEXUSD         float64                 `json:"put_gex_usd,omitempty"`
	MaxPositiveStrike float64                 `json:"max_positive_strike,omitempty"`
	MaxNegativeStrike float64                 `json:"max_negative_strike,omitempty"`
	GammaWallStrike   float64                 `json:"gamma_wall_strike,omitempty"`
	Levels            []MarketInfoGammaLevel  `json:"levels,omitempty"`
	Expiries          []MarketInfoGammaExpiry `json:"expiries,omitempty"`
}

type MarketInfoGammaLevel struct {
	Strike    float64 `json:"strike"`
	CallOI    float64 `json:"call_oi"`
	PutOI     float64 `json:"put_oi"`
	CallGEX   float64 `json:"call_gex_usd"`
	PutGEX    float64 `json:"put_gex_usd"`
	NetGEX    float64 `json:"net_gex_usd"`
	AbsGEX    float64 `json:"abs_gex_usd"`
	Contracts int     `json:"contracts"`
}

type MarketInfoGammaExpiry struct {
	Expiration string  `json:"expiration"`
	ExpiryTS   int64   `json:"expiry_ts"`
	CallOI     float64 `json:"call_oi"`
	PutOI      float64 `json:"put_oi"`
	CallGEX    float64 `json:"call_gex_usd"`
	PutGEX     float64 `json:"put_gex_usd"`
	NetGEX     float64 `json:"net_gex_usd"`
	AbsGEX     float64 `json:"abs_gex_usd"`
	Contracts  int     `json:"contracts"`
}

type MarketInfoResponse struct {
	Exchange       string                `json:"exchange"`
	Symbol         string                `json:"symbol"`
	Period         string                `json:"period"`
	Limit          int                   `json:"limit"`
	GeneratedAt    int64                 `json:"generated_at"`
	CacheStatus    string                `json:"cache_status,omitempty"`
	CacheUpdatedAt int64                 `json:"cache_updated_at,omitempty"`
	Refreshing     bool                  `json:"refreshing,omitempty"`
	Current        MarketInfoCurrent     `json:"current"`
	Windows        []MarketInfoWindow    `json:"windows"`
	Series         MarketInfoSeries      `json:"series"`
	Gamma          MarketInfoGammaStatus `json:"gamma"`
	Warnings       []string              `json:"warnings,omitempty"`
}
