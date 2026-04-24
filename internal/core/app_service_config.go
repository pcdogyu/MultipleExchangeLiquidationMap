package liqmap

func (a *App) LoadModelConfig() ModelConfig {
	return a.loadModelConfig()
}

func (a *App) SaveModelConfig(req ModelConfig) error {
	return a.saveModelConfig(req)
}

func (a *App) RunModelFit(hours, minEvents int, exchange, mode string) (map[string]any, error) {
	return a.runModelFit(hours, minEvents, exchange, mode)
}
