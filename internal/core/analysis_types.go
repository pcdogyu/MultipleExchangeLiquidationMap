package liqmap

type AnalysisOverview struct {
	Title      string  `json:"title"`
	Bias       string  `json:"bias"`
	Direction  string  `json:"direction"`
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

type AnalysisSignalRecord struct {
	ID                int64   `json:"id"`
	SignalTS          int64   `json:"signal_ts"`
	Symbol            string  `json:"symbol"`
	SourceGroup       int     `json:"source_group"`
	Direction         string  `json:"direction"`
	Confidence        float64 `json:"confidence"`
	SignalPrice       float64 `json:"signal_price"`
	AnalysisGenerated int64   `json:"analysis_generated_at"`
	Headline          string  `json:"headline"`
	Summary           string  `json:"summary"`
	VerifyHorizonMin  int     `json:"verify_horizon_min"`
}

type AnalysisSignalResult struct {
	ID                int64                         `json:"id"`
	SignalTS          int64                         `json:"signal_ts"`
	Symbol            string                        `json:"symbol"`
	SourceGroup       int                           `json:"source_group"`
	Direction         string                        `json:"direction"`
	Confidence        float64                       `json:"confidence"`
	SignalPrice       float64                       `json:"signal_price"`
	AnalysisGenerated int64                         `json:"analysis_generated_at"`
	Headline          string                        `json:"headline"`
	Summary           string                        `json:"summary"`
	VerifyHorizonMin  int                           `json:"verify_horizon_min"`
	SecondFactorKey   string                        `json:"second_factor_key,omitempty"`
	SecondFactorLabel string                        `json:"second_factor_label,omitempty"`
	VerifyDueTS       int64                         `json:"verify_due_ts"`
	VerifyClosePrice  float64                       `json:"verify_close_price,omitempty"`
	Result            string                        `json:"result"`
	DeltaPrice        float64                       `json:"delta_price,omitempty"`
	DeltaPct          float64                       `json:"delta_pct,omitempty"`
	Horizons          []AnalysisSignalHorizonResult `json:"horizons,omitempty"`
}

type AnalysisSignalHorizonResult struct {
	HorizonMin       int     `json:"horizon_min"`
	VerifyDueTS      int64   `json:"verify_due_ts"`
	VerifyClosePrice float64 `json:"verify_close_price,omitempty"`
	Result           string  `json:"result"`
	DeltaPrice       float64 `json:"delta_price,omitempty"`
	DeltaPct         float64 `json:"delta_pct,omitempty"`
}

type AnalysisBacktestSummary struct {
	WindowHours  int     `json:"window_hours"`
	TotalSignals int     `json:"total_signals"`
	CorrectCount int     `json:"correct_count"`
	WrongCount   int     `json:"wrong_count"`
	PendingCount int     `json:"pending_count"`
	NoDataCount  int     `json:"no_data_count"`
	CorrectRate  float64 `json:"correct_rate"`
}

type AnalysisBacktestHorizonStat struct {
	HorizonMin   int     `json:"horizon_min"`
	Label        string  `json:"label"`
	TotalSignals int     `json:"total_signals"`
	CorrectCount int     `json:"correct_count"`
	WrongCount   int     `json:"wrong_count"`
	PendingCount int     `json:"pending_count"`
	NoDataCount  int     `json:"no_data_count"`
	CorrectRate  float64 `json:"correct_rate"`
}

type AnalysisBacktestConfidenceBucket struct {
	Label        string  `json:"label"`
	MinInclusive float64 `json:"min_inclusive"`
	MaxExclusive float64 `json:"max_exclusive,omitempty"`
	TotalSignals int     `json:"total_signals"`
	CorrectCount int     `json:"correct_count"`
	WrongCount   int     `json:"wrong_count"`
	PendingCount int     `json:"pending_count"`
	NoDataCount  int     `json:"no_data_count"`
	CorrectRate  float64 `json:"correct_rate"`
}

type AnalysisBacktestConfidenceFactorBucket struct {
	Label        string  `json:"label"`
	MinInclusive float64 `json:"min_inclusive"`
	MaxExclusive float64 `json:"max_exclusive,omitempty"`
	FactorKey    string  `json:"factor_key"`
	FactorLabel  string  `json:"factor_label"`
	TotalSignals int     `json:"total_signals"`
	CorrectCount int     `json:"correct_count"`
	WrongCount   int     `json:"wrong_count"`
	PendingCount int     `json:"pending_count"`
	NoDataCount  int     `json:"no_data_count"`
	CorrectRate  float64 `json:"correct_rate"`
}

type AnalysisBacktestStrategyGroup struct {
	FactorKey             string  `json:"factor_key"`
	FactorLabel           string  `json:"factor_label"`
	Label                 string  `json:"label"`
	MinInclusive          float64 `json:"min_inclusive"`
	MaxExclusive          float64 `json:"max_exclusive,omitempty"`
	TotalSignals          int     `json:"total_signals"`
	SampleCount           int     `json:"sample_count"`
	FiveMinuteCorrectRate float64 `json:"five_minute_correct_rate"`
	CompositeScore        float64 `json:"composite_score"`
	Selected              bool    `json:"selected"`
	Reason                string  `json:"reason"`
}

type AnalysisBacktestPageResponse struct {
	Candles           []map[string]any                   `json:"candles"`
	Signals           []AnalysisSignalResult             `json:"signals"`
	Summary           AnalysisBacktestSummary            `json:"summary"`
	HorizonStats      []AnalysisBacktestHorizonStat      `json:"horizon_stats"`
	ConfidenceBuckets []AnalysisBacktestConfidenceBucket `json:"confidence_buckets,omitempty"`
	QualityMode       string                             `json:"quality_mode,omitempty"`
	ChartSource       string                             `json:"chart_source,omitempty"`
	ChartInterval     string                             `json:"chart_interval,omitempty"`
}

type AnalysisBacktestHistoryResponse struct {
	Page    int                    `json:"page"`
	Limit   int                    `json:"limit"`
	Signals []AnalysisSignalResult `json:"signals"`
}

type AnalysisBacktest2FAFactorSummary struct {
	Key          string  `json:"key"`
	Label        string  `json:"label"`
	TotalSignals int     `json:"total_signals"`
	CorrectCount int     `json:"correct_count"`
	WrongCount   int     `json:"wrong_count"`
	PendingCount int     `json:"pending_count"`
	NoDataCount  int     `json:"no_data_count"`
	CorrectRate  float64 `json:"correct_rate"`
}

type AnalysisBacktest2FAResponse struct {
	Candles                 []map[string]any                         `json:"candles"`
	Signals                 []AnalysisSignalResult                   `json:"signals"`
	Summary                 AnalysisBacktestSummary                  `json:"summary"`
	HorizonStats            []AnalysisBacktestHorizonStat            `json:"horizon_stats"`
	ConfidenceBuckets       []AnalysisBacktestConfidenceBucket       `json:"confidence_buckets,omitempty"`
	ConfidenceFactorBuckets []AnalysisBacktestConfidenceFactorBucket `json:"confidence_factor_buckets,omitempty"`
	StrategyMode            string                                   `json:"strategy_mode"`
	StrategyActive          bool                                     `json:"strategy_active"`
	StrategyGroups          []AnalysisBacktestStrategyGroup          `json:"strategy_groups,omitempty"`
	SelectedFactor          string                                   `json:"selected_factor"`
	FactorOptions           []AnalysisBacktest2FAFactorSummary       `json:"factor_options"`
	ChartSource             string                                   `json:"chart_source,omitempty"`
	ChartInterval           string                                   `json:"chart_interval,omitempty"`
}
