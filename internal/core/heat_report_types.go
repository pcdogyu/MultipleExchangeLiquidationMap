package liqmap

type HeatReportBand struct {
	Band            int
	UpPrice         float64
	UpNotionalUSD   float64
	DownPrice       float64
	DownNotionalUSD float64
	DiffUSD         float64
	Highlight       bool
}

type HeatReportPeak struct {
	Price         float64
	SingleUSD     float64
	CumulativeUSD float64
	Distance      float64
}

type HeatReportData struct {
	GeneratedAt   int64
	CurrentPrice  float64
	Bands         []HeatReportBand
	LongTotalUSD  float64
	ShortTotalUSD float64
	LongPeak      HeatReportPeak
	ShortPeak     HeatReportPeak
}
