package app

import liqmap "multipleexchangeliquidationmap"

type Dependencies struct {
	Core  *liqmap.App
	Debug bool
}

func NewDependencies(core *liqmap.App, debug bool) *Dependencies {
	return &Dependencies{
		Core:  core,
		Debug: debug,
	}
}
