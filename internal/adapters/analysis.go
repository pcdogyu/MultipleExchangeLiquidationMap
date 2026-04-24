package adapters

import (
	liqmap "multipleexchangeliquidationmap/internal/core"
	"multipleexchangeliquidationmap/internal/modules/analysis"
)

type analysisModuleAdapter struct {
	app *liqmap.App
}

func NewAnalysis(app *liqmap.App) analysis.Services {
	return &analysisModuleAdapter{app: app}
}

func (s *analysisModuleAdapter) AnalysisSnapshot() (liqmap.AnalysisSnapshot, error) {
	return s.app.BuildAnalysisSnapshot()
}
