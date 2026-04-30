package liqmap

import (
	"fmt"
	"strings"
)

func (a *App) buildTelegramThirtyDayTextSafe(monitor HeatReportData, monitorBands []HeatReportBand, webMap WebDataSourceMapResponse) string {
	monitor = a.alignTelegramTextHeatReport(monitor, webMap)
	bandBySize := func(band int) HeatReportBand {
		if len(monitorBands) > 0 {
			return heatBandBySize(monitorBands, band)
		}
		return heatReportBandBySize(monitor, band)
	}

	lines := []string{
		fmt.Sprintf("<b>ETH 雷区速报 | 现价$%s</b>", formatPrice1(monitor.CurrentPrice)),
		fmt.Sprintf("总多单<b>%s亿</b> | 总空单<b>%s亿</b>", formatYi1(monitor.LongTotalUSD), formatYi1(monitor.ShortTotalUSD)),
		"",
		"<b>多单最长柱</b>",
		fmt.Sprintf("价格$%s | 距现价<b>%s</b>", formatPrice1(monitor.LongPeak.Price), formatPointDistance(monitor.LongPeak.Distance)),
		fmt.Sprintf("单柱%s亿 | 累计<b>%s亿</b>", formatYi1(monitor.LongPeak.SingleUSD), formatYi1(monitor.LongPeak.CumulativeUSD)),
		"",
		"<b>空单最长柱</b>",
		fmt.Sprintf("价格$%s | 距现价<b>%s</b>", formatPrice1(monitor.ShortPeak.Price), formatPointDistance(monitor.ShortPeak.Distance)),
		fmt.Sprintf("单柱%s亿 | 累计<b>%s亿</b>", formatYi1(monitor.ShortPeak.SingleUSD), formatYi1(monitor.ShortPeak.CumulativeUSD)),
		"",
		"<b>多空失衡概览</b>",
		buildSafeTelegramImbalanceLine(bandBySize(20)),
		buildSafeTelegramImbalanceLine(bandBySize(50)),
		buildSafeTelegramImbalanceLine(bandBySize(80)),
		buildSafeTelegramImbalanceLine(bandBySize(100)),
		buildSafeTelegramImbalanceLine(bandBySize(200)),
		buildSafeTelegramImbalanceLine(bandBySize(300)),
	}
	return strings.Join(lines, "\n")
}
