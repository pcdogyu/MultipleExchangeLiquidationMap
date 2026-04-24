package liqmap

type ModelConfig struct {
	LookbackMin           int
	BucketMin             int
	PriceStep             float64
	PriceRange            float64
	LeverageCSV           string
	WeightCSV             string
	WeightCSVBinance      string
	WeightCSVOKX          string
	WeightCSVBybit        string
	MaintMargin           float64
	MaintMarginCSV        string
	MaintMarginCSVBinance string
	MaintMarginCSVOKX     string
	MaintMarginCSVBybit   string
	FundingScale          float64
	FundingScaleCSV       string
	IntensityScale        float64
	DecayK                float64
	NeighborShare         float64
}
