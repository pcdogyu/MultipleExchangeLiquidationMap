package liqmap

type AnalysisModuleAdapter struct {
	app *App
}

func NewAnalysisModuleAdapter(app *App) *AnalysisModuleAdapter {
	return &AnalysisModuleAdapter{app: app}
}

func (s *AnalysisModuleAdapter) AnalysisSnapshot() (AnalysisSnapshot, error) {
	return s.app.buildAnalysisSnapshot()
}
