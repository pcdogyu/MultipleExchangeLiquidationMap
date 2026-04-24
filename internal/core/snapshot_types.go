package liqmap

type Snapshot struct {
	Exchange       string
	Symbol         string
	MarkPrice      float64
	OIQty          float64
	OIValueUSD     float64
	FundingRate    float64
	LongShortRatio float64
	UpdatedTS      int64
}
