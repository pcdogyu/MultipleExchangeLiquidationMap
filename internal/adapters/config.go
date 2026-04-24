package adapters

import (
	liqmap "multipleexchangeliquidationmap"
	"multipleexchangeliquidationmap/internal/modules/config"
)

type configModuleAdapter struct {
	app *liqmap.App
}

func NewConfig(app *liqmap.App) config.Services {
	return &configModuleAdapter{app: app}
}

func (s *configModuleAdapter) LoadModelConfig() liqmap.ModelConfig {
	return s.app.LoadModelConfig()
}

func (s *configModuleAdapter) SaveModelConfig(req liqmap.ModelConfig) error {
	return s.app.SaveModelConfig(req)
}

func (s *configModuleAdapter) RunModelFit(hours, minEvents int, exchange, mode string) (map[string]any, error) {
	return s.app.RunModelFit(hours, minEvents, exchange, mode)
}
