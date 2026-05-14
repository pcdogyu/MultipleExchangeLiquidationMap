package liqmap

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

func parseClockMinutes(raw string) (int, bool) {
	parts := strings.Split(strings.TrimSpace(raw), ":")
	if len(parts) != 2 {
		return 0, false
	}
	hour, errHour := strconv.Atoi(strings.TrimSpace(parts[0]))
	minute, errMinute := strconv.Atoi(strings.TrimSpace(parts[1]))
	if errHour != nil || errMinute != nil {
		return 0, false
	}
	if hour < 0 || hour > 23 || minute < 0 || minute > 59 {
		return 0, false
	}
	return hour*60 + minute, true
}

func splitTimeExpr(raw string) []string {
	return strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ';' || r == '\n'
	})
}

func matchSimpleTimeExpr(now time.Time, expr string) (bool, bool) {
	expr = strings.TrimSpace(expr)
	if expr == "" {
		return true, true
	}
	if strings.HasPrefix(strings.ToLower(expr), "regex:") {
		return false, false
	}
	nowMin := now.Hour()*60 + now.Minute()
	parts := splitTimeExpr(expr)
	if len(parts) == 0 {
		return false, false
	}
	parsedAny := false
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		bounds := strings.SplitN(part, "-", 2)
		if len(bounds) != 2 {
			return false, false
		}
		startMin, okStart := parseClockMinutes(bounds[0])
		endMin, okEnd := parseClockMinutes(bounds[1])
		if !okStart || !okEnd {
			return false, false
		}
		parsedAny = true
		if startMin <= endMin {
			if nowMin >= startMin && nowMin <= endMin {
				return true, true
			}
			continue
		}
		if nowMin >= startMin || nowMin <= endMin {
			return true, true
		}
	}
	return false, parsedAny
}

func compileTimeExprRegex(expr string) (*regexp.Regexp, error) {
	pattern := strings.TrimSpace(expr)
	if strings.HasPrefix(strings.ToLower(pattern), "regex:") {
		pattern = strings.TrimSpace(pattern[len("regex:"):])
	}
	if pattern == "" {
		return nil, nil
	}
	return regexp.Compile(pattern)
}

func validateWorkTimeExpr(expr string) error {
	expr = normalizeQuotedInput(expr)
	if expr == "" {
		return nil
	}
	if _, ok := matchSimpleTimeExpr(time.Now(), expr); ok {
		return nil
	}
	if _, err := compileTimeExprRegex(expr); err != nil {
		return fmt.Errorf("work time range must be HH:MM-HH:MM[,...] or regex / regex:<pattern>: %w", err)
	}
	return nil
}

func isWorkTime(now time.Time, expr string) bool {
	expr = normalizeQuotedInput(expr)
	if expr == "" {
		return true
	}
	if match, ok := matchSimpleTimeExpr(now, expr); ok {
		return match
	}
	re, err := compileTimeExprRegex(expr)
	if err != nil || re == nil {
		return true
	}
	candidates := []string{
		now.Format("15:04"),
		now.Format("Mon 15:04"),
		now.Format("Monday 15:04"),
		now.Format("2006-01-02 15:04"),
		now.Format("2006-01-02 Mon 15:04"),
	}
	for _, candidate := range candidates {
		if re.MatchString(candidate) {
			return true
		}
	}
	return false
}

func effectiveNotifyInterval(settings ChannelSettings, now time.Time) int {
	workInterval := settings.NotifyWorkIntervalMin
	if workInterval <= 0 {
		workInterval = settings.NotifyIntervalMin
	}
	if workInterval <= 0 {
		workInterval = 15
	}
	offInterval := settings.NotifyOffIntervalMin
	if offInterval <= 0 {
		offInterval = workInterval
	}
	if isWorkTime(now, settings.WorkTimeExpr) {
		return workInterval
	}
	return offInterval
}
