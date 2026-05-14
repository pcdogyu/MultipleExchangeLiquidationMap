package liqmap

type MarketState struct {
	Exchange       string   `json:"exchange"`
	Symbol         string   `json:"symbol"`
	MarkPrice      float64  `json:"mark_price"`
	OIQty          *float64 `json:"oi_qty,omitempty"`
	OIValueUSD     *float64 `json:"oi_value_usd,omitempty"`
	FundingRate    *float64 `json:"funding_rate,omitempty"`
	LongShortRatio *float64 `json:"long_short_ratio,omitempty"`
	UpdatedTS      int64    `json:"updated_ts"`
}

type BandRow struct {
	Band            int     `json:"band"`
	UpPrice         float64 `json:"up_price"`
	UpNotionalUSD   float64 `json:"up_notional_usd"`
	DownPrice       float64 `json:"down_price"`
	DownNotionalUSD float64 `json:"down_notional_usd"`
}

type ImbalanceStat struct {
	Band            int     `json:"band"`
	UpNotionalUSD   float64 `json:"up_notional_usd"`
	DownNotionalUSD float64 `json:"down_notional_usd"`
	Ratio           float64 `json:"ratio"`
	Verdict         string  `json:"verdict"`
}

type DensityLayer struct {
	Label           string  `json:"label"`
	UpNotionalUSD   float64 `json:"up_notional_usd"`
	DownNotionalUSD float64 `json:"down_notional_usd"`
}

type ChangeTracking struct {
	Up20DeltaUSD    float64 `json:"up20_delta_usd"`
	Down20DeltaUSD  float64 `json:"down20_delta_usd"`
	LongestDeltaUSD float64 `json:"longest_delta_usd"`
	FundingDelta    float64 `json:"funding_delta"`
}

type CoreZone struct {
	UpPrice            float64 `json:"up_price"`
	UpNotionalUSD      float64 `json:"up_notional_usd"`
	DownPrice          float64 `json:"down_price"`
	DownNotionalUSD    float64 `json:"down_notional_usd"`
	NearestSide        string  `json:"nearest_side"`
	NearestDistance    float64 `json:"nearest_distance"`
	NearestStrongPrice float64 `json:"nearest_strong_price"`
}

type ExchangeContribution struct {
	Exchange    string  `json:"exchange"`
	NotionalUSD float64 `json:"notional_usd"`
	Share       float64 `json:"share"`
}

type AlertSummary struct {
	Level              string  `json:"level"`
	RecentExchange     string  `json:"recent_exchange"`
	RecentSide         string  `json:"recent_side"`
	RecentPrice        float64 `json:"recent_price"`
	Recent1mUSD        float64 `json:"recent_1m_usd"`
	Recent1mBinanceUSD float64 `json:"recent_1m_binance_usd"`
	Recent1mBybitUSD   float64 `json:"recent_1m_bybit_usd"`
	Recent1mOKXUSD     float64 `json:"recent_1m_okx_usd"`
	Suggestion         string  `json:"suggestion"`
}

type TopConclusion struct {
	ShortBias   string  `json:"short_bias"`
	Bias20Delta float64 `json:"bias20_delta"`
	Bias50Label string  `json:"bias50_label"`
}

type MarketSummary struct {
	BinanceOIUSD      float64 `json:"binance_oi_usd"`
	OKXOIUSD          float64 `json:"okx_oi_usd"`
	BybitOIUSD        float64 `json:"bybit_oi_usd"`
	AvgFunding        float64 `json:"avg_funding"`
	AvgFundingVerdict string  `json:"avg_funding_verdict"`
}

type HeatZoneAnalytics struct {
	Top              TopConclusion          `json:"top"`
	Market           MarketSummary          `json:"market"`
	ImbalanceStats   []ImbalanceStat        `json:"imbalance_stats"`
	DensityLayers    []DensityLayer         `json:"density_layers"`
	ChangeTracking   ChangeTracking         `json:"change_tracking"`
	CoreZone         CoreZone               `json:"core_zone"`
	ExchangeContrib  []ExchangeContribution `json:"exchange_contrib"`
	DominantExchange string                 `json:"dominant_exchange"`
	Alert            AlertSummary           `json:"alert"`
}

type Dashboard struct {
	Symbol       string            `json:"symbol"`
	WindowDays   int               `json:"window_days"`
	GeneratedAt  int64             `json:"generated_at"`
	WindowCutoff int64             `json:"window_cutoff"`
	States       []MarketState     `json:"states"`
	CurrentPrice float64           `json:"current_price"`
	Bands        []BandRow         `json:"bands"`
	LongestShort []any             `json:"longest_short"`
	LongestLong  []any             `json:"longest_long"`
	Events       []EventRow        `json:"events"`
	Analytics    HeatZoneAnalytics `json:"analytics"`
}

type EventRow struct {
	Exchange    string  `json:"exchange"`
	Symbol      string  `json:"symbol,omitempty"`
	Side        string  `json:"side"`
	Price       float64 `json:"price"`
	Qty         float64 `json:"qty"`
	NotionalUSD float64 `json:"notional_usd"`
	EventTS     int64   `json:"event_ts"`
}

type PriceWallEvent struct {
	Side       string  `json:"side"`
	Price      float64 `json:"price"`
	Peak       float64 `json:"peak"`
	DurationMS int64   `json:"duration_ms"`
	EventTS    int64   `json:"event_ts"`
	Mode       string  `json:"mode"`
}
