package analysis

import liqmap "multipleexchangeliquidationmap/internal/core"

type Services interface {
	AnalysisSnapshot() (liqmap.AnalysisSnapshot, error)
	AnalysisBacktest(hours int, minConfidence float64, horizons []int) (liqmap.AnalysisBacktestPageResponse, error)
	AnalysisBacktestHistory(limit, page int) (liqmap.AnalysisBacktestHistoryResponse, error)
}

type service struct {
	core Services
}

func newService(core Services) *service {
	return &service{core: core}
}
