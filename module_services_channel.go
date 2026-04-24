package liqmap

type ChannelModuleServices struct {
	app *App
}

func NewChannelModuleServices(app *App) *ChannelModuleServices {
	return &ChannelModuleServices{app: app}
}

func (s *ChannelModuleServices) LoadSettings() ChannelSettings {
	return s.app.loadSettings()
}

func (s *ChannelModuleServices) SaveSettings(req ChannelSettings) error {
	return s.app.saveSettings(req)
}

func (s *ChannelModuleServices) TriggerChannelTestSend() (string, bool) {
	return s.app.triggerTelegramTestSend()
}

func (s *ChannelModuleServices) ListTelegramSendHistory(limit int) ([]TelegramSendHistoryRow, error) {
	return s.app.listTelegramSendHistory(limit)
}

func (s *ChannelModuleServices) ListChannelTimeline(hours int) ([]ChannelTimelineRow, error) {
	return s.app.listChannelTimeline(hours)
}

func (s *ChannelModuleServices) ListChannelPlannedPushes(hours int) []ChannelPlannedPushRow {
	return s.app.listChannelPlannedPushes(hours)
}
