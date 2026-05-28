package db

import (
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"time"
)

const (
	DefaultRetentionWindow = 14 * 24 * time.Hour
	DefaultCleanupInterval = 8 * time.Hour
)

type CleanupSummary struct {
	CutoffMS    int64
	DeletedRows int64
	ByTable     map[string]int64
}

func (s CleanupSummary) DetailString() string {
	if len(s.ByTable) == 0 {
		return ""
	}
	keys := make([]string, 0, len(s.ByTable))
	for key, value := range s.ByTable {
		if value > 0 {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	if len(keys) == 0 {
		return ""
	}
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, fmt.Sprintf("%s=%d", key, s.ByTable[key]))
	}
	return strings.Join(parts, ", ")
}

func CleanupExpiredData(db *sql.DB, now time.Time, retention time.Duration) (CleanupSummary, error) {
	if retention <= 0 {
		retention = DefaultRetentionWindow
	}
	cutoffMS := now.Add(-retention).UnixMilli()
	summary := CleanupSummary{
		CutoffMS: cutoffMS,
		ByTable:  map[string]int64{},
	}
	steps := []struct {
		name string
		stmt string
		args []any
	}{
		{name: "band_reports", stmt: `DELETE FROM band_reports WHERE report_ts < ?`, args: []any{cutoffMS}},
		{name: "liquidation_events", stmt: `DELETE FROM liquidation_events WHERE event_ts < ? AND inserted_ts < ?`, args: []any{cutoffMS, cutoffMS}},
		{name: "longest_bar_reports", stmt: `DELETE FROM longest_bar_reports WHERE report_ts < ?`, args: []any{cutoffMS}},
		{name: "market_state", stmt: `DELETE FROM market_state WHERE updated_ts < ?`, args: []any{cutoffMS}},
		{name: "model_liqmap_snapshots", stmt: `DELETE FROM model_liqmap_snapshots WHERE generated_at < ?`, args: []any{cutoffMS}},
		{name: "market_info_snapshots", stmt: `DELETE FROM market_info_snapshots WHERE generated_at < ? AND refreshed_at < ?`, args: []any{cutoffMS, cutoffMS}},
		{name: "oi_snapshots", stmt: `DELETE FROM oi_snapshots WHERE updated_ts < ?`, args: []any{cutoffMS}},
		{name: "price_wall_events", stmt: `DELETE FROM price_wall_events WHERE event_ts < ? AND inserted_ts < ?`, args: []any{cutoffMS, cutoffMS}},
		{name: "webdatasource_points", stmt: `DELETE FROM webdatasource_points WHERE captured_at < ?`, args: []any{cutoffMS}},
		{name: "webdatasource_runs", stmt: `DELETE FROM webdatasource_runs WHERE started_at < ? AND finished_at < ?`, args: []any{cutoffMS, cutoffMS}},
		{name: "webdatasource_snapshots", stmt: `DELETE FROM webdatasource_snapshots WHERE captured_at < ?`, args: []any{cutoffMS}},
		{name: "telegram_send_history", stmt: `DELETE FROM telegram_send_history WHERE sent_at < ?`, args: []any{cutoffMS}},
		{name: "analysis_direction_signals", stmt: `DELETE FROM analysis_direction_signals WHERE signal_ts < ? AND analysis_generated_at < ?`, args: []any{cutoffMS, cutoffMS}},
		{name: "analysis_liquidation_backtest_signals", stmt: `DELETE FROM analysis_liquidation_backtest_signals WHERE signal_ts < ? AND generated_at < ?`, args: []any{cutoffMS, cutoffMS}},
		{name: "webdatasource_points_orphans", stmt: `DELETE FROM webdatasource_points WHERE snapshot_id NOT IN (SELECT id FROM webdatasource_snapshots)`, args: nil},
	}

	tx, err := db.Begin()
	if err != nil {
		return summary, err
	}
	defer func() {
		_ = tx.Rollback()
	}()

	for _, step := range steps {
		res, err := tx.Exec(step.stmt, step.args...)
		if err != nil {
			if isMissingSQLiteObject(err) {
				summary.ByTable[step.name] = 0
				continue
			}
			return summary, err
		}
		rows, err := res.RowsAffected()
		if err != nil {
			return summary, err
		}
		summary.ByTable[step.name] = rows
		summary.DeletedRows += rows
	}

	if err := tx.Commit(); err != nil {
		return summary, err
	}
	_ = execWithBusyRetry(db, `PRAGMA wal_checkpoint(PASSIVE);`)
	return summary, nil
}

func isMissingSQLiteObject(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "no such table") || strings.Contains(msg, "no such column")
}
