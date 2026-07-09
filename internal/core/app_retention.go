package liqmap

import (
	"context"
	"log"
	"strconv"
	"strings"
	"time"

	dbplatform "multipleexchangeliquidationmap/internal/platform/db"
)

func (a *App) startDataRetention(ctx context.Context) {
	go func() {
		if dataRetentionCleanupOnStart() {
			a.runDataRetention()
		}

		interval := dataRetentionCleanupInterval()
		next := nextDataRetentionCleanupTime(time.Now(), interval)
		timer := time.NewTimer(time.Until(next))
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			a.runDataRetention()
		}

		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				a.runDataRetention()
			}
		}
	}()
}

func (a *App) runDataRetention() {
	summary, err := dbplatform.CleanupExpiredData(a.db, time.Now(), dbplatform.DefaultRetentionWindow)
	if err != nil {
		log.Printf("db retention cleanup failed: %v", err)
		return
	}
	if summary.DeletedRows == 0 && !a.debug {
		return
	}
	details := summary.DetailString()
	if details == "" {
		log.Printf("db retention cleanup completed: cutoff_ms=%d deleted=%d", summary.CutoffMS, summary.DeletedRows)
		return
	}
	log.Printf("db retention cleanup completed: cutoff_ms=%d deleted=%d details=%s", summary.CutoffMS, summary.DeletedRows, details)
}

func dataRetentionCleanupOnStart() bool {
	raw := strings.ToLower(strings.TrimSpace(getenv("RETENTION_CLEANUP_ON_START", "0")))
	return raw == "1" || raw == "true" || raw == "yes" || raw == "on"
}

func dataRetentionCleanupInterval() time.Duration {
	raw := strings.TrimSpace(getenv("RETENTION_CLEANUP_INTERVAL_HOURS", ""))
	if raw == "" {
		return dbplatform.DefaultCleanupInterval
	}
	hours, err := strconv.Atoi(raw)
	if err != nil || hours <= 0 {
		return dbplatform.DefaultCleanupInterval
	}
	return time.Duration(hours) * time.Hour
}

func nextDataRetentionCleanupTime(now time.Time, interval time.Duration) time.Time {
	if interval <= 0 {
		interval = dbplatform.DefaultCleanupInterval
	}
	midnight := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	elapsed := now.Sub(midnight)
	steps := int(elapsed/interval) + 1
	return midnight.Add(time.Duration(steps) * interval)
}
