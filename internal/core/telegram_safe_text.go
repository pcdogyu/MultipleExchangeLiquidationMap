package liqmap

import (
	"fmt"
	"math"
)

func telegramBandBiasLabel(up, down float64) string {
	switch {
	case down > up:
		return "多单偏多"
	case up > down:
		return "空单偏多"
	default:
		return "基本均衡"
	}
}

func buildSafeTelegramImbalanceLine(b HeatReportBand) string {
	sign := ""
	switch {
	case b.DownNotionalUSD > b.UpNotionalUSD:
		sign = "+"
	case b.UpNotionalUSD > b.DownNotionalUSD:
		sign = "-"
	}
	return fmt.Sprintf(
		"<b>%d点</b> %s（%s%s亿）",
		b.Band,
		telegramBandBiasLabel(b.UpNotionalUSD, b.DownNotionalUSD),
		sign,
		formatYi2(math.Abs(b.DownNotionalUSD-b.UpNotionalUSD)),
	)
}
