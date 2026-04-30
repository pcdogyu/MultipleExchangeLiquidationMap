package liqmap

import (
	"fmt"
	"strings"
)

func (a *App) buildAnalysisTelegramTextSafe(snapshot AnalysisSnapshot) string {
	lines := []string{
		fmt.Sprintf("<b>ETH Intraday Analysis | Price $%s</b>", formatPrice1(snapshot.CurrentPrice)),
		escTelegramHTML(snapshot.Broadcast.Headline),
		escTelegramHTML(snapshot.Broadcast.Text),
		"",
		"<b>Multi-Period Indicators</b>",
	}
	for _, it := range snapshot.Indicators {
		lines = append(lines, fmt.Sprintf("- <b>%s</b>: %s", escTelegramHTML(it.Label), escTelegramHTML(strings.TrimSpace(it.Value+" "+it.Subvalue))))
	}
	if len(snapshot.Broadcast.Bullets) > 0 {
		lines = append(lines, "", "<b>Broadcast Highlights</b>")
		for _, it := range snapshot.Broadcast.Bullets {
			lines = append(lines, "- "+escTelegramHTML(it))
		}
	}
	return strings.Join(lines, "\n")
}
