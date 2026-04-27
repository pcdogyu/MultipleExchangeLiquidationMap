package liqmap

type LiquidationListOptions struct {
	Limit       int
	Offset      int
	StartTS     int64
	EndTS       int64
	Symbol      string
	FilterField string
	MinValue    float64
}

type LiquidationWSStatus struct {
	Exchange         string `json:"exchange"`
	Connected        bool   `json:"connected"`
	ActiveConns      int    `json:"active_conns"`
	SubscribedTopics int    `json:"subscribed_topics"`
	Mode             string `json:"mode,omitempty"`
	LastEventTS      int64  `json:"last_event_ts"`
	LastConnectTS    int64  `json:"last_connect_ts"`
	LastDisconnectTS int64  `json:"last_disconnect_ts"`
	LastError        string `json:"last_error,omitempty"`
	PausedUntil      int64  `json:"paused_until,omitempty"`
	PauseReason      string `json:"pause_reason,omitempty"`
}

type LiquidationPeriodBucket struct {
	Label          string  `json:"label"`
	Hours          int     `json:"hours"`
	TotalUSD       float64 `json:"total_usd"`
	LongUSD        float64 `json:"long_usd"`
	ShortUSD       float64 `json:"short_usd"`
	DominantSide   string  `json:"dominant_side"`
	PricePush      string  `json:"price_push"`
	PricePushLabel string  `json:"price_push_label"`
	BalanceRatio   float64 `json:"balance_ratio"`
}

type LiquidationPeriodPattern struct {
	Code         string  `json:"code"`
	Label        string  `json:"label"`
	TrendBias    string  `json:"trend_bias"`
	Score        float64 `json:"score"`
	Summary      string  `json:"summary"`
	PatternCount int     `json:"pattern_count"`
}

type LiquidationSyncSignal struct {
	Active          bool    `json:"active"`
	Direction       string  `json:"direction"`
	Label           string  `json:"label"`
	Reason          string  `json:"reason"`
	FirstPeriod     string  `json:"first_period"`
	SecondPeriod    string  `json:"second_period"`
	ThresholdRatio  float64 `json:"threshold_ratio"`
	FirstEffective  string  `json:"first_effective"`
	SecondEffective string  `json:"second_effective"`
}

type LiquidationSyncSignals struct {
	ShortTermSync    LiquidationSyncSignal `json:"short_term_sync"`
	Continuation     LiquidationSyncSignal `json:"continuation_sync"`
	OverallAlignment string                `json:"overall_alignment"`
	OverallLabel     string                `json:"overall_label"`
	OverallReason    string                `json:"overall_reason"`
	ThresholdRatio   float64               `json:"threshold_ratio"`
}

type LiquidationPeriodSummary struct {
	Buckets     []LiquidationPeriodBucket `json:"buckets"`
	Pattern     LiquidationPeriodPattern  `json:"pattern"`
	SyncSignals LiquidationSyncSignals    `json:"sync_signals"`
}

type liquidationWSState struct {
	Exchange         string
	ActiveConns      int
	SubscribedTopics int
	Mode             string
	LastEventTS      int64
	LastConnectTS    int64
	LastDisconnectTS int64
	LastError        string
}
