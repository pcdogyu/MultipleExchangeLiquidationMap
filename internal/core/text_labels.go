package liqmap

import (
	"fmt"
	"strings"
)

func heatReportBias(up, down float64) string {
	total := up + down
	if total <= 0 {
		return "基本均衡"
	}
	if down > up {
		return "下方偏多"
	}
	if up > down {
		return "上方偏多"
	}
	return "基本均衡"
}

func telegramSendLabel(isTest bool) string {
	if isTest {
		return "Test Send"
	}
	return "Auto Send"
}

func topExchangeLabel(contrib []ExchangeContribution) string {
	if len(contrib) == 0 {
		return "-"
	}
	return fmt.Sprintf("%s %.0f%%", contrib[0].Exchange, contrib[0].Share*100)
}

func topPointLabel(points []WebDataSourceTopPoint) string {
	if len(points) == 0 {
		return "-"
	}
	return fmt.Sprintf("$%.1f / %s yi", points[0].Price, formatYi2(points[0].LiqValue))
}

func telegramImbalanceLabel(up, down float64) string {
	switch {
	case down > up:
		return "多单偏多"
	case up > down:
		return "空单偏多"
	default:
		return "基本均衡"
	}
}

func telegramPeakVerdict(longPeak, shortPeak HeatReportPeak) string {
	switch {
	case longPeak.SingleUSD > shortPeak.SingleUSD:
		return "下方多单更强"
	case shortPeak.SingleUSD > longPeak.SingleUSD:
		return "上方空单更强"
	default:
		return "最长柱均衡"
	}
}

func scoreTone(score float64) string {
	switch {
	case score >= 75:
		return "high"
	case score >= 55:
		return "medium"
	default:
		return "low"
	}
}

func signedTone(v float64) string {
	switch {
	case v > 0:
		return "up"
	case v < 0:
		return "down"
	default:
		return "flat"
	}
}

func riskLabel(score float64) string {
	switch {
	case score >= 80:
		return "高风险"
	case score >= 65:
		return "偏高"
	case score >= 50:
		return "中性偏高"
	case score >= 35:
		return "中性"
	default:
		return "偏低"
	}
}

func buildBroadcastSummary(currentPrice float64, overview AnalysisOverview, shortRiskScore, longRiskScore float64, keyZones []AnalysisKeyZone, dash Dashboard) AnalysisBroadcast {
	headline := overview.Title
	lines := []string{
		fmt.Sprintf("当前 ETH 价格 %.1f。", currentPrice),
		fmt.Sprintf("空头被挤压风险 %.0f 分，多头被踩踏风险 %.0f 分。", shortRiskScore, longRiskScore),
	}
	if len(keyZones) >= 2 {
		lines = append(lines, fmt.Sprintf("上方关键价位 %.1f，下方关键价位 %.1f。", keyZones[0].Price, keyZones[1].Price))
	}
	if dash.Analytics.DominantExchange != "" {
		lines = append(lines, fmt.Sprintf("主导交易所为 %s，当前预警等级 %s。", dash.Analytics.DominantExchange, dash.Analytics.Alert.Level))
	}
	return AnalysisBroadcast{
		Headline: headline,
		Text:     strings.Join(lines, ""),
		Bullets: []string{
			overview.Bias,
			fmt.Sprintf("关注提示：%s。", dash.Analytics.Alert.Suggestion),
			fmt.Sprintf("结论置信度 %.0f%%。", overview.Confidence),
		},
	}
}
