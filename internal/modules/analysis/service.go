package analysis

import liqmap "multipleexchangeliquidationmap/internal/core"

type Services interface {
	AnalysisSnapshot() (liqmap.AnalysisSnapshot, error)
	AnalysisBacktest(hours int, interval string, minConfidence float64) (liqmap.AnalysisBacktestPageResponse, error)
	AnalysisBacktest2FA(hours int, interval string, factor string, minConfidence float64) (liqmap.AnalysisBacktest2FAResponse, error)
	AnalysisBacktestHistory(limit, page int) (liqmap.AnalysisBacktestHistoryResponse, error)
}

type service struct {
	core Services
}

func newService(core Services) *service {
	return &service{core: core}
}
