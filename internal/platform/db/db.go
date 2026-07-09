package db

import (
	"database/sql"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	_ "modernc.org/sqlite"
)

const defaultSQLiteBusyTimeoutMS = 30000

type Dialect string

const (
	DialectSQLite   Dialect = "sqlite"
	DialectPostgres Dialect = "postgres"
)

type DB struct {
	*sql.DB
	dialect Dialect
}

type Tx struct {
	tx      *sql.Tx
	dialect Dialect
}

type Stmt struct {
	stmt *sql.Stmt
}

func (db *DB) Dialect() Dialect {
	if db == nil || db.dialect == "" {
		return DialectSQLite
	}
	return db.dialect
}

func (db *DB) IsPostgres() bool {
	return db != nil && db.Dialect() == DialectPostgres
}

func SQLiteDSN(dbPath string) string {
	dbPath = strings.TrimSpace(dbPath)
	if dbPath == "" {
		return dbPath
	}
	q := url.Values{}
	q.Add("_pragma", "busy_timeout="+strconv.Itoa(defaultSQLiteBusyTimeoutMS))
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_pragma", "synchronous(NORMAL)")
	sep := "?"
	if strings.Contains(dbPath, "?") {
		sep = "&"
	}
	return dbPath + sep + q.Encode()
}

func Open(dbPath string) (*DB, error) {
	if dsn := strings.TrimSpace(os.Getenv("DATABASE_URL")); dsn != "" {
		return OpenPostgres(dsn)
	}
	return OpenSQLite(dbPath)
}

func OpenSQLite(dbPath string) (*DB, error) {
	sqlDB, err := sql.Open("sqlite", SQLiteDSN(dbPath))
	if err != nil {
		return nil, err
	}
	return &DB{DB: sqlDB, dialect: DialectSQLite}, nil
}

func OpenPostgres(dsn string) (*DB, error) {
	sqlDB, err := sql.Open("pgx", strings.TrimSpace(dsn))
	if err != nil {
		return nil, err
	}
	return &DB{DB: sqlDB, dialect: DialectPostgres}, nil
}

func RedactDatabaseURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.User == nil {
		return raw
	}
	username := u.User.Username()
	if username == "" {
		return raw
	}
	if _, hasPassword := u.User.Password(); hasPassword {
		u.User = url.UserPassword(username, "xxxxx")
	} else {
		u.User = url.User(username)
	}
	return u.String()
}

func WrapSQLite(sqlDB *sql.DB) *DB {
	return &DB{DB: sqlDB, dialect: DialectSQLite}
}

func WrapPostgres(sqlDB *sql.DB) *DB {
	return &DB{DB: sqlDB, dialect: DialectPostgres}
}

func (db *DB) Exec(query string, args ...any) (sql.Result, error) {
	return db.DB.Exec(db.rebind(query), args...)
}

func (db *DB) Query(query string, args ...any) (*sql.Rows, error) {
	return db.DB.Query(db.rebind(query), args...)
}

func (db *DB) QueryRow(query string, args ...any) *sql.Row {
	return db.DB.QueryRow(db.rebind(query), args...)
}

func (db *DB) Begin() (*Tx, error) {
	tx, err := db.DB.Begin()
	if err != nil {
		return nil, err
	}
	return &Tx{tx: tx, dialect: db.Dialect()}, nil
}

func (db *DB) InsertID(query string, args ...any) (int64, error) {
	var id int64
	if db.IsPostgres() {
		query = strings.TrimSpace(strings.TrimSuffix(query, ";")) + " RETURNING id"
		err := WithBusyRetry(func() error {
			return db.QueryRow(query, args...).Scan(&id)
		})
		return id, err
	}
	err := WithBusyRetry(func() error {
		res, execErr := db.Exec(query, args...)
		if execErr != nil {
			return execErr
		}
		var idErr error
		id, idErr = res.LastInsertId()
		return idErr
	})
	return id, err
}

func (db *DB) rebind(query string) string {
	if db.IsPostgres() {
		return RebindPostgres(query)
	}
	return query
}

func (tx *Tx) Exec(query string, args ...any) (sql.Result, error) {
	return tx.tx.Exec(rebindForDialect(tx.dialect, query), args...)
}

func (tx *Tx) Query(query string, args ...any) (*sql.Rows, error) {
	return tx.tx.Query(rebindForDialect(tx.dialect, query), args...)
}

func (tx *Tx) QueryRow(query string, args ...any) *sql.Row {
	return tx.tx.QueryRow(rebindForDialect(tx.dialect, query), args...)
}

func (tx *Tx) Prepare(query string) (*Stmt, error) {
	stmt, err := tx.tx.Prepare(rebindForDialect(tx.dialect, query))
	if err != nil {
		return nil, err
	}
	return &Stmt{stmt: stmt}, nil
}

func (tx *Tx) Commit() error {
	return tx.tx.Commit()
}

func (tx *Tx) Rollback() error {
	return tx.tx.Rollback()
}

func (stmt *Stmt) Exec(args ...any) (sql.Result, error) {
	return stmt.stmt.Exec(args...)
}

func (stmt *Stmt) Query(args ...any) (*sql.Rows, error) {
	return stmt.stmt.Query(args...)
}

func (stmt *Stmt) QueryRow(args ...any) *sql.Row {
	return stmt.stmt.QueryRow(args...)
}

func (stmt *Stmt) Close() error {
	return stmt.stmt.Close()
}

func rebindForDialect(dialect Dialect, query string) string {
	if dialect == DialectPostgres {
		return RebindPostgres(query)
	}
	return query
}

func RebindPostgres(query string) string {
	var b strings.Builder
	b.Grow(len(query) + 8)
	arg := 1
	inSingle := false
	inDouble := false
	inLineComment := false
	inBlockComment := false
	for i := 0; i < len(query); i++ {
		ch := query[i]
		next := byte(0)
		if i+1 < len(query) {
			next = query[i+1]
		}
		if inLineComment {
			b.WriteByte(ch)
			if ch == '\n' {
				inLineComment = false
			}
			continue
		}
		if inBlockComment {
			b.WriteByte(ch)
			if ch == '*' && next == '/' {
				i++
				b.WriteByte('/')
				inBlockComment = false
			}
			continue
		}
		if inSingle {
			b.WriteByte(ch)
			if ch == '\'' {
				if next == '\'' {
					i++
					b.WriteByte(next)
					continue
				}
				inSingle = false
			}
			continue
		}
		if inDouble {
			b.WriteByte(ch)
			if ch == '"' {
				if next == '"' {
					i++
					b.WriteByte(next)
					continue
				}
				inDouble = false
			}
			continue
		}
		switch {
		case ch == '-' && next == '-':
			inLineComment = true
			b.WriteByte(ch)
			i++
			b.WriteByte(next)
		case ch == '/' && next == '*':
			inBlockComment = true
			b.WriteByte(ch)
			i++
			b.WriteByte(next)
		case ch == '\'':
			inSingle = true
			b.WriteByte(ch)
		case ch == '"':
			inDouble = true
			b.WriteByte(ch)
		case ch == '?':
			b.WriteByte('$')
			b.WriteString(strconv.Itoa(arg))
			arg++
		default:
			b.WriteByte(ch)
		}
	}
	return b.String()
}

func IsSQLiteBusy(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "database is locked") ||
		strings.Contains(msg, "database table is locked") ||
		strings.Contains(msg, "sqlite_busy") ||
		strings.Contains(msg, "sqlite_locked")
}

func WithBusyRetry(fn func() error) error {
	var err error
	for attempt := 0; attempt < 8; attempt++ {
		err = fn()
		if !IsSQLiteBusy(err) {
			return err
		}
		time.Sleep(time.Duration(250*(attempt+1)) * time.Millisecond)
	}
	return err
}

func ExecWithBusyRetry(db *DB, stmt string, args ...any) (sql.Result, error) {
	var res sql.Result
	err := WithBusyRetry(func() error {
		var execErr error
		res, execErr = db.Exec(stmt, args...)
		return execErr
	})
	return res, err
}

func execWithBusyRetry(db *DB, stmt string, args ...any) error {
	_, err := ExecWithBusyRetry(db, stmt, args...)
	return err
}

func Configure(db *DB) error {
	if db.IsPostgres() {
		db.SetMaxOpenConns(16)
		db.SetMaxIdleConns(8)
		db.SetConnMaxLifetime(30 * time.Minute)
		return db.Ping()
	}
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(4)
	db.SetConnMaxLifetime(0)
	if err := execWithBusyRetry(db, `PRAGMA busy_timeout=`+strconv.Itoa(defaultSQLiteBusyTimeoutMS)+`;`); err != nil {
		return err
	}
	if err := execWithBusyRetry(db, `PRAGMA journal_mode=WAL;`); err != nil {
		return err
	}
	return execWithBusyRetry(db, `PRAGMA synchronous=NORMAL;`)
}

func Init(db *DB) error {
	stmts := sqliteSchemaStatements()
	if db.IsPostgres() {
		stmts = postgresSchemaStatements()
	}
	for _, stmt := range stmts {
		if err := execWithBusyRetry(db, stmt); err != nil {
			return err
		}
	}
	_ = execWithBusyRetry(db, `UPDATE analysis_direction_signals
		SET verify_horizon_min=5
		WHERE verify_horizon_min IS NULL OR verify_horizon_min<>5`)
	_ = ensureColumn(db, "analysis_direction_signals", "confidence", "REAL NOT NULL DEFAULT 0")
	_ = ensureColumn(db, "analysis_liquidation_backtest_signals", "signal_action", "TEXT NOT NULL DEFAULT ''")
	_ = ensureColumn(db, "analysis_liquidation_backtest_signals", "signal_side", "TEXT NOT NULL DEFAULT ''")
	_ = ensureColumn(db, "analysis_liquidation_backtest_signals", "signal_label", "TEXT NOT NULL DEFAULT ''")
	_ = execWithBusyRetry(db, `DELETE FROM analysis_liquidation_backtest_signals WHERE signal_action='' OR signal_side='';`)
	_ = execWithBusyRetry(db, `DROP INDEX IF EXISTS idx_analysis_liq_backtest_signals_uniq;`)
	_ = execWithBusyRetry(db, `CREATE UNIQUE INDEX IF NOT EXISTS idx_analysis_liq_backtest_signals_uniq
		ON analysis_liquidation_backtest_signals(symbol, signal_ts, signal_side, signal_action);`)
	_ = ensureColumn(db, "market_state", "long_short_ratio", "REAL")
	_ = ensureColumn(db, "oi_snapshots", "long_short_ratio", "REAL")
	return nil
}

func sqliteSchemaStatements() []string {
	return []string{
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
		`CREATE INDEX IF NOT EXISTS idx_liquidation_events_ts ON liquidation_events(event_ts);`,
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
		`CREATE TABLE IF NOT EXISTS market_info_snapshots (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			exchange TEXT NOT NULL,
			symbol TEXT NOT NULL,
			period TEXT NOT NULL,
			limit_count INTEGER NOT NULL,
			generated_at INTEGER NOT NULL,
			refreshed_at INTEGER NOT NULL,
			status TEXT NOT NULL DEFAULT 'ready',
			error_message TEXT NOT NULL DEFAULT '',
			payload_json TEXT NOT NULL
		);`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_market_info_snapshots_key
			ON market_info_snapshots(exchange, symbol, period, limit_count);`,
		`CREATE INDEX IF NOT EXISTS idx_market_info_snapshots_generated_at
			ON market_info_snapshots(symbol, generated_at);`,
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
		`CREATE TABLE IF NOT EXISTS analysis_direction_signals (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			signal_ts INTEGER NOT NULL,
			symbol TEXT NOT NULL,
			source_group INTEGER NOT NULL,
			direction TEXT NOT NULL,
			confidence REAL NOT NULL DEFAULT 0,
			signal_price REAL NOT NULL,
			analysis_generated_at INTEGER NOT NULL,
			headline TEXT NOT NULL DEFAULT '',
			summary TEXT NOT NULL DEFAULT '',
			verify_horizon_min INTEGER NOT NULL DEFAULT 5
		);`,
		`CREATE INDEX IF NOT EXISTS idx_analysis_direction_signals_ts ON analysis_direction_signals(signal_ts DESC);`,
		`CREATE INDEX IF NOT EXISTS idx_analysis_direction_signals_symbol_ts ON analysis_direction_signals(symbol, signal_ts DESC);`,
		`CREATE TABLE IF NOT EXISTS analysis_liquidation_backtest_signals (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			signal_ts INTEGER NOT NULL,
			symbol TEXT NOT NULL,
			source_group INTEGER NOT NULL,
			direction TEXT NOT NULL,
			confidence REAL NOT NULL DEFAULT 0,
			signal_price REAL NOT NULL,
			analysis_generated_at INTEGER NOT NULL,
			headline TEXT NOT NULL DEFAULT '',
			summary TEXT NOT NULL DEFAULT '',
			verify_horizon_min INTEGER NOT NULL DEFAULT 5,
			signal_action TEXT NOT NULL DEFAULT '',
			signal_side TEXT NOT NULL DEFAULT '',
			signal_label TEXT NOT NULL DEFAULT '',
			second_factor_key TEXT NOT NULL DEFAULT '',
			second_factor_label TEXT NOT NULL DEFAULT '',
			generated_at INTEGER NOT NULL
		);`,
		`CREATE INDEX IF NOT EXISTS idx_analysis_liq_backtest_signals_symbol_ts
			ON analysis_liquidation_backtest_signals(symbol, signal_ts DESC);`,
	}
}

func postgresSchemaStatements() []string {
	out := make([]string, 0, len(sqliteSchemaStatements()))
	for _, stmt := range sqliteSchemaStatements() {
		if strings.HasPrefix(strings.TrimSpace(strings.ToUpper(stmt)), "PRAGMA ") {
			continue
		}
		stmt = strings.ReplaceAll(stmt, "INTEGER PRIMARY KEY AUTOINCREMENT", "BIGINT GENERATED BY DEFAULT AS IDENTITY PRIMARY KEY")
		stmt = strings.ReplaceAll(stmt, " INTEGER", " BIGINT")
		stmt = strings.ReplaceAll(stmt, " REAL", " DOUBLE PRECISION")
		out = append(out, stmt)
	}
	return out
}

func ensureColumn(db *DB, table, col, typ string) error {
	if db.IsPostgres() {
		var exists bool
		err := db.QueryRow(`SELECT EXISTS (
			SELECT 1 FROM information_schema.columns
			WHERE table_schema='public' AND table_name=? AND column_name=?
		)`, table, col).Scan(&exists)
		if err != nil {
			return err
		}
		if exists {
			return nil
		}
		return execWithBusyRetry(db, `ALTER TABLE `+table+` ADD COLUMN `+col+` `+postgresColumnType(typ))
	}
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
	return execWithBusyRetry(db, `ALTER TABLE `+table+` ADD COLUMN `+col+` `+typ)
}

func postgresColumnType(typ string) string {
	typ = strings.ReplaceAll(typ, "REAL", "DOUBLE PRECISION")
	return typ
}
