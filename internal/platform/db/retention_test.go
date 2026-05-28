package db

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func TestCleanupExpiredDataRemovesOldRowsAndKeepsRecentRows(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "retention.db")
	conn, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer conn.Close()

	if err := Configure(conn); err != nil {
		t.Fatalf("configure db: %v", err)
	}
	if err := Init(conn); err != nil {
		t.Fatalf("init db: %v", err)
	}

	now := time.Date(2026, 5, 28, 12, 0, 0, 0, time.UTC)
	cutoff := now.Add(-DefaultRetentionWindow).UnixMilli()
	oldTS := cutoff - int64((2*time.Hour)/time.Millisecond)
	newTS := cutoff + int64((2*time.Hour)/time.Millisecond)

	mustExec := func(stmt string, args ...any) {
		t.Helper()
		if _, err := conn.Exec(stmt, args...); err != nil {
			t.Fatalf("exec %q failed: %v", stmt, err)
		}
	}
	countRows := func(table string) int {
		t.Helper()
		var count int
		if err := conn.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&count); err != nil {
			t.Fatalf("count rows from %s: %v", table, err)
		}
		return count
	}

	mustExec(`INSERT INTO band_reports(report_ts, symbol, current_price, band, up_price, up_notional_usd, down_price, down_notional_usd) VALUES(?, 'ETHUSDT', 100, 10, 110, 1000, 90, 900)`, oldTS)
	mustExec(`INSERT INTO band_reports(report_ts, symbol, current_price, band, up_price, up_notional_usd, down_price, down_notional_usd) VALUES(?, 'ETHUSDT', 100, 10, 110, 1000, 90, 900)`, newTS)
	mustExec(`INSERT INTO liquidation_events(exchange, symbol, side, raw_side, qty, price, mark_price, notional_usd, event_ts, inserted_ts) VALUES('binance', 'ETHUSDT', 'long', 'BUY', 1, 100, 100, 100, ?, ?)`, oldTS, oldTS)
	mustExec(`INSERT INTO liquidation_events(exchange, symbol, side, raw_side, qty, price, mark_price, notional_usd, event_ts, inserted_ts) VALUES('binance', 'ETHUSDT', 'short', 'SELL', 1, 100, 100, 100, ?, ?)`, newTS, newTS)
	mustExec(`INSERT INTO market_info_snapshots(exchange, symbol, period, limit_count, generated_at, refreshed_at, status, error_message, payload_json) VALUES('binance', 'ETHUSDT', '5m', 100, ?, ?, 'ready', '', '{}')`, oldTS, oldTS)
	mustExec(`INSERT INTO market_info_snapshots(exchange, symbol, period, limit_count, generated_at, refreshed_at, status, error_message, payload_json) VALUES('okx', 'ETHUSDT', '5m', 100, ?, ?, 'ready', '', '{}')`, newTS, newTS)
	mustExec(`INSERT INTO telegram_send_history(sent_at, send_mode, group_index, group_name, status, error_text) VALUES(?, 'photo', 1, 'g1', 'ok', '')`, oldTS)
	mustExec(`INSERT INTO telegram_send_history(sent_at, send_mode, group_index, group_name, status, error_text) VALUES(?, 'photo', 1, 'g1', 'ok', '')`, newTS)
	mustExec(`INSERT INTO analysis_direction_signals(signal_ts, symbol, source_group, direction, confidence, signal_price, analysis_generated_at, headline, summary, verify_horizon_min) VALUES(?, 'ETHUSDT', 1, 'up', 0.8, 100, ?, 'h', 's', 5)`, oldTS, oldTS)
	mustExec(`INSERT INTO analysis_direction_signals(signal_ts, symbol, source_group, direction, confidence, signal_price, analysis_generated_at, headline, summary, verify_horizon_min) VALUES(?, 'ETHUSDT', 1, 'up', 0.8, 100, ?, 'h', 's', 5)`, newTS, newTS)
	mustExec(`INSERT INTO analysis_liquidation_backtest_signals(signal_ts, symbol, source_group, direction, confidence, signal_price, analysis_generated_at, headline, summary, verify_horizon_min, signal_action, signal_side, signal_label, second_factor_key, second_factor_label, generated_at) VALUES(?, 'ETHUSDT', 1, 'up', 0.8, 100, ?, 'h', 's', 5, 'open', 'long', 'label', 'k', 'l', ?)`, oldTS, oldTS, oldTS)
	mustExec(`INSERT INTO analysis_liquidation_backtest_signals(signal_ts, symbol, source_group, direction, confidence, signal_price, analysis_generated_at, headline, summary, verify_horizon_min, signal_action, signal_side, signal_label, second_factor_key, second_factor_label, generated_at) VALUES(?, 'ETHUSDT', 1, 'up', 0.8, 100, ?, 'h', 's', 5, 'open', 'long', 'label', 'k', 'l', ?)`, newTS, newTS, newTS)
	mustExec(`INSERT INTO webdatasource_snapshots(id, symbol, window_days, captured_at, range_low, range_high, payload_json) VALUES(1, 'ETHUSDT', 14, ?, 0, 0, '{}')`, oldTS)
	mustExec(`INSERT INTO webdatasource_snapshots(id, symbol, window_days, captured_at, range_low, range_high, payload_json) VALUES(2, 'ETHUSDT', 14, ?, 0, 0, '{}')`, newTS)
	mustExec(`INSERT INTO webdatasource_points(snapshot_id, symbol, window_days, side, exchange, price, liq_value, captured_at) VALUES(1, 'ETHUSDT', 14, 'long', 'binance', 100, 1000, ?)`, oldTS)
	mustExec(`INSERT INTO webdatasource_points(snapshot_id, symbol, window_days, side, exchange, price, liq_value, captured_at) VALUES(2, 'ETHUSDT', 14, 'long', 'binance', 100, 1000, ?)`, newTS)
	mustExec(`INSERT INTO webdatasource_points(snapshot_id, symbol, window_days, side, exchange, price, liq_value, captured_at) VALUES(999, 'ETHUSDT', 14, 'long', 'binance', 100, 1000, ?)`, newTS)

	summary, err := CleanupExpiredData(conn, now, DefaultRetentionWindow)
	if err != nil {
		t.Fatalf("cleanup expired data: %v", err)
	}
	if summary.CutoffMS != cutoff {
		t.Fatalf("expected cutoff %d, got %d", cutoff, summary.CutoffMS)
	}
	if summary.DeletedRows < 7 {
		t.Fatalf("expected deleted rows to be tracked, got %d", summary.DeletedRows)
	}

	if got := countRows("band_reports"); got != 1 {
		t.Fatalf("expected 1 band_reports row, got %d", got)
	}
	if got := countRows("liquidation_events"); got != 1 {
		t.Fatalf("expected 1 liquidation_events row, got %d", got)
	}
	if got := countRows("market_info_snapshots"); got != 1 {
		t.Fatalf("expected 1 market_info_snapshots row, got %d", got)
	}
	if got := countRows("telegram_send_history"); got != 1 {
		t.Fatalf("expected 1 telegram_send_history row, got %d", got)
	}
	if got := countRows("analysis_direction_signals"); got != 1 {
		t.Fatalf("expected 1 analysis_direction_signals row, got %d", got)
	}
	if got := countRows("analysis_liquidation_backtest_signals"); got != 1 {
		t.Fatalf("expected 1 analysis_liquidation_backtest_signals row, got %d", got)
	}
	if got := countRows("webdatasource_snapshots"); got != 1 {
		t.Fatalf("expected 1 webdatasource_snapshots row, got %d", got)
	}
	if got := countRows("webdatasource_points"); got != 1 {
		t.Fatalf("expected orphaned and old webdatasource_points rows removed, got %d", got)
	}
}
