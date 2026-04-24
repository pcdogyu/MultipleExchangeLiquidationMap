package liqmap

import (
	"math"
	"time"
)

func windowCutoff(now time.Time, window int) int64 {
	if window == windowIntraday {
		return intradayCutoff(now).UnixMilli()
	}
	if window <= 0 {
		window = 1
	}
	return now.Add(-time.Duration(window) * 24 * time.Hour).UnixMilli()
}

func intradayCutoff(now time.Time) time.Time {
	loc := now.Location()
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		// fallback: use fixed summer open time in local zone
		t := now.In(loc)
		return time.Date(t.Year(), t.Month(), t.Day(), 8, 0, 0, 0, loc)
	}
	t := now.In(loc)
	today8 := time.Date(t.Year(), t.Month(), t.Day(), 8, 0, 0, 0, loc)
	yesterday8 := today8.Add(-24 * time.Hour)

	nowNY := now.In(ny)
	// US market open anchor (per UI requirement): 9:00 ET -> 21:00 (DST) / 22:00 (standard) in China time.
	todayOpenLocal := time.Date(nowNY.Year(), nowNY.Month(), nowNY.Day(), 9, 0, 0, 0, ny).In(loc)
	yesterdayOpenLocal := time.Date(nowNY.Year(), nowNY.Month(), nowNY.Day()-1, 9, 0, 0, 0, ny).In(loc)

	candidates := []time.Time{today8, yesterday8, todayOpenLocal, yesterdayOpenLocal}
	best := time.Time{}
	for _, c := range candidates {
		if c.After(now) {
			continue
		}
		if best.IsZero() || c.After(best) {
			best = c
		}
	}
	if best.IsZero() || best.After(now) {
		return yesterday8
	}
	return best
}

func lookbackMinForWindow(now time.Time, windowDays int) int {
	if windowDays == windowIntraday {
		start := intradayCutoff(now)
		if start.After(now) {
			start = now.Add(-1 * time.Hour)
		}
		mins := int(math.Ceil(now.Sub(start).Minutes()))
		if mins < 60 {
			mins = 60
		}
		if mins > 43200 {
			mins = 43200
		}
		return mins
	}
	if windowDays <= 0 {
		windowDays = 1
	}
	mins := windowDays * 1440
	if mins < 60 {
		mins = 60
	}
	if mins > 43200 {
		mins = 43200
	}
	return mins
}
