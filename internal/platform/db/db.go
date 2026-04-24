package db

import (
	"database/sql"
	"strings"
	"time"
)

func isSQLiteBusy(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "database is locked") || strings.Contains(msg, "sqlite_busy")
}

func execWithBusyRetry(db *sql.DB, stmt string, args ...any) error {
	var err error
	for attempt := 0; attempt < 6; attempt++ {
		_, err = db.Exec(stmt, args...)
		if !isSQLiteBusy(err) {
			return err
		}
		time.Sleep(time.Duration(150*(attempt+1)) * time.Millisecond)
	}
	return err
}

func Configure(db *sql.DB) error {
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(0)
	return execWithBusyRetry(db, `PRAGMA busy_timeout=5000;`)
}

func Init(db *sql.DB) error {
	stmts := []string{
		`PRAGMA journal_mode=WAL;`,
		`CREATE TABLE IF NOT EXISTS market_state (
			exchange TEXT NOT NULL,
			symbol TEXT NOT NULL,
			mark_price REAL,
			oi_qty REAL,
			oi_value_usd REAL,
			funding_rate REAL,
			long_short_ratio REAL,
			updated_ts INTEGER NOT NULL,
			PRIMARY KEY(exchange, symbol)
		);`,
		`CREATE TABLE IF NOT EXISTS oi_snapshots (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			exchange TEXT NOT NULL,
			symbol TEXT NOT NULL,
			mark_price REAL NOT NULL,
			oi_value_usd REAL NOT NULL,
			funding_rate REAL,
			long_short_ratio REAL,
			updated_ts INTEGER NOT NULL
		);`,
		`CREATE INDEX IF NOT EXISTS idx_oi_snapshots_symbol_ts ON oi_snapshots(symbol, updated_ts);`,
		`CREATE TABLE IF NOT EXISTS liquidation_events (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			exchange TEXT NOT NULL,
			symbol TEXT NOT NULL,
			side TEXT NOT NULL,
			raw_side TEXT,
			qty REAL NOT NULL,
			price REAL NOT NULL,
			mark_price REAL NOT NULL,
			notional_usd REAL NOT NULL,
			event_ts INTEGER NOT NULL,
			inserted_ts INTEGER NOT NULL
		);`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_liquidation_events_uniq
			ON liquidation_events(exchange, symbol, side, price, qty, event_ts);`,
		`CREATE INDEX IF NOT EXISTS idx_liquidation_events_symbol_ts ON liquidation_events(symbol, event_ts);`,
		`CREATE TABLE IF NOT EXISTS band_reports (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			report_ts INTEGER NOT NULL,
			symbol TEXT NOT NULL,
			current_price REAL NOT NULL,
			band INTEGER NOT NULL,
			up_price REAL NOT NULL,
			up_notional_usd REAL NOT NULL,
			down_price REAL NOT NULL,
			down_notional_usd REAL NOT NULL
		);`,
		`CREATE INDEX IF NOT EXISTS idx_band_reports_symbol_ts_band ON band_reports(symbol, report_ts, band);`,
		`CREATE TABLE IF NOT EXISTS longest_bar_reports (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			report_ts INTEGER NOT NULL,
			symbol TEXT NOT NULL,
			side TEXT NOT NULL,
			bucket_size REAL NOT NULL,
			bucket_price REAL NOT NULL,
			bucket_notional_usd REAL NOT NULL
		);`,
		`CREATE TABLE IF NOT EXISTS app_settings (
			key TEXT PRIMARY KEY,
			value TEXT NOT NULL
		);`,
		`CREATE TABLE IF NOT EXISTS model_liqmap_snapshots (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			symbol TEXT NOT NULL,
			window_days INTEGER NOT NULL,
			config_rev INTEGER NOT NULL,
			generated_at INTEGER NOT NULL,
			payload_json TEXT NOT NULL
		);`,
		`CREATE INDEX IF NOT EXISTS idx_model_liqmap_snapshots_key_ts
			ON model_liqmap_snapshots(symbol, window_days, config_rev, generated_at);`,
		`CREATE TABLE IF NOT EXISTS price_wall_events (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			side TEXT NOT NULL,
			price REAL NOT NULL,
			peak_notional_usd REAL NOT NULL,
			duration_ms INTEGER NOT NULL,
			event_ts INTEGER NOT NULL,
			mode TEXT NOT NULL DEFAULT 'weighted',
			inserted_ts INTEGER NOT NULL
		);`,
		`CREATE INDEX IF NOT EXISTS idx_price_wall_events_ts ON price_wall_events(event_ts);`,
		`CREATE TABLE IF NOT EXISTS webdatasource_settings (
			key TEXT PRIMARY KEY,
			value TEXT NOT NULL
		);`,
		`CREATE TABLE IF NOT EXISTS webdatasource_runs (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			started_at INTEGER NOT NULL,
			finished_at INTEGER NOT NULL,
			status TEXT NOT NULL,
			window_days INTEGER NOT NULL,
			error_message TEXT NOT NULL DEFAULT '',
			records_count INTEGER NOT NULL DEFAULT 0,
			source_meta_json TEXT NOT NULL DEFAULT ''
		);`,
		`CREATE INDEX IF NOT EXISTS idx_webdatasource_runs_started_at ON webdatasource_runs(started_at);`,
		`CREATE TABLE IF NOT EXISTS webdatasource_snapshots (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			symbol TEXT NOT NULL,
			window_days INTEGER NOT NULL,
			captured_at INTEGER NOT NULL,
			range_low REAL NOT NULL DEFAULT 0,
			range_high REAL NOT NULL DEFAULT 0,
			payload_json TEXT NOT NULL
		);`,
		`CREATE INDEX IF NOT EXISTS idx_webdatasource_snapshots_key_ts ON webdatasource_snapshots(symbol, window_days, captured_at);`,
		`CREATE TABLE IF NOT EXISTS webdatasource_points (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			snapshot_id INTEGER NOT NULL,
			symbol TEXT NOT NULL,
			window_days INTEGER NOT NULL,
			side TEXT NOT NULL,
			exchange TEXT NOT NULL,
			price REAL NOT NULL,
			liq_value REAL NOT NULL,
			captured_at INTEGER NOT NULL
		);`,
		`CREATE INDEX IF NOT EXISTS idx_webdatasource_points_snapshot_id ON webdatasource_points(snapshot_id);`,
		`CREATE TABLE IF NOT EXISTS telegram_send_history (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			sent_at INTEGER NOT NULL,
			send_mode TEXT NOT NULL,
			group_index INTEGER NOT NULL,
			group_name TEXT NOT NULL,
			status TEXT NOT NULL,
			error_text TEXT NOT NULL DEFAULT ''
		);`,
		`CREATE INDEX IF NOT EXISTS idx_telegram_send_history_sent_at ON telegram_send_history(sent_at DESC);`,
	}
	for _, stmt := range stmts {
		if err := execWithBusyRetry(db, stmt); err != nil {
			return err
		}
	}
	_ = ensureColumn(db, "market_state", "long_short_ratio", "REAL")
	_ = ensureColumn(db, "oi_snapshots", "long_short_ratio", "REAL")
	return nil
}

func ensureColumn(db *sql.DB, table, col, typ string) error {
	rows, err := db.Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull int
		var dflt sql.NullString
		var pk int
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			return err
		}
		if strings.EqualFold(name, col) {
			return nil
		}
	}
	_, err = db.Exec(`ALTER TABLE ` + table + ` ADD COLUMN ` + col + ` ` + typ)
	return err
}
