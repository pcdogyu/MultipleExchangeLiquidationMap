package liqmap

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
)

const liquidationSyncThresholdRatio = 0.15

type liquidationSyncAlertState struct {
	ShortActive    bool   `json:"short_active"`
	ShortDirection string `json:"short_direction"`
	ContActive     bool   `json:"cont_active"`
	ContDirection  string `json:"cont_direction"`
	Alignment      string `json:"alignment"`
}

func (a *App) buildLiquidationSyncSignals(buckets []LiquidationPeriodBucket) LiquidationSyncSignals {
	out := LiquidationSyncSignals{ThresholdRatio: liquidationSyncThresholdRatio}
	if len(buckets) == 0 {
		out.ShortTermSync = LiquidationSyncSignal{
			Direction:      "neutral",
			Label:          "短期方向未确认",
			Reason:         "暂无 1H / 4H 周期数据，无法确认短期方向。",
			FirstPeriod:    "1H",
			SecondPeriod:   "4H",
			ThresholdRatio: liquidationSyncThresholdRatio,
		}
		out.Continuation = LiquidationSyncSignal{
			Direction:      "neutral",
			Label:          "方向未延续",
			Reason:         "暂无 12H / 24H 周期数据，无法确认方向延续。",
			FirstPeriod:    "12H",
			SecondPeriod:   "24H",
			ThresholdRatio: liquidationSyncThresholdRatio,
		}
		out.OverallAlignment = "mixed"
		out.OverallLabel = "暂无同步信号"
		out.OverallReason = "短期确认与方向延续都尚未形成。"
		return out
	}

	byHours := make(map[int]LiquidationPeriodBucket, len(buckets))
	for _, bucket := range buckets {
		byHours[bucket.Hours] = bucket
	}

	out.ShortTermSync = buildLiquidationSyncSignal(byHours[1], byHours[4], "短期方向确认", "短期方向未确认")
	out.Continuation = buildLiquidationSyncSignal(byHours[12], byHours[24], "方向延续", "方向未延续")

	switch {
	case out.ShortTermSync.Active && out.Continuation.Active && out.ShortTermSync.Direction == "up" && out.Continuation.Direction == "up":
		out.OverallAlignment = "up_resonance"
		out.OverallLabel = "上行共振"
		out.OverallReason = "1H / 4H 短期确认与 12H / 24H 方向延续同时偏上推。"
	case out.ShortTermSync.Active && out.Continuation.Active && out.ShortTermSync.Direction == "down" && out.Continuation.Direction == "down":
		out.OverallAlignment = "down_resonance"
		out.OverallLabel = "下行共振"
		out.OverallReason = "1H / 4H 短期确认与 12H / 24H 方向延续同时偏下压。"
	case out.ShortTermSync.Active && !out.Continuation.Active:
		out.OverallAlignment = "mixed"
		out.OverallLabel = "仅短期确认"
		out.OverallReason = "1H / 4H 已同步，但 12H / 24H 尚未形成有效延续。"
	case !out.ShortTermSync.Active && out.Continuation.Active:
		out.OverallAlignment = "mixed"
		out.OverallLabel = "仅方向延续"
		out.OverallReason = "12H / 24H 已同步，但 1H / 4H 尚未形成短期确认。"
	default:
		out.OverallAlignment = "mixed"
		out.OverallLabel = "同步信号混合"
		out.OverallReason = "短期确认与方向延续暂未形成同向共振。"
	}
	return out
}

func buildLiquidationSyncSignal(first, second LiquidationPeriodBucket, activeLabel, inactiveLabel string) LiquidationSyncSignal {
	firstLabel := strings.TrimSpace(first.Label)
	if firstLabel == "" {
		firstLabel = fmt.Sprintf("%dH", first.Hours)
	}
	secondLabel := strings.TrimSpace(second.Label)
	if secondLabel == "" {
		secondLabel = fmt.Sprintf("%dH", second.Hours)
	}

	firstEff := liquidationBucketEffectiveDirection(first)
	secondEff := liquidationBucketEffectiveDirection(second)
	signal := LiquidationSyncSignal{
		Direction:       "neutral",
		Label:           inactiveLabel,
		FirstPeriod:     firstLabel,
		SecondPeriod:    secondLabel,
		ThresholdRatio:  liquidationSyncThresholdRatio,
		FirstEffective:  firstEff,
		SecondEffective: secondEff,
	}

	switch {
	case firstEff == "neutral" && secondEff == "neutral":
		signal.Reason = fmt.Sprintf("%s 与 %s 的差值占比都不足 %.0f%%，当前不计入有效方向。", signal.FirstPeriod, signal.SecondPeriod, liquidationSyncThresholdRatio*100)
	case firstEff == "neutral":
		signal.Reason = fmt.Sprintf("%s 的差值占比不足 %.0f%%，当前不计入有效方向。", signal.FirstPeriod, liquidationSyncThresholdRatio*100)
	case secondEff == "neutral":
		signal.Reason = fmt.Sprintf("%s 的差值占比不足 %.0f%%，当前不计入有效方向。", signal.SecondPeriod, liquidationSyncThresholdRatio*100)
	case firstEff != secondEff:
		signal.Reason = fmt.Sprintf("%s 为%s，%s 为%s，两周期方向不一致。", signal.FirstPeriod, liquidationsDirectionText(firstEff), signal.SecondPeriod, liquidationsDirectionText(secondEff))
	default:
		signal.Active = true
		signal.Direction = firstEff
		signal.Label = activeLabel
		signal.Reason = fmt.Sprintf("%s、%s 同步%s，且差值占比都达到 %.0f%% 以上。", signal.FirstPeriod, signal.SecondPeriod, liquidationsDirectionText(firstEff), liquidationSyncThresholdRatio*100)
	}
	return signal
}

func liquidationBucketEffectiveDirection(bucket LiquidationPeriodBucket) string {
	if bucket.TotalUSD <= 0 || bucket.BalanceRatio < liquidationSyncThresholdRatio {
		return "neutral"
	}
	switch strings.ToLower(strings.TrimSpace(bucket.PricePush)) {
	case "up":
		return "up"
	case "down":
		return "down"
	default:
		return "neutral"
	}
}

func liquidationsDirectionText(direction string) string {
	switch strings.ToLower(strings.TrimSpace(direction)) {
	case "up":
		return "上推"
	case "down":
		return "下压"
	default:
		return "中性"
	}
}

func liquidationsAlignmentText(alignment string) string {
	switch strings.ToLower(strings.TrimSpace(alignment)) {
	case "up_resonance":
		return "上行共振"
	case "down_resonance":
		return "下行共振"
	default:
		return "混合"
	}
}

func liquidationsAlignmentActive(alignment string) bool {
	switch strings.ToLower(strings.TrimSpace(alignment)) {
	case "up_resonance", "down_resonance":
		return true
	default:
		return false
	}
}

func buildLiquidationSyncAlertState(summary LiquidationPeriodSummary) liquidationSyncAlertState {
	return liquidationSyncAlertState{
		ShortActive:    summary.SyncSignals.ShortTermSync.Active,
		ShortDirection: strings.ToLower(strings.TrimSpace(summary.SyncSignals.ShortTermSync.Direction)),
		ContActive:     summary.SyncSignals.Continuation.Active,
		ContDirection:  strings.ToLower(strings.TrimSpace(summary.SyncSignals.Continuation.Direction)),
		Alignment:      strings.ToLower(strings.TrimSpace(summary.SyncSignals.OverallAlignment)),
	}
}

func (a *App) loadLiquidationSyncAlertState() liquidationSyncAlertState {
	raw := strings.TrimSpace(a.getSetting("liquidation_sync_alert_state"))
	if raw == "" {
		return liquidationSyncAlertState{}
	}
	var state liquidationSyncAlertState
	if err := json.Unmarshal([]byte(raw), &state); err != nil {
		return liquidationSyncAlertState{}
	}
	return state
}

func (a *App) saveLiquidationSyncAlertState(state liquidationSyncAlertState) error {
	body, err := json.Marshal(state)
	if err != nil {
		return err
	}
	return a.setSetting("liquidation_sync_alert_state", string(body))
}

func shouldSendLiquidationSyncAlert(prev, curr liquidationSyncAlertState) bool {
	prevAligned := liquidationsAlignmentActive(prev.Alignment)
	currAligned := liquidationsAlignmentActive(curr.Alignment)
	if prevAligned != currAligned {
		return true
	}
	if prevAligned && currAligned && prev.Alignment != curr.Alignment {
		return true
	}
	return false
}

func (a *App) maybeSendLiquidationSyncAlert(sendMode string) error {
	summary := a.LiquidationPeriodSummary(LiquidationListOptions{Symbol: defaultSymbol})
	prev := a.loadLiquidationSyncAlertState()
	curr := buildLiquidationSyncAlertState(summary)

	if !shouldSendLiquidationSyncAlert(prev, curr) {
		return a.saveLiquidationSyncAlertState(curr)
	}

	text := a.buildLiquidationSyncAlertText(summary, prev, curr, false)
	if err := a.sendTelegramText(text); err != nil {
		a.recordTelegramSendHistory(sendMode, 8, "liquidations-sync-alert", "failed", err.Error())
		return err
	}
	a.recordTelegramSendHistory(sendMode, 8, "liquidations-sync-alert", "success", "")
	return a.saveLiquidationSyncAlertState(curr)
}

func (a *App) sendLiquidationSyncAlertTest(sendMode string) error {
	summary := a.LiquidationPeriodSummary(LiquidationListOptions{Symbol: defaultSymbol})
	prev := a.loadLiquidationSyncAlertState()
	curr := buildLiquidationSyncAlertState(summary)
	text := a.buildLiquidationSyncAlertText(summary, prev, curr, true)
	if err := a.sendTelegramText(text); err != nil {
		a.recordTelegramSendHistory(sendMode, 8, "liquidations-sync-alert", "failed", err.Error())
		return err
	}
	a.recordTelegramSendHistory(sendMode, 8, "liquidations-sync-alert", "success", "")
	return nil
}

func (a *App) buildLiquidationSyncAlertText(summary LiquidationPeriodSummary, prev, curr liquidationSyncAlertState, isTest bool) string {
	title := "<b>四周期同步报警</b>"
	if isTest {
		title = "<b>四周期同步报警测试</b>"
	}
	lines := []string{
		title,
		fmt.Sprintf("形态: <b>%s</b> / %s", escTelegramHTML(summary.Pattern.Code), escTelegramHTML(liquidationPatternTrendTitle(summary.Pattern.TrendBias))),
		fmt.Sprintf("短期确认: <b>%s</b>", escTelegramHTML(formatSyncSignalHeadline(summary.SyncSignals.ShortTermSync))),
		escTelegramHTML(summary.SyncSignals.ShortTermSync.Reason),
		fmt.Sprintf("方向延续: <b>%s</b>", escTelegramHTML(formatSyncSignalHeadline(summary.SyncSignals.Continuation))),
		escTelegramHTML(summary.SyncSignals.Continuation.Reason),
		fmt.Sprintf("共振状态: <b>%s</b>", escTelegramHTML(liquidationsAlignmentText(summary.SyncSignals.OverallAlignment))),
		escTelegramHTML(summary.SyncSignals.OverallReason),
		escTelegramHTML(formatLiquidationSyncTransitionSummary(prev, curr, isTest)),
		"",
		"<b>周期摘要</b>",
	}
	for _, bucket := range summary.Buckets {
		lines = append(lines, escTelegramHTML(formatLiquidationBucketSnapshot(bucket)))
	}
	if prev.Alignment != curr.Alignment || prev.ShortDirection != curr.ShortDirection || prev.ContDirection != curr.ContDirection || prev.ShortActive != curr.ShortActive || prev.ContActive != curr.ContActive {
		lines = append(lines, "", escTelegramHTML(formatLiquidationSyncTransition(prev, curr)))
	}
	return strings.Join(lines, "\n")
}

func formatSyncSignalHeadline(signal LiquidationSyncSignal) string {
	if !signal.Active {
		return signal.Label
	}
	return fmt.Sprintf("%s %s", signal.Label, liquidationsDirectionText(signal.Direction))
}

func formatLiquidationBucketSnapshot(bucket LiquidationPeriodBucket) string {
	diff := math.Abs(bucket.LongUSD - bucket.ShortUSD)
	return fmt.Sprintf("%s | %s | 多 %.2fM / 空 %.2fM | 差值 %.2fM | 占比 %.1f%%",
		strings.TrimSpace(bucket.Label),
		liquidationsDirectionText(liquidationBucketEffectiveDirection(bucket)),
		bucket.LongUSD/1_000_000,
		bucket.ShortUSD/1_000_000,
		diff/1_000_000,
		bucket.BalanceRatio*100,
	)
}

func formatLiquidationSyncTransition(prev, curr liquidationSyncAlertState) string {
	return fmt.Sprintf("状态切换: 短期 %s -> %s | 延续 %s -> %s | 共振 %s -> %s",
		liquidationStateLabel(prev.ShortActive, prev.ShortDirection),
		liquidationStateLabel(curr.ShortActive, curr.ShortDirection),
		liquidationStateLabel(prev.ContActive, prev.ContDirection),
		liquidationStateLabel(curr.ContActive, curr.ContDirection),
		liquidationsAlignmentText(prev.Alignment),
		liquidationsAlignmentText(curr.Alignment),
	)
}

func formatLiquidationSyncTransitionSummary(prev, curr liquidationSyncAlertState, isTest bool) string {
	prefix := "状态摘要:"
	if isTest {
		prefix = "测试摘要:"
	}
	return fmt.Sprintf("%s %s -> %s", prefix, liquidationsAlignmentText(prev.Alignment), liquidationsAlignmentText(curr.Alignment))
}

func liquidationStateLabel(active bool, direction string) string {
	if !active {
		return "未确认"
	}
	return liquidationsDirectionText(direction)
}
