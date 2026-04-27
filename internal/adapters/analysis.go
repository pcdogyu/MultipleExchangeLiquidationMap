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

func (s *analysisModuleAdapter) AnalysisBacktest(hours int, minConfidence float64, horizons []int) (liqmap.AnalysisBacktestPageResponse, error) {
	return s.app.AnalysisBacktest(hours, minConfidence, horizons)
}

func (s *analysisModuleAdapter) AnalysisBacktestHistory(limit, page int) (liqmap.AnalysisBacktestHistoryResponse, error) {
	return s.app.AnalysisBacktestHistory(limit, page)
}
