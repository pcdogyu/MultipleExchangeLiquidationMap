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

func (s *analysisModuleAdapter) AnalysisBacktest(hours int, interval string, minConfidence float64) (liqmap.AnalysisBacktestPageResponse, error) {
	return s.app.AnalysisBacktest(hours, interval, minConfidence)
}

func (s *analysisModuleAdapter) AnalysisBacktest2FA(hours int, interval string, factor string, minConfidence float64) (liqmap.AnalysisBacktest2FAResponse, error) {
	return s.app.AnalysisBacktest2FA(hours, interval, factor, minConfidence)
}

func (s *analysisModuleAdapter) AnalysisBacktestHistory(limit, page int) (liqmap.AnalysisBacktestHistoryResponse, error) {
	return s.app.AnalysisBacktestHistory(limit, page)
}
