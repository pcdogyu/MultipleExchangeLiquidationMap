package monitor

import "multipleexchangeliquidationmap/internal/appctx"

type service struct {
	deps *appctx.Dependencies
}

func newService(deps *appctx.Dependencies) *service {
	return &service{deps: deps}
}
