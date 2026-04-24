package liqmap

type LiquidationsModuleServices struct {
	app *App
}

func NewLiquidationsModuleServices(app *App) *LiquidationsModuleServices {
	return &LiquidationsModuleServices{app: app}
}

func (s *LiquidationsModuleServices) ListLiquidations(limit, offset int, startTS, endTS int64) []EventRow {
	return s.app.loadLiquidations(defaultSymbol, limit, offset, startTS, endTS)
}
