package analysis

import liqmap "multipleexchangeliquidationmap/internal/core"

type Services interface {
	AnalysisSnapshot() (liqmap.AnalysisSnapshot, error)
}

type service struct {
	core Services
}

func newService(core Services) *service {
	return &service{core: core}
}
