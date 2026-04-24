package liqmap

type ConfigModuleAdapter struct {
	app *App
}

func NewConfigModuleAdapter(app *App) *ConfigModuleAdapter {
	return &ConfigModuleAdapter{app: app}
}

func (s *ConfigModuleAdapter) LoadModelConfig() ModelConfig {
	return s.app.loadModelConfig()
}

func (s *ConfigModuleAdapter) SaveModelConfig(req ModelConfig) error {
	return s.app.saveModelConfig(req)
}

func (s *ConfigModuleAdapter) RunModelFit(hours, minEvents int, exchange, mode string) (map[string]any, error) {
	return s.app.runModelFit(hours, minEvents, exchange, mode)
}
