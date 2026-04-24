package liqmap

type ChannelModuleAdapter struct {
	app *App
}

func NewChannelModuleAdapter(app *App) *ChannelModuleAdapter {
	return &ChannelModuleAdapter{app: app}
}

func (s *ChannelModuleAdapter) LoadSettings() ChannelSettings {
	return s.app.loadSettings()
}

func (s *ChannelModuleAdapter) SaveSettings(req ChannelSettings) error {
	return s.app.saveSettings(req)
}

func (s *ChannelModuleAdapter) TriggerChannelTestSend() (string, bool) {
	return s.app.triggerTelegramTestSend()
}

func (s *ChannelModuleAdapter) ListTelegramSendHistory(limit int) ([]TelegramSendHistoryRow, error) {
	return s.app.listTelegramSendHistory(limit)
}

func (s *ChannelModuleAdapter) ListChannelTimeline(hours int) ([]ChannelTimelineRow, error) {
	return s.app.listChannelTimeline(hours)
}

func (s *ChannelModuleAdapter) ListChannelPlannedPushes(hours int) []ChannelPlannedPushRow {
	return s.app.listChannelPlannedPushes(hours)
}
