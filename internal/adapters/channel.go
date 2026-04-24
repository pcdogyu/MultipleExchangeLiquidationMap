package adapters

import (
	liqmap "multipleexchangeliquidationmap/internal/core"
	"multipleexchangeliquidationmap/internal/modules/channel"
)

type channelModuleAdapter struct {
	app *liqmap.App
}

func NewChannel(app *liqmap.App) channel.Services {
	return &channelModuleAdapter{app: app}
}

func (s *channelModuleAdapter) LoadSettings() liqmap.ChannelSettings {
	return s.app.LoadSettings()
}

func (s *channelModuleAdapter) SaveSettings(req liqmap.ChannelSettings) error {
	return s.app.SaveSettings(req)
}

func (s *channelModuleAdapter) TriggerChannelTestSend() (string, bool) {
	return s.app.TriggerChannelTestSend()
}

func (s *channelModuleAdapter) ListTelegramSendHistory(limit int) ([]liqmap.TelegramSendHistoryRow, error) {
	return s.app.ListTelegramSendHistory(limit)
}

func (s *channelModuleAdapter) ListChannelTimeline(hours int) ([]liqmap.ChannelTimelineRow, error) {
	return s.app.ListChannelTimeline(hours)
}

func (s *channelModuleAdapter) ListChannelPlannedPushes(hours int) []liqmap.ChannelPlannedPushRow {
	return s.app.ListChannelPlannedPushes(hours)
}
