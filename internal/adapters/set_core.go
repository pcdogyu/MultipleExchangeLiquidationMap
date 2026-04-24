package adapters

import (
	liqmap "multipleexchangeliquidationmap"
	"multipleexchangeliquidationmap/internal/modules/analysis"
	"multipleexchangeliquidationmap/internal/modules/home"
)

type CoreSet struct {
	Home     home.Services
	Analysis analysis.Services
}

func NewCore(app *liqmap.App) CoreSet {
	return CoreSet{
		Home:     NewHome(app),
		Analysis: NewAnalysis(app),
	}
}
