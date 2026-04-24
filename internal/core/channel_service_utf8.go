package liqmap

import "time"

func buildChannelPlannedPushDetail(previewOnly bool) string {
	detail := "抓取会比推送提前 5 分钟，用于生成最新清算热区摘要。"
	if previewOnly {
		detail += " 当前为预览排程，自动通知尚未开启。"
	}
	return detail
}

func (a *App) listChannelPlannedPushesUTF8(hours int) []ChannelPlannedPushRow {
	if hours <= 0 {
		hours = 24
	}
	settings := a.loadSettings()
	previewOnly := !settings.NotifyEnabled
	settings.NotifyEnabled = true
	now := time.Now().Truncate(time.Minute)
	limit := now.Add(time.Duration(hours) * time.Hour)
	baseTS := a.getLastNotifyTS()
	if historyTS := a.latestSuccessfulNotifyTS(); historyTS > baseTS {
		baseTS = historyTS
	}
	out := make([]ChannelPlannedPushRow, 0, 64)
	cursor := now
	detail := buildChannelPlannedPushDetail(previewOnly)
	for !cursor.After(limit) {
		pushAt, intervalMin, period, ok := a.nextTelegramAutoNotifyAfter(settings, baseTS, cursor, limit)
		if !ok {
			break
		}
		pushTS := pushAt.UnixMilli()
		out = append(out, ChannelPlannedPushRow{
			PushTS:      pushTS,
			CaptureTS:   pushTS - 5*60*1000,
			Period:      period,
			IntervalMin: intervalMin,
			Detail:      detail,
		})
		baseTS = pushTS
		cursor = pushAt.Add(time.Minute)
	}
	return out
}
