package liqmap

func (a *App) LoadSettings() ChannelSettings {
	return a.loadSettings()
}

func (a *App) SaveSettings(req ChannelSettings) error {
	return a.saveSettings(req)
}

func (a *App) TriggerChannelTestSend() (string, bool) {
	return a.triggerTelegramTestSend()
}

func (a *App) ListTelegramSendHistory(limit int) ([]TelegramSendHistoryRow, error) {
	return a.listTelegramSendHistory(limit)
}

func (a *App) ListChannelTimeline(hours int) ([]ChannelTimelineRow, error) {
	return a.listChannelTimeline(hours)
}

func (a *App) ListChannelPlannedPushes(hours int) []ChannelPlannedPushRow {
	return a.listChannelPlannedPushes(hours)
}

func (a *App) LoadModelConfig() ModelConfig {
	return a.loadModelConfig()
}

func (a *App) SaveModelConfig(req ModelConfig) error {
	return a.saveModelConfig(req)
}

func (a *App) RunModelFit(hours, minEvents int, exchange, mode string) (map[string]any, error) {
	return a.runModelFit(hours, minEvents, exchange, mode)
}
