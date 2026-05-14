package liqmap

import "math"

type analysisScoreSnapshot struct {
	TS           int64
	CurrentPrice float64
	Bands        []BandRow
	CoreZone     CoreZone
	AvgFunding   float64
	UpByBand     map[int]float64
	DownByBand   map[int]float64
}

type analysisScoreOutcome struct {
	ShortRisk  float64
	LongRisk   float64
	Direction  string
	Confidence float64
	Title      string
	Bias       string
}

func analysisBaseRiskScores(currentPrice float64, bands []BandRow, core CoreZone, avgFunding float64) (float64, float64) {
	b20 := bandRowOrDefault(bands, currentPrice, 20)
	b50 := bandRowOrDefault(bands, currentPrice, 50)
	b100 := bandRowOrDefault(bands, currentPrice, 100)

	upWeighted := b20.UpNotionalUSD*0.5 + math.Max(0, b50.UpNotionalUSD-b20.UpNotionalUSD)*0.3 + math.Max(0, b100.UpNotionalUSD-b50.UpNotionalUSD)*0.2
	downWeighted := b20.DownNotionalUSD*0.5 + math.Max(0, b50.DownNotionalUSD-b20.DownNotionalUSD)*0.3 + math.Max(0, b100.DownNotionalUSD-b50.DownNotionalUSD)*0.2
	totalWeighted := upWeighted + downWeighted

	fundingBias := 0.0
	if avgFunding > 0 {
		fundingBias = clamp(avgFunding*120000, 0, 8)
	} else if avgFunding < 0 {
		fundingBias = clamp(-avgFunding*120000, 0, 8)
	}

	nearUpBonus := 0.0
	nearDownBonus := 0.0
	if core.UpPrice > 0 {
		nearUpBonus = clamp((120-nearestZoneDistance(currentPrice, core.UpPrice))/12, 0, 10)
	}
	if core.DownPrice > 0 {
		nearDownBonus = clamp((120-nearestZoneDistance(currentPrice, core.DownPrice))/12, 0, 10)
	}

	shortRiskScore := 45.0
	longRiskScore := 45.0
	if totalWeighted > 0 {
		shortRiskScore += 35 * (upWeighted / totalWeighted)
		longRiskScore += 35 * (downWeighted / totalWeighted)
	}
	if avgFunding > 0 {
		shortRiskScore += fundingBias
	} else if avgFunding < 0 {
		longRiskScore += fundingBias
	}
	shortRiskScore = clamp(shortRiskScore+nearUpBonus, 0, 100)
	longRiskScore = clamp(longRiskScore+nearDownBonus, 0, 100)
	return shortRiskScore, longRiskScore
}

func analysisApplyMomentumTilt(shortRiskScore, longRiskScore, shortTermTilt float64) (float64, float64) {
	if shortTermTilt > 0 {
		shortRiskScore = clamp(shortRiskScore+shortTermTilt, 0, 100)
		longRiskScore = clamp(longRiskScore-shortTermTilt*0.55, 0, 100)
	} else if shortTermTilt < 0 {
		downTilt := -shortTermTilt
		longRiskScore = clamp(longRiskScore+downTilt, 0, 100)
		shortRiskScore = clamp(shortRiskScore-downTilt*0.55, 0, 100)
	}
	return shortRiskScore, longRiskScore
}

func analysisDirectionAndConfidence(shortRiskScore, longRiskScore float64) (string, float64) {
	switch {
	case shortRiskScore-longRiskScore >= 8:
		return "up", clamp(math.Abs(shortRiskScore-longRiskScore)*4.5, 18, 92)
	case longRiskScore-shortRiskScore >= 8:
		return "down", clamp(math.Abs(shortRiskScore-longRiskScore)*4.5, 18, 92)
	default:
		return "flat", clamp(math.Abs(shortRiskScore-longRiskScore)*4.5, 18, 92)
	}
}

func analysisOverviewHeadline(shortRiskScore, longRiskScore float64) (string, string) {
	bias := "区间内多空风险相对均衡"
	title := "当前市场更像震荡蓄能"
	if shortRiskScore-longRiskScore >= 8 {
		bias = "上方空头清算风险更高，短线更容易被向上挤压"
		title = "当前市场偏向上冲挤空结构"
	} else if longRiskScore-shortRiskScore >= 8 {
		bias = "下方多头清算风险更高，短线更容易出现向下踩踏"
		title = "当前市场偏向下探踩踏结构"
	}
	return title, bias
}

func analysisScoreFromSnapshotBase(snap analysisScoreSnapshot) analysisScoreOutcome {
	shortRiskScore, longRiskScore := analysisBaseRiskScores(snap.CurrentPrice, snap.Bands, snap.CoreZone, snap.AvgFunding)
	direction, confidence := analysisDirectionAndConfidence(shortRiskScore, longRiskScore)
	title, bias := analysisOverviewHeadline(shortRiskScore, longRiskScore)
	return analysisScoreOutcome{
		ShortRisk:  shortRiskScore,
		LongRisk:   longRiskScore,
		Direction:  direction,
		Confidence: confidence,
		Title:      title,
		Bias:       bias,
	}
}

func analysisScoreFromSnapshotComposite(history []analysisScoreSnapshot, idx int) analysisScoreOutcome {
	snap := history[idx]
	shortRiskScore, longRiskScore := analysisBaseRiskScores(snap.CurrentPrice, snap.Bands, snap.CoreZone, snap.AvgFunding)
	shortRiskScore, longRiskScore = analysisApplyMomentumTilt(shortRiskScore, longRiskScore, analysisShortTermMomentumTiltFromSnapshots(history[:idx+1]))
	direction, confidence := analysisDirectionAndConfidence(shortRiskScore, longRiskScore)
	title, bias := analysisOverviewHeadline(shortRiskScore, longRiskScore)
	return analysisScoreOutcome{
		ShortRisk:  shortRiskScore,
		LongRisk:   longRiskScore,
		Direction:  direction,
		Confidence: confidence,
		Title:      title,
		Bias:       bias,
	}
}

func analysisShortTermMomentumTiltFromSnapshots(history []analysisScoreSnapshot) float64 {
	if len(history) == 0 {
		return 0
	}
	latest := history[len(history)-1]
	if latest.CurrentPrice <= 0 {
		return 0
	}
	push20 := analysisBandPushScore(latest.UpByBand[20], latest.DownByBand[20])
	push50 := analysisBandPushScore(latest.UpByBand[50], latest.DownByBand[50])
	pushTilt := clamp(push20*0.20+push50*0.08, -18, 18)

	change5m := analysisSnapshotPriceChangePctFromSnapshots(history, 5)
	change15m := analysisSnapshotPriceChangePctFromSnapshots(history, 15)
	change30m := analysisSnapshotPriceChangePctFromSnapshots(history, 30)
	priceTilt := clamp(change5m*10+change15m*7+change30m*5, -24, 24)
	return clamp(pushTilt+priceTilt, -24, 24)
}

func analysisSnapshotPriceChangePctFromSnapshots(history []analysisScoreSnapshot, lookbackMin int) float64 {
	if len(history) < 2 || lookbackMin <= 0 {
		return 0
	}
	latest := history[len(history)-1]
	targetTS := latest.TS - int64(lookbackMin)*60*1000
	pastPrice := 0.0
	for i := len(history) - 2; i >= 0; i-- {
		if history[i].TS <= targetTS && history[i].CurrentPrice > 0 {
			pastPrice = history[i].CurrentPrice
			break
		}
	}
	if pastPrice <= 0 {
		for i := len(history) - 2; i >= 0; i-- {
			if history[i].CurrentPrice > 0 {
				pastPrice = history[i].CurrentPrice
				break
			}
		}
	}
	if pastPrice <= 0 {
		return 0
	}
	return ((latest.CurrentPrice - pastPrice) / pastPrice) * 100
}
