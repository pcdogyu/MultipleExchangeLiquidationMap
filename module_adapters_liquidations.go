package liqmap

type LiquidationsModuleAdapter struct {
	app *App
}

func NewLiquidationsModuleAdapter(app *App) *LiquidationsModuleAdapter {
	return &LiquidationsModuleAdapter{app: app}
}

func (s *LiquidationsModuleAdapter) ListLiquidations(limit, offset int, startTS, endTS int64) []EventRow {
	return s.app.loadLiquidations(defaultSymbol, limit, offset, startTS, endTS)
}
