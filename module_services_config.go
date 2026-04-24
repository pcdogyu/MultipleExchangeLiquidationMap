package liqmap

type ConfigModuleServices struct {
	app *App
}

func NewConfigModuleServices(app *App) *ConfigModuleServices {
	return &ConfigModuleServices{app: app}
}

func (s *ConfigModuleServices) LoadModelConfig() ModelConfig {
	return s.app.loadModelConfig()
}

func (s *ConfigModuleServices) SaveModelConfig(req ModelConfig) error {
	return s.app.saveModelConfig(req)
}

func (s *ConfigModuleServices) RunModelFit(hours, minEvents int, exchange, mode string) (map[string]any, error) {
	return s.app.runModelFit(hours, minEvents, exchange, mode)
}
