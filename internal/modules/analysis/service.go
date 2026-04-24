package analysis

import liqmap "multipleexchangeliquidationmap"

type snapshotProvider interface {
	AnalysisSnapshot() (liqmap.AnalysisSnapshot, error)
}

type service struct {
	core snapshotProvider
}

func newService(core snapshotProvider) *service {
	return &service{core: core}
}
