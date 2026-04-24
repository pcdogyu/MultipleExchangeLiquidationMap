package liqmap

func (a *App) AnalysisSnapshot() (AnalysisSnapshot, error) {
	return a.buildAnalysisSnapshot()
}
