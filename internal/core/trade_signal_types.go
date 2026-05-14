package liqmap

type TradeSignal struct {
	TS       int64   `json:"ts"`
	Side     string  `json:"side"`
	Action   string  `json:"action"`
	Price    float64 `json:"price"`
	Label    string  `json:"label"`
	Reason   string  `json:"reason"`
	Strength float64 `json:"strength"`
}

type TradeSignalOptions struct {
	StartTS  int64
	EndTS    int64
	Symbol   string
	Interval string
}
