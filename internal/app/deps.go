package app

import (
	liqmap "multipleexchangeliquidationmap"
	"multipleexchangeliquidationmap/internal/appctx"
)

type Dependencies = appctx.Dependencies

func NewDependencies(core *liqmap.App, debug bool) *Dependencies {
	return &appctx.Dependencies{
		Core:  core,
		Debug: debug,
	}
}
