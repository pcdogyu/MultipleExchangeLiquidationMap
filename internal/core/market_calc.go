package liqmap

import (
	"math"
	"strings"
	"time"
)

func heatReportBandBySize(r HeatReportData, band int) HeatReportBand {
	for _, b := range r.Bands {
		if b.Band == band {
			return b
		}
	}
	return HeatReportBand{Band: band}
}

func heatBandBySize(bands []HeatReportBand, band int) HeatReportBand {
	for _, b := range bands {
		if b.Band == band {
			return b
		}
	}
	return HeatReportBand{Band: band}
}

func nearestZoneDistance(current, price float64) float64 {
	if current <= 0 || price <= 0 {
		return 0
	}
	return math.Abs(price - current)
}

func bandRowOrDefault(bands []BandRow, currentPrice float64, band int) BandRow {
	for _, b := range bands {
		if b.Band == band {
			return b
		}
	}
	bandF := float64(band)
	return BandRow{
		Band:            band,
		UpPrice:         math.Round((currentPrice+bandF)*10) / 10,
		DownPrice:       math.Round((currentPrice-bandF)*10) / 10,
		UpNotionalUSD:   0,
		DownNotionalUSD: 0,
	}
}

func classifyImbalance(up, down float64) (string, float64) {
	if up <= 0 && down <= 0 {
		return "暂无明显失衡", 1
	}
	if up >= down {
		ratio := up / math.Max(down, 1e-9)
		if ratio >= 1.25 {
			return "上方偏强", ratio
		}
		return "基本均衡", ratio
	}
	ratio := down / math.Max(up, 1e-9)
	if ratio >= 1.25 {
		return "下方偏强", ratio
	}
	return "基本均衡", ratio
}

func inferShortBias(up, down float64) string {
	total := up + down
	if total <= 0 {
		return "暂无数据"
	}
	delta := (up - down) / total
	if delta >= 0.18 {
		return "偏上杀"
	}
	if delta <= -0.18 {
		return "偏下杀"
	}
	return "基本均衡"
}

func normalizeExchangeName(ex string) string {
	switch strings.ToLower(strings.TrimSpace(ex)) {
	case "binance":
		return "Binance"
	case "bybit":
		return "Bybit"
	case "okx":
		return "OKX"
	default:
		return strings.ToUpper(strings.TrimSpace(ex))
	}
}

func findStateByExchange(states []MarketState, exchange string) *MarketState {
	exchange = strings.ToLower(strings.TrimSpace(exchange))
	for i := range states {
		if strings.ToLower(strings.TrimSpace(states[i].Exchange)) == exchange {
			return &states[i]
		}
	}
	return nil
}

func fundingDirectionText(v *float64) string {
	if v == nil {
		return "-"
	}
	if *v > 0 {
		return "longs pay shorts"
	}
	if *v < 0 {
		return "shorts pay longs"
	}
	return "neutral"
}

func fundingVerdictFromStates(states []MarketState) string {
	pos := 0
	neg := 0
	for _, s := range states {
		if s.FundingRate == nil {
			continue
		}
		switch {
		case *s.FundingRate > 0:
			pos++
		case *s.FundingRate < 0:
			neg++
		}
	}
	switch {
	case pos > neg:
		return "short side favored"
	case neg > pos:
		return "long side favored"
	default:
		return "mostly neutral"
	}
}

func averageMarkPriceWithinAge(states []MarketState, maxAge time.Duration) float64 {
	nowTS := time.Now().UnixMilli()
	var sumPrice float64
	var cnt int
	for _, s := range states {
		if s.MarkPrice <= 0 {
			continue
		}
		if maxAge > 0 && s.UpdatedTS > 0 {
			age := nowTS - s.UpdatedTS
			if age > int64(maxAge/time.Millisecond) {
				continue
			}
		}
		sumPrice += s.MarkPrice
		cnt++
	}
	if cnt == 0 {
		return 0
	}
	return sumPrice / float64(cnt)
}

func averageMarkPrice(states []MarketState) float64 {
	if fresh := averageMarkPriceWithinAge(states, marketStateFreshAge); fresh > 0 {
		return fresh
	}
	return averageMarkPriceWithinAge(states, 0)
}

func averageSnapshotPrice(snapshots []Snapshot) float64 {
	var sum float64
	var cnt int
	for _, s := range snapshots {
		if s.MarkPrice <= 0 {
			continue
		}
		sum += s.MarkPrice
		cnt++
	}
	if cnt == 0 {
		return 0
	}
	return sum / float64(cnt)
}
