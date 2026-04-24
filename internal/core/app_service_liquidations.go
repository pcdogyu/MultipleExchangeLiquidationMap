package liqmap

func (a *App) ListLiquidations(limit, offset int, startTS, endTS int64) []EventRow {
	return a.loadLiquidations(defaultSymbol, limit, offset, startTS, endTS)
}
