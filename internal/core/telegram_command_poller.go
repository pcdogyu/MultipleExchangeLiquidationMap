package liqmap

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

const telegramPollerConflictBackoff = 5 * time.Minute

type TelegramUpdate struct {
	UpdateID      int64                  `json:"update_id"`
	Message       *TelegramMessage       `json:"message,omitempty"`
	CallbackQuery *TelegramCallbackQuery `json:"callback_query,omitempty"`
}

type TelegramMessage struct {
	MessageID int64        `json:"message_id"`
	Chat      TelegramChat `json:"chat"`
	Text      string       `json:"text,omitempty"`
}

type TelegramChat struct {
	ID       int64  `json:"id"`
	Type     string `json:"type,omitempty"`
	Title    string `json:"title,omitempty"`
	Username string `json:"username,omitempty"`
}

type TelegramCallbackQuery struct {
	ID      string           `json:"id"`
	Message *TelegramMessage `json:"message,omitempty"`
	Data    string           `json:"data,omitempty"`
}

func (a *App) startTelegramCommandPoller(ctx context.Context) {
	go func() {
		conflictLogged := false
		for {
			select {
			case <-ctx.Done():
				return
			default:
			}

			token := normalizeQuotedInput(a.getSetting("telegram_bot_token"))
			if token == "" {
				_ = sleepWithContext(ctx, 30*time.Second)
				continue
			}
			if strings.TrimSpace(a.getSetting("telegram_update_offset_bootstrapped")) != "1" {
				if updates, err := a.getTelegramUpdates(ctx, token, -1, 1, 0); err == nil && len(updates) > 0 {
					_ = a.setSetting("telegram_update_offset", strconv.FormatInt(updates[len(updates)-1].UpdateID+1, 10))
				}
				_ = a.setSetting("telegram_update_offset_bootstrapped", "1")
				continue
			}

			offset := a.getSettingInt64("telegram_update_offset", 0)
			updates, err := a.getTelegramUpdates(ctx, token, offset, 20, 25)
			if err != nil {
				if isTelegramGetUpdatesConflict(err) {
					if !conflictLogged {
						log.Printf("telegram command poller paused for %s: another bot instance is already using getUpdates (%v)", telegramPollerConflictBackoff, err)
						conflictLogged = true
					}
					_ = sleepWithContext(ctx, telegramPollerConflictBackoff)
					continue
				}
				conflictLogged = false
				if a.debug {
					log.Printf("telegram command polling failed: %v", err)
				}
				_ = sleepWithContext(ctx, 5*time.Second)
				continue
			}
			conflictLogged = false
			for _, upd := range updates {
				a.dispatchTelegramUpdate(ctx, token, upd)
				_ = a.setSetting("telegram_update_offset", strconv.FormatInt(upd.UpdateID+1, 10))
			}
		}
	}()
}

func (a *App) getTelegramUpdates(ctx context.Context, token string, offset int64, limit, timeoutSec int) ([]TelegramUpdate, error) {
	if limit <= 0 {
		limit = 20
	}
	payload := map[string]any{
		"limit":           limit,
		"timeout":         timeoutSec,
		"allowed_updates": []string{"message", "callback_query"},
	}
	if offset != 0 {
		payload["offset"] = offset
	}
	var updates []TelegramUpdate
	if err := a.callTelegramAPI(ctx, token, "getUpdates", payload, &updates); err != nil {
		return nil, err
	}
	return updates, nil
}

func (a *App) callTelegramAPI(ctx context.Context, token, method string, payload any, out any) error {
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	timeout := time.Duration(35) * time.Second
	if method != "getUpdates" {
		timeout = telegramTimeoutFromEnv("TELEGRAM_TEXT_TIMEOUT_SEC", telegramTextTimeout)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.telegramAPIBaseURL()+fmt.Sprintf("/bot%s/%s", token, method), bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := a.telegramHTTPClient(timeout).Do(req)
	if err != nil {
		return wrapTelegramNetworkError(a.telegramAPIBaseURL(), err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return buildTelegramStatusError(resp.Status, body)
	}
	var env struct {
		OK          bool            `json:"ok"`
		Result      json.RawMessage `json:"result"`
		Description string          `json:"description,omitempty"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return err
	}
	if !env.OK {
		if strings.TrimSpace(env.Description) == "" {
			return fmt.Errorf("telegram api %s returned ok=false", method)
		}
		return fmt.Errorf("telegram api %s returned ok=false: %s", method, env.Description)
	}
	if out != nil && len(env.Result) > 0 {
		return json.Unmarshal(env.Result, out)
	}
	return nil
}

func isTelegramGetUpdatesConflict(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(strings.TrimSpace(err.Error()))
	if !strings.Contains(msg, "getupdates") {
		return false
	}
	return strings.Contains(msg, "409 conflict") || strings.Contains(msg, "terminated by other getupdates request")
}

func (a *App) dispatchTelegramUpdate(ctx context.Context, token string, upd TelegramUpdate) {
	if upd.Message != nil {
		a.handleTelegramMessage(ctx, token, *upd.Message)
		return
	}
	if upd.CallbackQuery != nil {
		a.handleTelegramCallback(ctx, token, *upd.CallbackQuery)
	}
}

func (a *App) handleTelegramMessage(ctx context.Context, token string, msg TelegramMessage) {
	text := strings.TrimSpace(msg.Text)
	if text == "" || !strings.HasPrefix(text, "/") {
		return
	}
	if !a.telegramCommandAllowed(msg.Chat) {
		_ = a.sendTelegramTextToChat(msg.Chat.ID, "无权限执行该命令")
		return
	}
	if isTelegramMenuCommand(text) {
		if err := a.sendTelegramCommandMenu(ctx, token, msg.Chat.ID); err != nil && a.debug {
			log.Printf("send telegram command menu failed: %v", err)
		}
		return
	}
	if isTelegramStatusCommand(text) {
		_ = a.sendTelegramTextToChat(msg.Chat.ID, a.buildTelegramCommandStatusText())
		return
	}
	if isTelegramPullAllCommand(text) {
		a.startTelegramCommandPullAll(ctx, msg.Chat.ID)
		return
	}
	if days, ok := parseTelegramPullWindow(text); ok {
		a.startTelegramCommandPull(ctx, msg.Chat.ID, days)
	}
}

func (a *App) handleTelegramCallback(ctx context.Context, token string, cb TelegramCallbackQuery) {
	if strings.TrimSpace(cb.ID) != "" {
		_ = a.answerTelegramCallback(ctx, token, cb.ID, "已收到")
	}
	if cb.Message == nil {
		return
	}
	chat := cb.Message.Chat
	if !a.telegramCommandAllowed(chat) {
		_ = a.sendTelegramTextToChat(chat.ID, "无权限执行该操作")
		return
	}
	data := strings.TrimSpace(cb.Data)
	if data == "status" {
		_ = a.sendTelegramTextToChat(chat.ID, a.buildTelegramCommandStatusText())
		return
	}
	if data == "pull:all" {
		a.startTelegramCommandPullAll(ctx, chat.ID)
		return
	}
	if strings.HasPrefix(data, "pull:") {
		days, err := strconv.Atoi(strings.TrimPrefix(data, "pull:"))
		if err == nil && (days == 1 || days == 7 || days == 30) {
			a.startTelegramCommandPull(ctx, chat.ID, days)
		}
	}
}

func (a *App) answerTelegramCallback(ctx context.Context, token, callbackID, text string) error {
	return a.callTelegramAPI(ctx, token, "answerCallbackQuery", map[string]any{
		"callback_query_id": callbackID,
		"text":              text,
		"show_alert":        false,
	}, nil)
}

func (a *App) sendTelegramCommandMenu(ctx context.Context, token string, chatID int64) error {
	return a.callTelegramAPI(ctx, token, "sendMessage", map[string]any{
		"chat_id":    chatID,
		"text":       "请选择要抓取的周期：",
		"parse_mode": "HTML",
		"reply_markup": map[string]any{
			"inline_keyboard": [][]map[string]string{
				{
					{"text": "抓取 1d", "callback_data": "pull:1"},
					{"text": "抓取 7d", "callback_data": "pull:7"},
					{"text": "抓取 30d", "callback_data": "pull:30"},
				},
				{{"text": "抓取全部并发送 8 组", "callback_data": "pull:all"}},
				{{"text": "状态", "callback_data": "status"}},
			},
		},
	}, nil)
}

func (a *App) startTelegramCommandPull(ctx context.Context, chatID int64, windowDays int) {
	go func() {
		windowLabel := fmt.Sprintf("%dd", windowDays)
		_ = a.sendTelegramTextToChat(chatID, fmt.Sprintf("已收到，开始抓取 %s", windowLabel))
		if !a.beginTelegramBundleSend() {
			_ = a.sendTelegramTextToChat(chatID, "已有报告发送任务进行中，请稍后再试")
			return
		}
		defer a.endTelegramBundleSend()

		days := windowDays
		if err := a.webds.runSync(ctx, &days); err != nil {
			if strings.Contains(strings.ToLower(err.Error()), "already in progress") {
				_ = a.sendTelegramTextToChat(chatID, "已有抓取任务进行中，请稍后再试")
				return
			}
			_ = a.sendTelegramTextToChat(chatID, fmt.Sprintf("抓取 %s 失败：%s", windowLabel, template.HTMLEscapeString(shortErrorText(err))))
			return
		}
		_ = a.sendTelegramTextToChat(chatID, fmt.Sprintf("%s 抓取完成，开始发送报告", windowLabel))
		if err := a.sendTelegramWindowDemandBundleLocked(windowDays, chatID); err != nil {
			_ = a.sendTelegramTextToChat(chatID, fmt.Sprintf("发送 %s 报告失败：%s", windowLabel, template.HTMLEscapeString(shortErrorText(err))))
			return
		}
		_ = a.sendTelegramTextToChat(chatID, fmt.Sprintf("%s 报告发送完成", windowLabel))
	}()
}

func (a *App) startTelegramCommandPullAll(ctx context.Context, chatID int64) {
	go func() {
		_ = a.sendTelegramTextToChat(chatID, "已收到，开始抓取 1d / 7d / 30d")
		if !a.beginTelegramBundleSend() {
			_ = a.sendTelegramTextToChat(chatID, "已有抓取或报告发送任务进行中，请稍后再试")
			return
		}
		defer a.endTelegramBundleSend()

		if err := a.webds.runSync(ctx, nil); err != nil {
			if strings.Contains(strings.ToLower(err.Error()), "already in progress") {
				_ = a.sendTelegramTextToChat(chatID, "已有抓取任务进行中，请稍后再试")
				return
			}
			_ = a.sendTelegramTextToChat(chatID, fmt.Sprintf("抓取全部周期失败：%s", template.HTMLEscapeString(shortErrorText(err))))
			return
		}
		_ = a.sendTelegramTextToChat(chatID, "1d / 7d / 30d 抓取完成，开始发送 8 组内容")
		if err := a.sendTelegramAllGroupsDemandBundleLocked(chatID); err != nil {
			_ = a.sendTelegramTextToChat(chatID, fmt.Sprintf("发送 8 组内容失败：%s", template.HTMLEscapeString(shortErrorText(err))))
			return
		}
		_ = a.sendTelegramTextToChat(chatID, "8 组内容发送完成")
	}()
}

func (a *App) sendTelegramWindowDemandBundleLocked(windowDays int, chatID any) error {
	if windowDays != 1 && windowDays != 7 && windowDays != 30 {
		return fmt.Errorf("unsupported telegram report window: %d", windowDays)
	}
	windowKey := fmt.Sprintf("%dd", windowDays)
	webMap := a.webds.loadLatestMap(windowKey)
	displayPrice := a.resolveUnifiedDisplayPrice(webMap, 0)
	monitorReport, modelBands, err := a.buildModelHeatReportBundleForWindow(windowDays, displayPrice)
	if err != nil {
		return fmt.Errorf("build %d-day monitor report: %w", windowDays, err)
	}
	monitorBands := buildMonitorHeatBandsFromWebMapAtPrice(webMap, displayPrice)
	if len(monitorBands) == 0 {
		monitorBands = modelBands
	}

	var errs []string
	if !webMap.HasData || len(webMap.Points) == 0 {
		errText := strings.TrimSpace(webMap.LastError)
		if errText == "" {
			errText = fmt.Sprintf("no %d-day webdatasource snapshot available", windowDays)
		}
		msg := fmt.Sprintf("数据缺失: %s", errText)
		a.recordTelegramSendHistory("command", 1, fmt.Sprintf("webdatasource-%s-image", windowKey), "failed", msg)
		errs = append(errs, msg)
	} else {
		webImage, err := a.captureWebDataSourceScreenshotJPEG(windowKey)
		if err != nil {
			msg := fmt.Sprintf("截图失败: %v", err)
			a.recordTelegramSendHistory("command", 1, fmt.Sprintf("webdatasource-%s-image", windowKey), "failed", msg)
			errs = append(errs, msg)
		} else if err := a.sendTelegramPhotoToChat(chatID, "", webImage); err != nil {
			msg := fmt.Sprintf("发送失败: %v", err)
			a.recordTelegramSendHistory("command", 1, fmt.Sprintf("webdatasource-%s-image", windowKey), "failed", msg)
			errs = append(errs, msg)
		} else {
			a.recordTelegramSendHistory("command", 1, fmt.Sprintf("webdatasource-%s-image", windowKey), "success", "")
		}
	}

	monitorImage, err := a.captureMonitorScreenshotJPEG(windowDays)
	if err != nil {
		msg := fmt.Sprintf("截图失败: %v", err)
		a.recordTelegramSendHistory("command", 2, fmt.Sprintf("monitor-%s-image", windowKey), "failed", msg)
		errs = append(errs, msg)
	} else if err := a.sendTelegramPhotoToChat(chatID, "", monitorImage); err != nil {
		msg := fmt.Sprintf("发送失败: %v", err)
		a.recordTelegramSendHistory("command", 2, fmt.Sprintf("monitor-%s-image", windowKey), "failed", msg)
		errs = append(errs, msg)
	} else {
		a.recordTelegramSendHistory("command", 2, fmt.Sprintf("monitor-%s-image", windowKey), "success", "")
	}

	text := a.buildTelegramThirtyDayTextSafe(monitorReport, monitorBands, webMap)
	if err := a.sendTelegramTextToChat(chatID, text); err != nil {
		msg := fmt.Sprintf("发送失败: %v", err)
		a.recordTelegramSendHistory("command", 3, fmt.Sprintf("monitor-%s-text", windowKey), "failed", msg)
		errs = append(errs, msg)
	} else {
		a.recordTelegramSendHistory("command", 3, fmt.Sprintf("monitor-%s-text", windowKey), "success", "")
	}

	if len(errs) > 0 {
		return errors.New(strings.Join(errs, " | "))
	}
	return nil
}

func (a *App) sendTelegramAllGroupsDemandBundleLocked(chatID any) error {
	const windowDays = 30
	const sendMode = "command"
	settings := a.loadSettings()

	webMap := a.webds.loadLatestMap("30d")
	displayPrice := a.resolveUnifiedDisplayPrice(webMap, 0)
	monitorReport, modelBands, err := a.buildModelHeatReportBundleForWindow(windowDays, displayPrice)
	if err != nil {
		return fmt.Errorf("build 30-day monitor report: %w", err)
	}
	monitorBands := buildMonitorHeatBandsFromWebMapAtPrice(webMap, displayPrice)
	if len(monitorBands) == 0 {
		monitorBands = modelBands
	}

	var errs []string
	var analysisSnapshot AnalysisSnapshot
	var analysisSnapshotLoaded bool
	loadAnalysisSnapshot := func() (AnalysisSnapshot, error) {
		if analysisSnapshotLoaded {
			return analysisSnapshot, nil
		}
		snapshot, err := a.BuildAnalysisSnapshot()
		if err != nil {
			return AnalysisSnapshot{}, err
		}
		analysisSnapshot = snapshot
		analysisSnapshotLoaded = true
		return analysisSnapshot, nil
	}

	if settings.Group1Enabled {
		if !webMap.HasData || len(webMap.Points) == 0 {
			errText := webDataSourceNoDataMessage("30d", webMap.LastError)
			msg := fmt.Sprintf("数据缺失: %s", errText)
			a.recordTelegramSendHistory(sendMode, 1, "webdatasource-30d-image", "failed", msg)
			errs = append(errs, msg)
		} else {
			webImage, err := a.captureWebDataSourceScreenshotJPEG("30d")
			if err != nil {
				msg := fmt.Sprintf("截图失败: %v", err)
				a.recordTelegramSendHistory(sendMode, 1, "webdatasource-30d-image", "failed", msg)
				errs = append(errs, msg)
			} else if err := a.sendTelegramPhotoToChat(chatID, "", webImage); err != nil {
				msg := fmt.Sprintf("发送失败: %v", err)
				a.recordTelegramSendHistory(sendMode, 1, "webdatasource-30d-image", "failed", msg)
				errs = append(errs, msg)
			} else {
				a.recordTelegramSendHistory(sendMode, 1, "webdatasource-30d-image", "success", "")
			}
		}
	}

	if settings.Group2Enabled {
		monitorImage, err := a.captureMonitorScreenshotJPEG(windowDays)
		if err != nil {
			msg := fmt.Sprintf("截图失败: %v", err)
			a.recordTelegramSendHistory(sendMode, 2, "monitor-30d-image", "failed", msg)
			errs = append(errs, msg)
		} else if err := a.sendTelegramPhotoToChat(chatID, "", monitorImage); err != nil {
			msg := fmt.Sprintf("发送失败: %v", err)
			a.recordTelegramSendHistory(sendMode, 2, "monitor-30d-image", "failed", msg)
			errs = append(errs, msg)
		} else {
			a.recordTelegramSendHistory(sendMode, 2, "monitor-30d-image", "success", "")
		}
	}

	if settings.Group3Enabled {
		text := a.buildTelegramThirtyDayTextSafe(monitorReport, monitorBands, webMap)
		if err := a.sendTelegramTextToChat(chatID, text); err != nil {
			msg := fmt.Sprintf("发送失败: %v", err)
			a.recordTelegramSendHistory(sendMode, 3, "monitor-30d-text", "failed", msg)
			errs = append(errs, msg)
		} else {
			a.recordTelegramSendHistory(sendMode, 3, "monitor-30d-text", "success", "")
		}
	}

	if settings.Group4Enabled {
		analysisImage, err := a.captureAnalysisScreenshotJPEG()
		if err != nil {
			msg := fmt.Sprintf("截图失败: %v", err)
			a.recordTelegramSendHistory(sendMode, 4, "analysis-image", "failed", msg)
			errs = append(errs, msg)
		} else if err := a.sendTelegramPhotoToChat(chatID, "", analysisImage); err != nil {
			msg := fmt.Sprintf("发送失败: %v", err)
			a.recordTelegramSendHistory(sendMode, 4, "analysis-image", "failed", msg)
			errs = append(errs, msg)
		} else {
			a.recordTelegramSendHistory(sendMode, 4, "analysis-image", "success", "")
		}
	}

	if settings.Group5Enabled {
		snapshot, err := loadAnalysisSnapshot()
		if err != nil {
			msg := fmt.Sprintf("生成日内分析失败: %v", err)
			a.recordTelegramSendHistory(sendMode, 5, "analysis-text", "failed", msg)
			errs = append(errs, msg)
		} else if err := a.sendTelegramTextToChat(chatID, a.buildAnalysisTelegramTextSafe(snapshot)); err != nil {
			msg := fmt.Sprintf("发送失败: %v", err)
			a.recordTelegramSendHistory(sendMode, 5, "analysis-text", "failed", msg)
			errs = append(errs, msg)
		} else {
			a.recordTelegramSendHistory(sendMode, 5, "analysis-text", "success", "")
		}
	}

	if settings.Group6Enabled {
		structureImage, err := a.captureLiquidationsStructureScreenshotJPEG()
		if err != nil {
			msg := fmt.Sprintf("截图失败: %v", err)
			a.recordTelegramSendHistory(sendMode, 6, "liquidations-structure-image", "failed", msg)
			errs = append(errs, msg)
		} else if err := a.sendTelegramPhotoToChat(chatID, "", structureImage); err != nil {
			msg := fmt.Sprintf("发送失败: %v", err)
			a.recordTelegramSendHistory(sendMode, 6, "liquidations-structure-image", "failed", msg)
			errs = append(errs, msg)
		} else {
			a.recordTelegramSendHistory(sendMode, 6, "liquidations-structure-image", "success", "")
		}
		if err := a.sendTelegramTextToChat(chatID, a.buildLiquidationPatternQuestionAttachment()); err != nil {
			msg := fmt.Sprintf("发送失败: %v", err)
			a.recordTelegramSendHistory(sendMode, 6, "liquidations-pattern-text", "failed", msg)
			errs = append(errs, msg)
		} else {
			a.recordTelegramSendHistory(sendMode, 6, "liquidations-pattern-text", "success", "")
		}
	}

	if settings.Group7Enabled {
		bubblesImage, err := a.captureBubblesScreenshotJPEG()
		if err != nil {
			msg := fmt.Sprintf("截图失败: %v", err)
			a.recordTelegramSendHistory(sendMode, 7, "bubbles-5m-24h-image", "failed", msg)
			errs = append(errs, msg)
		} else if err := a.sendTelegramPhotoToChat(chatID, "", bubblesImage); err != nil {
			msg := fmt.Sprintf("发送失败: %v", err)
			a.recordTelegramSendHistory(sendMode, 7, "bubbles-5m-24h-image", "failed", msg)
			errs = append(errs, msg)
		} else {
			a.recordTelegramSendHistory(sendMode, 7, "bubbles-5m-24h-image", "success", "")
		}
	}

	if settings.Group8Enabled {
		if err := a.sendLiquidationSyncAlertDemandToChat(sendMode, chatID); err != nil {
			errs = append(errs, fmt.Sprintf("liquidations sync alert: %v", err))
		}
	}

	if len(errs) > 0 {
		return errors.New(strings.Join(errs, " | "))
	}
	return nil
}

func (a *App) sendLiquidationSyncAlertDemandToChat(sendMode string, chatID any) error {
	summary := a.LiquidationPeriodSummary(LiquidationListOptions{Symbol: defaultSymbol})
	prev := a.loadLiquidationSyncAlertState()
	curr := buildLiquidationSyncAlertState(summary)
	text := a.buildLiquidationSyncAlertText(summary, prev, curr, false)
	if err := a.sendTelegramTextToChat(chatID, text); err != nil {
		a.recordTelegramSendHistory(sendMode, 8, "liquidations-sync-alert", "failed", err.Error())
		return err
	}
	a.recordTelegramSendHistory(sendMode, 8, "liquidations-sync-alert", "success", "")
	return nil
}

func (a *App) buildTelegramCommandStatusText() string {
	status := a.webds.loadStatus()
	lines := []string{
		"<b>页面数据源状态</b>",
		fmt.Sprintf("运行中：%s", yesNoCN(status.Running)),
	}
	if strings.TrimSpace(status.CurrentAction) != "" {
		lines = append(lines, "当前步骤："+template.HTMLEscapeString(status.CurrentAction))
	}
	if status.LastRun != nil {
		last := status.LastRun
		ts := last.FinishedAt
		if ts <= 0 {
			ts = last.StartedAt
		}
		lines = append(lines,
			fmt.Sprintf("最近任务：%dd | %s | %d 条", last.WindowDays, template.HTMLEscapeString(last.Status), last.RecordsCount),
			"最近时间："+time.UnixMilli(ts).Format("2006-01-02 15:04:05"),
		)
		if strings.TrimSpace(last.ErrorMessage) != "" {
			lines = append(lines, "错误："+template.HTMLEscapeString(shortErrorText(errors.New(last.ErrorMessage))))
		}
	}
	if strings.TrimSpace(status.LastError) != "" {
		lines = append(lines, "最后错误："+template.HTMLEscapeString(shortErrorText(errors.New(status.LastError))))
	}
	return strings.Join(lines, "\n")
}

func yesNoCN(v bool) string {
	if v {
		return "是"
	}
	return "否"
}

func shortErrorText(err error) string {
	if err == nil {
		return ""
	}
	text := strings.TrimSpace(err.Error())
	if len([]rune(text)) <= 260 {
		return text
	}
	r := []rune(text)
	return string(r[:260]) + "..."
}

func isTelegramMenuCommand(text string) bool {
	cmd, _ := splitTelegramCommand(text)
	return cmd == "/menu" || cmd == "/start"
}

func isTelegramStatusCommand(text string) bool {
	cmd, _ := splitTelegramCommand(text)
	return cmd == "/status"
}

func isTelegramPullAllCommand(text string) bool {
	cmd, _ := splitTelegramCommand(text)
	return cmd == "/pullall"
}

func parseTelegramPullWindow(text string) (int, bool) {
	cmd, args := splitTelegramCommand(text)
	switch cmd {
	case "/pull1d":
		return 1, true
	case "/pull7d":
		return 7, true
	case "/pull30d":
		return 30, true
	case "/pull":
		switch strings.ToLower(strings.TrimSpace(args)) {
		case "1", "1d":
			return 1, true
		case "7", "7d":
			return 7, true
		case "30", "30d":
			return 30, true
		}
	}
	return 0, false
}

func splitTelegramCommand(text string) (string, string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return "", ""
	}
	parts := strings.Fields(text)
	if len(parts) == 0 {
		return "", ""
	}
	cmd := strings.ToLower(parts[0])
	if at := strings.Index(cmd, "@"); at >= 0 {
		cmd = cmd[:at]
	}
	args := ""
	if len(parts) > 1 {
		args = strings.Join(parts[1:], " ")
	}
	return cmd, args
}

func (a *App) telegramCommandAllowed(chat TelegramChat) bool {
	allowedRaw := strings.TrimSpace(a.getSetting("telegram_allowed_chat_ids"))
	if allowedRaw == "" {
		allowedRaw = strings.TrimSpace(os.Getenv("TELEGRAM_ALLOWED_CHAT_IDS"))
	}
	if allowedRaw != "" {
		for _, item := range strings.Split(allowedRaw, ",") {
			if telegramChatMatches(chat, strings.TrimSpace(item)) {
				return true
			}
		}
		return false
	}
	return telegramChatMatches(chat, normalizeQuotedInput(a.getSetting("telegram_channel")))
}

func telegramChatMatches(chat TelegramChat, configured string) bool {
	configured = strings.TrimSpace(configured)
	if configured == "" {
		return false
	}
	if configured == strconv.FormatInt(chat.ID, 10) {
		return true
	}
	username := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(chat.Username)), "@")
	confUsername := strings.TrimPrefix(strings.ToLower(configured), "@")
	return username != "" && confUsername != "" && username == confUsername
}
