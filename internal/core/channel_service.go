package liqmap

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"strconv"
	"strings"
	"time"
)

var webDataSourceCaptureMinutes = []int{0, 5, 10, 15, 20, 25, 30, 35, 40, 45, 50, 55}

func (a *App) nextTelegramAutoNotifyTS(now time.Time) (int64, bool) {
	settings := a.loadSettings()
	if !settings.NotifyEnabled {
		return 0, false
	}
	intervalMin := effectiveNotifyInterval(settings, now)
	if intervalMin <= 0 {
		intervalMin = 15
	}
	baseTS := a.getLastNotifyTS()
	if historyTS := a.latestSuccessfulNotifyTS(); historyTS > baseTS {
		baseTS = historyTS
	}
	if baseTS <= 0 {
		return now.Add(time.Duration(intervalMin) * time.Minute).UnixMilli(), true
	}
	return baseTS + int64(intervalMin)*60*1000, true
}

func latestScheduledWebDataSourceCaptureTS(now time.Time) int64 {
	slot := time.Date(now.Year(), now.Month(), now.Day(), now.Hour(), 0, 0, 0, now.Location())
	minute := now.Minute()
	for i := len(webDataSourceCaptureMinutes) - 1; i >= 0; i-- {
		if minute >= webDataSourceCaptureMinutes[i] {
			return slot.Add(time.Duration(webDataSourceCaptureMinutes[i]) * time.Minute).UnixMilli()
		}
	}
	return slot.Add(-time.Hour).Add(time.Duration(webDataSourceCaptureMinutes[len(webDataSourceCaptureMinutes)-1]) * time.Minute).UnixMilli()
}

func nextScheduledWebDataSourceCaptureTS(now time.Time) int64 {
	slot := time.Date(now.Year(), now.Month(), now.Day(), now.Hour(), 0, 0, 0, now.Location())
	minute := now.Minute()
	second := now.Second()
	nano := now.Nanosecond()
	for _, m := range webDataSourceCaptureMinutes {
		if minute < m || (minute == m && second == 0 && nano == 0) {
			return slot.Add(time.Duration(m) * time.Minute).UnixMilli()
		}
	}
	return slot.Add(time.Hour).Add(time.Duration(webDataSourceCaptureMinutes[0]) * time.Minute).UnixMilli()
}

func (a *App) nextWebDataSourceCaptureTS(now time.Time) (int64, bool) {
	return nextScheduledWebDataSourceCaptureTS(now), true
}

func (a *App) loadSettings() ChannelSettings {
	legacyInterval := parsePositiveIntSetting(a.getSetting("notify_interval_min"), 15)
	workInterval := parsePositiveIntSetting(a.getSetting("notify_work_interval_min"), legacyInterval)
	offInterval := parsePositiveIntSetting(a.getSetting("notify_off_interval_min"), legacyInterval)
	return ChannelSettings{
		TelegramBotToken:      normalizeQuotedInput(a.getSetting("telegram_bot_token")),
		TelegramChannel:       normalizeQuotedInput(a.getSetting("telegram_channel")),
		NotifyIntervalMin:     workInterval,
		NotifyWorkIntervalMin: workInterval,
		NotifyOffIntervalMin:  offInterval,
		WorkTimeExpr:          normalizeQuotedInput(a.getSetting("notify_work_time_expr")),
		NotifyEnabled:         parseBoolSetting(a.getSetting("notify_enabled"), false),
		Group1Enabled:         parseBoolSetting(a.getSetting("notify_group_1_enabled"), true),
		Group2Enabled:         parseBoolSetting(a.getSetting("notify_group_2_enabled"), true),
		Group3Enabled:         parseBoolSetting(a.getSetting("notify_group_3_enabled"), true),
		Group4Enabled:         parseBoolSetting(a.getSetting("notify_group_4_enabled"), true),
		Group5Enabled:         parseBoolSetting(a.getSetting("notify_group_5_enabled"), true),
		Group6Enabled:         parseBoolSetting(a.getSetting("notify_group_6_enabled"), true),
		Group7Enabled:         parseBoolSetting(a.getSetting("notify_group_7_enabled"), true),
		Group8Enabled:         parseBoolSetting(a.getSetting("notify_group_8_enabled"), true),
	}
}

func (a *App) saveSettings(req ChannelSettings) error {
	token := normalizeQuotedInput(req.TelegramBotToken)
	channel := normalizeQuotedInput(req.TelegramChannel)
	if err := a.setSetting("telegram_bot_token", token); err != nil {
		return err
	}
	if err := a.setSetting("telegram_channel", channel); err != nil {
		return err
	}
	workInterval := req.NotifyWorkIntervalMin
	if workInterval <= 0 {
		workInterval = req.NotifyIntervalMin
	}
	if workInterval <= 0 {
		workInterval = 15
	}
	offInterval := req.NotifyOffIntervalMin
	if offInterval <= 0 {
		if req.NotifyIntervalMin > 0 {
			offInterval = req.NotifyIntervalMin
		} else {
			offInterval = workInterval
		}
	}
	workTimeExpr := normalizeQuotedInput(req.WorkTimeExpr)
	if err := validateWorkTimeExpr(workTimeExpr); err != nil {
		return err
	}
	if err := a.setSetting("notify_interval_min", strconv.Itoa(workInterval)); err != nil {
		return err
	}
	if err := a.setSetting("notify_work_interval_min", strconv.Itoa(workInterval)); err != nil {
		return err
	}
	if err := a.setSetting("notify_off_interval_min", strconv.Itoa(offInterval)); err != nil {
		return err
	}
	if err := a.setSetting("notify_work_time_expr", workTimeExpr); err != nil {
		return err
	}
	if err := a.setSetting("notify_enabled", strconv.FormatBool(req.NotifyEnabled)); err != nil {
		return err
	}
	if err := a.setSetting("notify_group_1_enabled", strconv.FormatBool(req.Group1Enabled)); err != nil {
		return err
	}
	if err := a.setSetting("notify_group_2_enabled", strconv.FormatBool(req.Group2Enabled)); err != nil {
		return err
	}
	if err := a.setSetting("notify_group_3_enabled", strconv.FormatBool(req.Group3Enabled)); err != nil {
		return err
	}
	if err := a.setSetting("notify_group_4_enabled", strconv.FormatBool(req.Group4Enabled)); err != nil {
		return err
	}
	if err := a.setSetting("notify_group_5_enabled", strconv.FormatBool(req.Group5Enabled)); err != nil {
		return err
	}
	if err := a.setSetting("notify_group_6_enabled", strconv.FormatBool(req.Group6Enabled)); err != nil {
		return err
	}
	if err := a.setSetting("notify_group_7_enabled", strconv.FormatBool(req.Group7Enabled)); err != nil {
		return err
	}
	if err := a.setSetting("notify_group_8_enabled", strconv.FormatBool(req.Group8Enabled)); err != nil {
		return err
	}
	return nil
}

func (a *App) sendTelegramTestMessage() error {
	return a.sendTelegramThirtyDayBundle(true)
}

func (a *App) beginTelegramBundleSend() bool {
	a.bundleSendMu.Lock()
	defer a.bundleSendMu.Unlock()
	if a.bundleSending {
		return false
	}
	a.bundleSending = true
	return true
}

func (a *App) endTelegramBundleSend() {
	a.bundleSendMu.Lock()
	a.bundleSending = false
	a.bundleSendMu.Unlock()
}

func (a *App) triggerTelegramTestSend() (string, bool) {
	a.testSendMu.Lock()
	if a.testSending {
		a.testSendMu.Unlock()
		return "already running", false
	}
	if !a.beginTelegramBundleSend() {
		a.testSendMu.Unlock()
		return "already running", false
	}
	a.testSending = true
	a.testSendMu.Unlock()

	go func() {
		defer func() {
			a.endTelegramBundleSend()
			a.testSendMu.Lock()
			a.testSending = false
			a.testSendMu.Unlock()
		}()
		if err := a.sendTelegramThirtyDayBundleLocked(true); err != nil && a.debug {
			log.Printf("telegram test send failed: %v", err)
		}
	}()
	return "started", true
}

func (a *App) sendTelegramText(text string) error {
	token := normalizeQuotedInput(a.getSetting("telegram_bot_token"))
	channel := normalizeQuotedInput(a.getSetting("telegram_channel"))
	if token == "" || channel == "" {
		return fmt.Errorf("telegram bot token or channel is empty")
	}

	url := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", token)
	payload := map[string]string{
		"chat_id":    channel,
		"text":       text,
		"parse_mode": "HTML",
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(string(body)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		data, _ := io.ReadAll(resp.Body)
		if len(data) == 0 {
			return fmt.Errorf("telegram api returned %s", resp.Status)
		}
		return fmt.Errorf("telegram api returned %s: %s", resp.Status, strings.TrimSpace(string(data)))
	}
	return nil
}

func (a *App) sendTelegramPhoto(caption string, image []byte) error {
	token := normalizeQuotedInput(a.getSetting("telegram_bot_token"))
	channel := normalizeQuotedInput(a.getSetting("telegram_channel"))
	if token == "" || channel == "" {
		return fmt.Errorf("telegram bot token or channel is empty")
	}
	if len(image) == 0 {
		return fmt.Errorf("telegram photo image is empty")
	}

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	_ = mw.WriteField("chat_id", channel)
	if strings.TrimSpace(caption) != "" {
		_ = mw.WriteField("caption", caption)
		_ = mw.WriteField("parse_mode", "HTML")
	}
	fw, err := mw.CreateFormFile("photo", "heat-report.png")
	if err != nil {
		return err
	}
	if _, err := fw.Write(image); err != nil {
		return err
	}
	if err := mw.Close(); err != nil {
		return err
	}

	url := fmt.Sprintf("https://api.telegram.org/bot%s/sendPhoto", token)
	req, err := http.NewRequest(http.MethodPost, url, &body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		data, _ := io.ReadAll(resp.Body)
		if len(data) == 0 {
			return fmt.Errorf("telegram api returned %s", resp.Status)
		}
		return fmt.Errorf("telegram api returned %s: %s", resp.Status, strings.TrimSpace(string(data)))
	}
	return nil
}

func (a *App) recordTelegramSendHistory(sendMode string, groupIndex int, groupName, status, errorText string) {
	sendMode = strings.TrimSpace(sendMode)
	if sendMode == "" {
		sendMode = "manual"
	}
	status = strings.TrimSpace(status)
	if status == "" {
		status = "unknown"
	}
	_, err := a.db.Exec(`INSERT INTO telegram_send_history(sent_at, send_mode, group_index, group_name, status, error_text)
		VALUES(?, ?, ?, ?, ?, ?)`,
		time.Now().UnixMilli(),
		sendMode,
		groupIndex,
		strings.TrimSpace(groupName),
		status,
		strings.TrimSpace(errorText),
	)
	if err != nil && a.debug {
		log.Printf("record telegram send history failed: %v", err)
	}
}

func normalizeLegacyTelegramHistoryErrorText(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return v
	}
	replacer := strings.NewReplacer(
		"缁?缂佸嫭鏆熼幑顔惧繁婢?", "\u6570\u636e\u7f3a\u5931:",
		"缁?缂佸嫭鍩呴崶鎯ં亼鐠?", "\u622a\u56fe\u5931\u8d25:",
		"缁?缂佸嫬褰傞柅浣搞亼鐠?", "\u53d1\u9001\u5931\u8d25:",
	)
	v = replacer.Replace(v)
	return strings.TrimSpace(v)
}

func (a *App) listTelegramSendHistory(limit int) ([]TelegramSendHistoryRow, error) {
	if limit <= 0 {
		limit = 60
	}
	rows, err := a.db.Query(`SELECT id, sent_at, send_mode, group_index, group_name, status, error_text
		FROM telegram_send_history
		ORDER BY sent_at DESC, id DESC
		LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]TelegramSendHistoryRow, 0, limit)
	for rows.Next() {
		var item TelegramSendHistoryRow
		if err := rows.Scan(&item.ID, &item.SentAt, &item.SendMode, &item.GroupIndex, &item.GroupName, &item.Status, &item.ErrorText); err != nil {
			return nil, err
		}
		item.ErrorText = normalizeLegacyTelegramHistoryErrorText(item.ErrorText)
		out = append(out, item)
	}
	return out, rows.Err()
}

func (a *App) listChannelTimeline(hours int) ([]ChannelTimelineRow, error) {
	if hours <= 0 {
		hours = 24
	}
	cutoff := time.Now().Add(-time.Duration(hours) * time.Hour).UnixMilli()
	out := make([]ChannelTimelineRow, 0, 64)

	captureRows, err := a.db.Query(`SELECT started_at, finished_at, status, window_days, error_message, records_count
		FROM webdatasource_runs
		WHERE started_at >= ? OR finished_at >= ?
		ORDER BY started_at DESC, id DESC`, cutoff, cutoff)
	if err != nil {
		return nil, err
	}
	defer captureRows.Close()
	for captureRows.Next() {
		var startedAt, finishedAt int64
		var status, errorMessage string
		var windowDays, recordsCount int
		if err := captureRows.Scan(&startedAt, &finishedAt, &status, &windowDays, &errorMessage, &recordsCount); err != nil {
			return nil, err
		}
		ts := finishedAt
		if ts <= 0 {
			ts = startedAt
		}
		out = append(out, ChannelTimelineRow{
			TS:      ts,
			Source:  "webdatasource",
			Type:    "capture",
			Window:  fmt.Sprintf("%dd", windowDays),
			Status:  status,
			Detail:  strings.TrimSpace(errorMessage),
			Records: recordsCount,
		})
	}
	if err := captureRows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func (a *App) nextTelegramAutoNotifyAfter(settings ChannelSettings, baseTS int64, start, limit time.Time) (time.Time, int, string, bool) {
	if start.After(limit) {
		return time.Time{}, 0, "", false
	}
	if baseTS <= 0 {
		intervalMin := effectiveNotifyInterval(settings, start)
		if intervalMin <= 0 {
			intervalMin = 15
		}
		pushAt := start.Add(time.Duration(intervalMin) * time.Minute)
		if pushAt.After(limit) {
			return time.Time{}, 0, "", false
		}
		period := "off-hours"
		if isWorkTime(start, settings.WorkTimeExpr) {
			period = "work-hours"
		}
		return pushAt, intervalMin, period, true
	}
	cursor := start
	for !cursor.After(limit) {
		intervalMin := effectiveNotifyInterval(settings, cursor)
		if intervalMin <= 0 {
			intervalMin = 15
		}
		dueTS := baseTS + int64(intervalMin)*60*1000
		if cursor.UnixMilli() >= dueTS {
			period := "off-hours"
			if isWorkTime(cursor, settings.WorkTimeExpr) {
				period = "work-hours"
			}
			return cursor, intervalMin, period, true
		}
		cursor = cursor.Add(time.Minute)
	}
	return time.Time{}, 0, "", false
}

func (a *App) listChannelPlannedPushes(hours int) []ChannelPlannedPushRow {
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
	for !cursor.After(limit) {
		pushAt, intervalMin, period, ok := a.nextTelegramAutoNotifyAfter(settings, baseTS, cursor, limit)
		if !ok {
			break
		}
		pushTS := pushAt.UnixMilli()
		detail := "棰勮鎻愬墠 5 鍒嗛挓鎶撳彇鏁版嵁锛岀劧鍚庢墽琛屾帹閫?"
		if previewOnly {
			detail += "锛堝綋鍓嶈嚜鍔ㄩ€氱煡鏈紑鍚紝浠呬緵棰勮锛?"
		}
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

func (a *App) listChannelPlannedPushesClean(hours int) []ChannelPlannedPushRow {
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
	for !cursor.After(limit) {
		pushAt, intervalMin, period, ok := a.nextTelegramAutoNotifyAfter(settings, baseTS, cursor, limit)
		if !ok {
			break
		}
		pushTS := pushAt.UnixMilli()
		detail := "抓取会比推送提前 5 分钟，用于生成最新清算热区摘要。"
		if previewOnly {
			detail += " 当前为预览排程，自动通知尚未开启。"
		}
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
