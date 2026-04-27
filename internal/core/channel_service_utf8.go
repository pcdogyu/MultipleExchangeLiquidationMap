package liqmap

import (
	"sort"
	"time"
)

func buildChannelPlannedPushDetail(previewOnly bool) string {
	detail := "抓取会比推送提前 5 分钟，用于生成最新清算热区摘要。"
	if previewOnly {
		detail += " 当前为预览排程，自动通知尚未开启。"
	}
	return detail
}

func buildWebDataSourceImmediatePushDetail(previewOnly bool) string {
	detail := "webdatasource 按每小时 10 / 25 / 40 / 55 分抓取，成功后立即推送 Telegram。"
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

	out := make([]ChannelPlannedPushRow, 0, 96)
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

	webDetail := buildWebDataSourceImmediatePushDetail(previewOnly)
	cursor = now
	for !cursor.After(limit) {
		captureTS := nextScheduledWebDataSourceCaptureTS(cursor)
		captureAt := time.UnixMilli(captureTS)
		if captureAt.After(limit) {
			break
		}
		out = append(out, ChannelPlannedPushRow{
			PushTS:      captureTS,
			CaptureTS:   captureTS,
			Period:      "webdatasource-immediate",
			IntervalMin: 15,
			Detail:      webDetail,
		})
		cursor = captureAt.Add(time.Minute)
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].PushTS == out[j].PushTS {
			if out[i].Period == out[j].Period {
				return out[i].CaptureTS < out[j].CaptureTS
			}
			return out[i].Period < out[j].Period
		}
		return out[i].PushTS < out[j].PushTS
	})
	return out
}
