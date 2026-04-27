package liqmap

import (
	"fmt"
	"strings"
)

func liquidationPatternTrendTitle(trend string) string {
	switch strings.ToLower(strings.TrimSpace(trend)) {
	case "up_strong":
		return "强上推"
	case "up":
		return "偏上推"
	case "up_light":
		return "轻上推"
	case "down_strong":
		return "强下压"
	case "down":
		return "偏下压"
	case "down_light":
		return "轻下压"
	default:
		return "中性"
	}
}

func (a *App) buildLiquidationPatternQuestionAttachment() string {
	summary := a.LiquidationPeriodSummary(LiquidationListOptions{Symbol: defaultSymbol})
	lines := []string{
		"<b>已附截图：四周期多空清算结构</b>",
		"",
		"<b>四周期 16 种形态判断</b>",
		fmt.Sprintf("%s / %s", escTelegramHTML(summary.Pattern.Code), escTelegramHTML(liquidationPatternTrendTitle(summary.Pattern.TrendBias))),
		escTelegramHTML(strings.TrimSpace(summary.Pattern.Label + "。 " + summary.Pattern.Summary)),
	}
	return strings.Join(lines, "\n")
}

func formatUSDShort(v float64) string {
	switch {
	case v >= 1_000_000_000:
		return fmt.Sprintf("%.2fB", v/1_000_000_000)
	case v >= 1_000_000:
		return fmt.Sprintf("%.2fM", v/1_000_000)
	case v >= 1_000:
		return fmt.Sprintf("%.2fK", v/1_000)
	default:
		return fmt.Sprintf("%.2f", v)
	}
}
