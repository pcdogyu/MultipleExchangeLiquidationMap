package liqmap

type ChannelSettings struct {
	TelegramBotToken      string `json:"telegram_bot_token"`
	TelegramChannel       string `json:"telegram_channel"`
	NotifyIntervalMin     int    `json:"notify_interval_min"`
	NotifyWorkIntervalMin int    `json:"notify_work_interval_min"`
	NotifyOffIntervalMin  int    `json:"notify_off_interval_min"`
	WorkTimeExpr          string `json:"work_time_expr"`
	NotifyEnabled         bool   `json:"notify_enabled"`
	Group1Enabled         bool   `json:"group1_enabled"`
	Group2Enabled         bool   `json:"group2_enabled"`
	Group3Enabled         bool   `json:"group3_enabled"`
	Group4Enabled         bool   `json:"group4_enabled"`
	Group5Enabled         bool   `json:"group5_enabled"`
	Group6Enabled         bool   `json:"group6_enabled"`
	Group7Enabled         bool   `json:"group7_enabled"`
}

type TelegramSendHistoryRow struct {
	ID         int64  `json:"id"`
	SentAt     int64  `json:"sent_at"`
	SendMode   string `json:"send_mode"`
	GroupIndex int    `json:"group_index"`
	GroupName  string `json:"group_name"`
	Status     string `json:"status"`
	ErrorText  string `json:"error_text,omitempty"`
}

type ChannelTimelineRow struct {
	TS      int64  `json:"ts"`
	Source  string `json:"source"`
	Type    string `json:"type"`
	Window  string `json:"window,omitempty"`
	Target  string `json:"target,omitempty"`
	Status  string `json:"status"`
	Detail  string `json:"detail,omitempty"`
	Records int    `json:"records,omitempty"`
}

type ChannelPlannedPushRow struct {
	PushTS      int64  `json:"push_ts"`
	CaptureTS   int64  `json:"capture_ts"`
	Period      string `json:"period"`
	IntervalMin int    `json:"interval_min"`
	Detail      string `json:"detail,omitempty"`
}
