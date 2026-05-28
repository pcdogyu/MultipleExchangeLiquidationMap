package liqmap

import (
	"context"
	"log"
	"time"

	dbplatform "multipleexchangeliquidationmap/internal/platform/db"
)

func (a *App) startDataRetention(ctx context.Context) {
	go func() {
		a.runDataRetention()

		ticker := time.NewTicker(dbplatform.DefaultCleanupInterval)
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
