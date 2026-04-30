package liqmap

import (
	"database/sql"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
)

func (a *App) loadModelConfig() ModelConfig {
	cfg := ModelConfig{
		LookbackMin:           a.getSettingInt("model_lookback_min", defaultLookbackMin),
		BucketMin:             a.getSettingInt("model_bucket_min", defaultBucketMin),
		PriceStep:             a.getSettingFloat("model_price_step", defaultPriceStep),
		PriceRange:            a.getSettingFloat("model_price_range", defaultPriceRange),
		LeverageCSV:           fixedLeverageCSV,
		WeightCSV:             normalizeCSVInput(a.getSetting("model_leverage_weights")),
		WeightCSVBinance:      normalizeCSVInput(a.getSetting("model_leverage_weights_binance")),
		WeightCSVOKX:          normalizeCSVInput(a.getSetting("model_leverage_weights_okx")),
		WeightCSVBybit:        normalizeCSVInput(a.getSetting("model_leverage_weights_bybit")),
		MaintMargin:           a.getSettingFloat("model_mm", 0.005),
		MaintMarginCSV:        normalizeCSVInput(a.getSetting("model_mm_csv")),
		MaintMarginCSVBinance: normalizeCSVInput(a.getSetting("model_mm_csv_binance")),
		MaintMarginCSVOKX:     normalizeCSVInput(a.getSetting("model_mm_csv_okx")),
		MaintMarginCSVBybit:   normalizeCSVInput(a.getSetting("model_mm_csv_bybit")),
		FundingScale:          a.getSettingFloat("model_funding_scale", 7000),
		FundingScaleCSV:       normalizeCSVInput(a.getSetting("model_funding_scale_csv")),
		IntensityScale:        a.getSettingFloat("model_intensity_scale", 1.0),
		DecayK:                a.getSettingFloat("model_decay_k", 2.2),
		NeighborShare:         a.getSettingFloat("model_neighbor_share", 0.28),
	}
	if cfg.WeightCSV == "" {
		cfg.WeightCSV = "0.142857,0.142857,0.142857,0.142857,0.142857,0.142857,0.142857"
	}
	// If user didn't specify per-leverage MM, auto-generate a mid-leverage-heavier
	// MM schedule so the 20x short liquidation band is closer to common Coinglass
	// levels (e.g. ~2271 when mark~2185).
	if strings.TrimSpace(cfg.MaintMarginCSV) == "" {
		levs := parseCSVFloats(cfg.LeverageCSV)
		if len(levs) > 0 {
			cfg.MaintMarginCSV = autoMaintMarginCSV(levs, cfg.MaintMargin)
		}
	}
	if cfg.IntensityScale <= 0 || cfg.IntensityScale > 50 {
		cfg.IntensityScale = 1.0
	}
	return cfg
}

func (a *App) saveModelConfig(req ModelConfig) error {
	req.LeverageCSV = fixedLeverageCSV
	req.WeightCSV = normalizeCSVInput(req.WeightCSV)
	req.WeightCSVBinance = normalizeCSVInput(req.WeightCSVBinance)
	req.WeightCSVOKX = normalizeCSVInput(req.WeightCSVOKX)
	req.WeightCSVBybit = normalizeCSVInput(req.WeightCSVBybit)
	req.MaintMarginCSV = normalizeCSVInput(req.MaintMarginCSV)
	req.MaintMarginCSVBinance = normalizeCSVInput(req.MaintMarginCSVBinance)
	req.MaintMarginCSVOKX = normalizeCSVInput(req.MaintMarginCSVOKX)
	req.MaintMarginCSVBybit = normalizeCSVInput(req.MaintMarginCSVBybit)
	req.FundingScaleCSV = normalizeCSVInput(req.FundingScaleCSV)
	if req.IntensityScale <= 0 || req.IntensityScale > 50 {
		req.IntensityScale = 1.0
	}
	if req.LookbackMin < 60 || req.LookbackMin > 43200 {
		req.LookbackMin = defaultLookbackMin
	}
	if req.BucketMin < 1 || req.BucketMin > 30 {
		req.BucketMin = defaultBucketMin
	}
	if req.PriceStep < 1 || req.PriceStep > 50 {
		req.PriceStep = defaultPriceStep
	}
	if req.PriceRange < 100 || req.PriceRange > 1000 {
		req.PriceRange = defaultPriceRange
	}
	if req.MaintMarginCSV != "" && !(req.MaintMargin > 0) {
		if xs := parseCSVFloats(req.MaintMarginCSV); len(xs) > 0 {
			req.MaintMargin = xs[0]
		}
	}
	if req.FundingScaleCSV != "" && !(req.FundingScale > 0) {
		if xs := parseCSVFloats(req.FundingScaleCSV); len(xs) > 0 {
			req.FundingScale = xs[0]
		}
	}
	if req.MaintMargin <= 0 || req.MaintMargin > 0.02 {
		req.MaintMargin = 0.005
	}
	if req.FundingScale < 1000 || req.FundingScale > 20000 {
		req.FundingScale = 7000
	}
	if req.DecayK <= 0.1 || req.DecayK > 10 {
		req.DecayK = 2.2
	}
	if req.NeighborShare < 0 || req.NeighborShare > 1 {
		req.NeighborShare = 0.28
	}
	levs := parseCSVFloats(req.LeverageCSV)
	if len(levs) != 7 {
		return BadRequestError{Message: "fixed leverage tiers expected"}
	}
	ws := parseCSVNonNegFloats(req.WeightCSV)
	if len(ws) != len(levs) {
		return BadRequestError{Message: "weight_csv must match leverage count"}
	}
	sumW := 0.0
	for _, v := range ws {
		sumW += v
	}
	if !(sumW > 0) {
		return BadRequestError{Message: "weight_csv sum must be > 0"}
	}
	mmList := parseCSVFloats(req.MaintMarginCSV)
	for _, v := range mmList {
		if v <= 0 || v > 0.02 {
			return BadRequestError{Message: "maint_margin_csv values must be in (0, 0.02]"}
		}
	}
	if len(mmList) != len(levs) {
		return BadRequestError{Message: "maint_margin_csv must match leverage count"}
	}
	fsList := parseCSVFloats(req.FundingScaleCSV)
	for _, v := range fsList {
		if v < 1000 || v > 20000 {
			return BadRequestError{Message: "funding_scale_csv values must be in [1000, 20000]"}
		}
	}
	if len(fsList) != len(levs) {
		return BadRequestError{Message: "funding_scale_csv must match leverage count"}
	}

	validateWeights := func(raw string) error {
		if strings.TrimSpace(raw) == "" {
			return nil
		}
		xs := parseCSVNonNegFloats(raw)
		if len(xs) != len(levs) {
			return fmt.Errorf("weight_csv must match leverage count")
		}
		sum := 0.0
		for _, v := range xs {
			sum += v
		}
		if !(sum > 0) {
			return fmt.Errorf("weight_csv sum must be > 0")
		}
		return nil
	}
	validateMM := func(raw string) error {
		if strings.TrimSpace(raw) == "" {
			return nil
		}
		xs := parseCSVFloats(raw)
		if len(xs) != len(levs) {
			return fmt.Errorf("maint_margin_csv must match leverage count")
		}
		for _, v := range xs {
			if v <= 0 || v > 0.02 {
				return fmt.Errorf("maint_margin_csv values must be in (0, 0.02]")
			}
		}
		return nil
	}
	if err := validateWeights(req.WeightCSVBinance); err != nil {
		return BadRequestError{Message: "weight_csv_binance invalid: " + err.Error()}
	}
	if err := validateWeights(req.WeightCSVOKX); err != nil {
		return BadRequestError{Message: "weight_csv_okx invalid: " + err.Error()}
	}
	if err := validateWeights(req.WeightCSVBybit); err != nil {
		return BadRequestError{Message: "weight_csv_bybit invalid: " + err.Error()}
	}
	if err := validateMM(req.MaintMarginCSVBinance); err != nil {
		return BadRequestError{Message: "maint_margin_csv_binance invalid: " + err.Error()}
	}
	if err := validateMM(req.MaintMarginCSVOKX); err != nil {
		return BadRequestError{Message: "maint_margin_csv_okx invalid: " + err.Error()}
	}
	if err := validateMM(req.MaintMarginCSVBybit); err != nil {
		return BadRequestError{Message: "maint_margin_csv_bybit invalid: " + err.Error()}
	}
	if err := a.setSetting("model_lookback_min", strconv.Itoa(req.LookbackMin)); err != nil {
		return err
	}
	if err := a.setSetting("model_bucket_min", strconv.Itoa(req.BucketMin)); err != nil {
		return err
	}
	if err := a.setSetting("model_price_step", fmt.Sprintf("%.4f", req.PriceStep)); err != nil {
		return err
	}
	if err := a.setSetting("model_price_range", fmt.Sprintf("%.2f", req.PriceRange)); err != nil {
		return err
	}
	if err := a.setSetting("model_leverage_weights", strings.TrimSpace(req.WeightCSV)); err != nil {
		return err
	}
	if err := a.setSetting("model_leverage_weights_binance", strings.TrimSpace(req.WeightCSVBinance)); err != nil {
		return err
	}
	if err := a.setSetting("model_leverage_weights_okx", strings.TrimSpace(req.WeightCSVOKX)); err != nil {
		return err
	}
	if err := a.setSetting("model_leverage_weights_bybit", strings.TrimSpace(req.WeightCSVBybit)); err != nil {
		return err
	}
	if err := a.setSetting("model_mm", fmt.Sprintf("%.6f", req.MaintMargin)); err != nil {
		return err
	}
	if err := a.setSetting("model_mm_csv", strings.TrimSpace(req.MaintMarginCSV)); err != nil {
		return err
	}
	if err := a.setSetting("model_mm_csv_binance", strings.TrimSpace(req.MaintMarginCSVBinance)); err != nil {
		return err
	}
	if err := a.setSetting("model_mm_csv_okx", strings.TrimSpace(req.MaintMarginCSVOKX)); err != nil {
		return err
	}
	if err := a.setSetting("model_mm_csv_bybit", strings.TrimSpace(req.MaintMarginCSVBybit)); err != nil {
		return err
	}
	if err := a.setSetting("model_funding_scale", fmt.Sprintf("%.2f", req.FundingScale)); err != nil {
		return err
	}
	if err := a.setSetting("model_funding_scale_csv", strings.TrimSpace(req.FundingScaleCSV)); err != nil {
		return err
	}
	if err := a.setSetting("model_intensity_scale", fmt.Sprintf("%.4f", req.IntensityScale)); err != nil {
		return err
	}
	if err := a.setSetting("model_decay_k", fmt.Sprintf("%.4f", req.DecayK)); err != nil {
		return err
	}
	if err := a.setSetting("model_neighbor_share", fmt.Sprintf("%.4f", req.NeighborShare)); err != nil {
		return err
	}
	if err := a.setSetting("model_config_rev", strconv.FormatInt(time.Now().UnixMilli(), 10)); err != nil {
		return err
	}
	return nil
}

type modelFitSuggestion struct {
	Exchange   string  `json:"exchange"`
	Count      int     `json:"count"`
	Notional   float64 `json:"notional_usd"`
	WeightCSV  string  `json:"weight_csv"`
	MMCSV      string  `json:"maint_margin_csv"`
	FitBaseMM  float64 `json:"fit_base_mm"`
	FitK       float64 `json:"fit_k"`
	WeightedSE float64 `json:"weighted_se"`
}

func (a *App) runModelFit(hours, minEvents int, exFilter, mode string) (map[string]any, error) {
	cutoff := time.Now().Add(-time.Duration(hours) * time.Hour).UnixMilli()

	cfg := a.loadModelConfig()
	levs := parseCSVFloats(cfg.LeverageCSV)
	if len(levs) == 0 {
		levs = parseCSVFloats(fixedLeverageCSV)
	}
	if len(levs) == 0 {
		return nil, BadRequestError{Message: "no leverage tiers"}
	}

	type fitEvt struct {
		ex       string
		price    float64
		mark     float64
		notional float64
	}
	var rows *sql.Rows
	var err error
	if exFilter != "" {
		rows, err = a.db.Query(`SELECT LOWER(exchange), price, mark_price, notional_usd
			FROM liquidation_events
			WHERE symbol=? AND (event_ts>=? OR inserted_ts>=?) AND LOWER(exchange)=?`,
			defaultSymbol, cutoff, cutoff, exFilter)
	} else {
		rows, err = a.db.Query(`SELECT LOWER(exchange), price, mark_price, notional_usd
			FROM liquidation_events
			WHERE symbol=? AND (event_ts>=? OR inserted_ts>=?)`, defaultSymbol, cutoff, cutoff)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	byEx := map[string][]fitEvt{}
	counts := map[string]map[string]any{}
	totalCnt := 0
	for rows.Next() {
		var ex string
		var price, mark, notional float64
		if err := rows.Scan(&ex, &price, &mark, &notional); err != nil {
			continue
		}
		totalCnt++
		if ex == "" || !(price > 0) || !(mark > 0) || !(notional > 0) {
			continue
		}
		byEx[ex] = append(byEx[ex], fitEvt{ex: ex, price: price, mark: mark, notional: notional})
		if _, ok := counts[ex]; !ok {
			counts[ex] = map[string]any{"count": 0, "notional_usd": 0.0}
		}
		counts[ex]["count"] = int(counts[ex]["count"].(int)) + 1
		counts[ex]["notional_usd"] = counts[ex]["notional_usd"].(float64) + notional
	}

	fitOne := func(ex string, evts []fitEvt) (modelFitSuggestion, bool) {
		if len(evts) < minEvents {
			return modelFitSuggestion{}, false
		}
		totalNotional := 0.0
		for _, e := range evts {
			totalNotional += e.notional
		}
		bestBase := 0.0
		bestK := 0.0
		bestErr := math.Inf(1)

		for base := 0.002; base <= 0.010001; base += 0.0005 {
			for k := 0.00; k <= 0.20001; k += 0.01 {
				// quick guard: 1x tier must stay valid
				// For 1x leverage, mm can be much larger (up to nearly 1.0)
				// since distPred = 1 - mm, and typical liquidation distances are small
				if levs[0] == 1 {
					// Allow mm up to 0.5 for 1x leverage
					if base+k/levs[0] > 0.5 {
						continue
					}
				} else {
					// For higher leverage, use the original threshold
					if base+k/levs[0] > 0.02 {
						continue
					}
				}
				errSum := 0.0
				weightSum := 0.0
				for _, e := range evts {
					distObs := math.Abs(e.price-e.mark) / e.mark
					if !(distObs > 0) || distObs > 1.0 {
						continue
					}
					best := math.Inf(1)
					for _, lev := range levs {
						if !(lev > 0) {
							continue
						}
						mm := base + k/lev
						if mm <= 0 || mm > 0.02 {
							continue
						}
						distPred := 1/lev - mm
						if distPred <= 0 {
							continue
						}
						diff := distObs - distPred
						se := diff * diff
						if se < best {
							best = se
						}
					}
					if math.IsInf(best, 1) {
						continue
					}
					errSum += e.notional * best
					weightSum += e.notional
				}
				if !(weightSum > 0) {
					continue
				}
				if errSum < bestErr {
					bestErr = errSum
					bestBase = base
					bestK = k
				}
			}
		}
		if !isFinite(bestErr) {
			return modelFitSuggestion{}, false
		}

		weights := make([]float64, len(levs))
		for _, e := range evts {
			distObs := math.Abs(e.price-e.mark) / e.mark
			if !(distObs > 0) || distObs > 0.7 {
				continue
			}
			bestI := -1
			best := math.Inf(1)
			for i, lev := range levs {
				mm := bestBase + bestK/lev
				if mm <= 0 || mm > 0.02 {
					continue
				}
				distPred := 1/lev - mm
				if distPred <= 0 {
					continue
				}
				diff := distObs - distPred
				se := diff * diff
				if se < best {
					best = se
					bestI = i
				}
			}
			if bestI >= 0 {
				weights[bestI] += e.notional
			}
		}
		sumW := 0.0
		for _, v := range weights {
			sumW += v
		}
		if !(sumW > 0) {
			return modelFitSuggestion{}, false
		}
		for i := range weights {
			weights[i] /= sumW
		}
		mmParts := make([]string, 0, len(levs))
		wParts := make([]string, 0, len(levs))
		for i, lev := range levs {
			mm := bestBase + bestK/lev
			if mm <= 0 {
				mm = 0.0001
			}
			if mm > 0.02 {
				mm = 0.02
			}
			mmParts = append(mmParts, fmt.Sprintf("%.4f", mm))
			wParts = append(wParts, fmt.Sprintf("%.6f", weights[i]))
		}
		return modelFitSuggestion{
			Exchange:   ex,
			Count:      len(evts),
			Notional:   totalNotional,
			WeightCSV:  strings.Join(wParts, ","),
			MMCSV:      strings.Join(mmParts, ","),
			FitBaseMM:  bestBase,
			FitK:       bestK,
			WeightedSE: bestErr / math.Max(1, totalNotional),
		}, true
	}

	out := []modelFitSuggestion{}
	if exFilter != "" {
		evts := byEx[exFilter]
		if sug, ok := fitOne(exFilter, evts); ok {
			out = append(out, sug)
		}
	} else if mode == "global" {
		all := make([]fitEvt, 0, 4096)
		for _, evts := range byEx {
			all = append(all, evts...)
		}
		if sug, ok := fitOne("global", all); ok {
			out = append(out, sug)
		}
	} else {
		for ex, evts := range byEx {
			if sug, ok := fitOne(ex, evts); ok {
				out = append(out, sug)
			}
		}
		sort.Slice(out, func(i, j int) bool { return out[i].Notional > out[j].Notional })
	}
	return map[string]any{
		"symbol":      defaultSymbol,
		"hours":       hours,
		"min_events":  minEvents,
		"cutoff_ts":   cutoff,
		"raw_count":   totalCnt,
		"counts":      counts,
		"leverages":   levs,
		"suggestions": out,
	}, nil
}
