package liqmap

type AnalysisModuleServices struct {
	app *App
}

func NewAnalysisModuleServices(app *App) *AnalysisModuleServices {
	return &AnalysisModuleServices{app: app}
}

func (s *AnalysisModuleServices) AnalysisSnapshot() (AnalysisSnapshot, error) {
	return s.app.buildAnalysisSnapshot()
}
