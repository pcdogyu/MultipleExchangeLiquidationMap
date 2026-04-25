package liqmap

type AnalysisOverview struct {
	Title      string  `json:"title"`
	Bias       string  `json:"bias"`
	Confidence float64 `json:"confidence"`
	Summary    string  `json:"summary"`
}

type AnalysisRiskScore struct {
	Label string  `json:"label"`
	Score float64 `json:"score"`
	Tone  string  `json:"tone"`
}

type AnalysisKeyZone struct {
	Name        string  `json:"name"`
	Side        string  `json:"side"`
	Price       float64 `json:"price"`
	Distance    float64 `json:"distance"`
	NotionalUSD float64 `json:"notional_usd"`
	Note        string  `json:"note"`
}

type AnalysisDelta struct {
	Label         string                `json:"label"`
	Value         float64               `json:"value"`
	Unit          string                `json:"unit"`
	Tone          string                `json:"tone"`
	Subvalue      string                `json:"subvalue,omitempty"`
	PushDirection string                `json:"push_direction,omitempty"`
	Note          string                `json:"note"`
	Series        []AnalysisSeriesPoint `json:"series,omitempty"`
}

type AnalysisSeriesPoint struct {
	TS    int64   `json:"ts"`
	Value float64 `json:"value"`
}

type AnalysisBacktest struct {
	SampleCount   int     `json:"sample_count"`
	HorizonMin    int     `json:"horizon_min"`
	Hit20Rate     float64 `json:"hit_20_rate"`
	Hit50Rate     float64 `json:"hit_50_rate"`
	Hit100Rate    float64 `json:"hit_100_rate"`
	AvgMaxMove    float64 `json:"avg_max_move"`
	FitEventCount int     `json:"fit_event_count"`
	FitWeightedSE float64 `json:"fit_weighted_se"`
	Source        string  `json:"source"`
	Summary       string  `json:"summary"`
}

type AnalysisBroadcast struct {
	Headline string   `json:"headline"`
	Text     string   `json:"text"`
	Bullets  []string `json:"bullets"`
}

type AnalysisIndicator struct {
	Label    string `json:"label"`
	Value    string `json:"value"`
	Subvalue string `json:"subvalue,omitempty"`
	Tone     string `json:"tone"`
	Note     string `json:"note"`
}

type ExchangeAnalysisCard struct {
	Exchange        string  `json:"exchange"`
	MarkPrice       float64 `json:"mark_price"`
	OIValueUSD      float64 `json:"oi_value_usd"`
	FundingRate     float64 `json:"funding_rate"`
	LongShortRatio  float64 `json:"long_short_ratio"`
	UpperRiskUSD    float64 `json:"upper_risk_usd"`
	LowerRiskUSD    float64 `json:"lower_risk_usd"`
	ShortRiskScore  float64 `json:"short_risk_score"`
	LongRiskScore   float64 `json:"long_risk_score"`
	Bias            string  `json:"bias"`
	RecentLiquidUSD float64 `json:"recent_liquid_usd"`
	FocusPrice      float64 `json:"focus_price"`
	FocusSide       string  `json:"focus_side"`
	Summary         string  `json:"summary"`
}

type AnalysisSnapshot struct {
	Symbol        string                 `json:"symbol"`
	GeneratedAt   int64                  `json:"generated_at"`
	CurrentPrice  float64                `json:"current_price"`
	Overview      AnalysisOverview       `json:"overview"`
	Broadcast     AnalysisBroadcast      `json:"broadcast"`
	Indicators    []AnalysisIndicator    `json:"indicators"`
	RiskScores    []AnalysisRiskScore    `json:"risk_scores"`
	KeyZones      []AnalysisKeyZone      `json:"key_zones"`
	Changes       []AnalysisDelta        `json:"changes"`
	ExchangeCards []ExchangeAnalysisCard `json:"exchange_cards"`
	Backtest      AnalysisBacktest       `json:"backtest"`
	Dashboard     Dashboard              `json:"dashboard"`
}
