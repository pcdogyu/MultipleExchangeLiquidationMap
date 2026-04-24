package analysis

import liqmap "multipleexchangeliquidationmap"

type Services interface {
	AnalysisSnapshot() (liqmap.AnalysisSnapshot, error)
}

type service struct {
	core Services
}

func newService(core Services) *service {
	return &service{core: core}
}
