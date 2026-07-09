package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"log"
	"net/url"
	"os"
	"strings"

	"github.com/jackc/pgx/v5"

	dbplatform "multipleexchangeliquidationmap/internal/platform/db"
	"multipleexchangeliquidationmap/internal/platform/envfile"
)

type tableSpec struct {
	name     string
	columns  []string
	identity bool
	latest   string
}

var tables = []tableSpec{
	{name: "market_state", columns: []string{"exchange", "symbol", "mark_price", "oi_qty", "oi_value_usd", "funding_rate", "long_short_ratio", "updated_ts"}, latest: "updated_ts"},
	{name: "oi_snapshots", identity: true, columns: []string{"id", "exchange", "symbol", "mark_price", "oi_value_usd", "funding_rate", "long_short_ratio", "updated_ts"}, latest: "updated_ts"},
	{name: "liquidation_events", identity: true, columns: []string{"id", "exchange", "symbol", "side", "raw_side", "qty", "price", "mark_price", "notional_usd", "event_ts", "inserted_ts"}, latest: "event_ts"},
	{name: "band_reports", identity: true, columns: []string{"id", "report_ts", "symbol", "current_price", "band", "up_price", "up_notional_usd", "down_price", "down_notional_usd"}, latest: "report_ts"},
	{name: "longest_bar_reports", identity: true, columns: []string{"id", "report_ts", "symbol", "side", "bucket_size", "bucket_price", "bucket_notional_usd"}, latest: "report_ts"},
	{name: "app_settings", columns: []string{"key", "value"}},
	{name: "model_liqmap_snapshots", identity: true, columns: []string{"id", "symbol", "window_days", "config_rev", "generated_at", "payload_json"}, latest: "generated_at"},
	{name: "market_info_snapshots", identity: true, columns: []string{"id", "exchange", "symbol", "period", "limit_count", "generated_at", "refreshed_at", "status", "error_message", "payload_json"}, latest: "generated_at"},
	{name: "price_wall_events", identity: true, columns: []string{"id", "side", "price", "peak_notional_usd", "duration_ms", "event_ts", "mode", "inserted_ts"}, latest: "event_ts"},
	{name: "webdatasource_settings", columns: []string{"key", "value"}},
	{name: "webdatasource_runs", identity: true, columns: []string{"id", "started_at", "finished_at", "status", "window_days", "error_message", "records_count", "source_meta_json"}, latest: "started_at"},
	{name: "webdatasource_snapshots", identity: true, columns: []string{"id", "symbol", "window_days", "captured_at", "range_low", "range_high", "payload_json"}, latest: "captured_at"},
	{name: "webdatasource_points", identity: true, columns: []string{"id", "snapshot_id", "symbol", "window_days", "side", "exchange", "price", "liq_value", "captured_at"}, latest: "captured_at"},
	{name: "telegram_send_history", identity: true, columns: []string{"id", "sent_at", "send_mode", "group_index", "group_name", "status", "error_text"}, latest: "sent_at"},
	{name: "analysis_direction_signals", identity: true, columns: []string{"id", "signal_ts", "symbol", "source_group", "direction", "confidence", "signal_price", "analysis_generated_at", "headline", "summary", "verify_horizon_min"}, latest: "signal_ts"},
	{name: "analysis_liquidation_backtest_signals", identity: true, columns: []string{"id", "signal_ts", "symbol", "source_group", "direction", "confidence", "signal_price", "analysis_generated_at", "headline", "summary", "verify_horizon_min", "signal_action", "signal_side", "signal_label", "second_factor_key", "second_factor_label", "generated_at"}, latest: "signal_ts"},
}

func main() {
	if err := envfile.Load("config/local.env"); err != nil {
		log.Fatal(err)
	}
	source := flag.String("source", getenv("SQLITE_PATH", getenv("DB_PATH", "data/liqmap.db")), "source SQLite database path")
	target := flag.String("target", strings.TrimSpace(os.Getenv("DATABASE_URL")), "target PostgreSQL DATABASE_URL")
	allowNonEmpty := flag.Bool("allow-non-empty", false, "allow migrating into a non-empty target database")
	truncateTarget := flag.Bool("truncate", false, "delete target table rows before migration")
	skipCreateDB := flag.Bool("skip-create-db", false, "skip CREATE DATABASE if the target database is missing")
	flag.Parse()

	if strings.TrimSpace(*target) == "" {
		log.Fatal("target PostgreSQL DATABASE_URL is required")
	}
	if !*skipCreateDB {
		if err := ensureTargetDatabase(*target); err != nil {
			log.Fatal(err)
		}
	}

	src, err := dbplatform.OpenSQLite(*source)
	if err != nil {
		log.Fatal(err)
	}
	defer src.Close()
	if err := dbplatform.Configure(src); err != nil {
		log.Fatal(err)
	}

	dst, err := dbplatform.OpenPostgres(*target)
	if err != nil {
		log.Fatal(err)
	}
	defer dst.Close()
	if err := dbplatform.Configure(dst); err != nil {
		log.Fatal(err)
	}
	if err := dbplatform.Init(dst); err != nil {
		log.Fatal(err)
	}
	pgxConn, err := pgx.Connect(context.Background(), *target)
	if err != nil {
		log.Fatal(err)
	}
	defer pgxConn.Close(context.Background())

	totalRows, err := targetRowCount(dst)
	if err != nil {
		log.Fatal(err)
	}
	if totalRows > 0 {
		switch {
		case *truncateTarget:
			if err := truncateTables(dst); err != nil {
				log.Fatal(err)
			}
		case !*allowNonEmpty:
			log.Fatalf("target database is not empty (%d rows); use -truncate or -allow-non-empty explicitly", totalRows)
		}
	}

	log.Printf("migrating sqlite=%s -> postgres=%s", *source, dbplatform.RedactDatabaseURL(*target))
	for _, table := range tables {
		sourceCount, targetCount, err := migrateTable(context.Background(), src, dst, pgxConn, table)
		if err != nil {
			log.Fatal(err)
		}
		if sourceCount != targetCount {
			log.Fatalf("%s row count mismatch: sqlite=%d postgres=%d", table.name, sourceCount, targetCount)
		}
		if table.identity {
			if err := resetIdentity(dst, table.name); err != nil {
				log.Fatal(err)
			}
		}
		latest := ""
		if table.latest != "" {
			latest = latestSummary(src, dst, table)
		}
		log.Printf("%-38s rows=%d%s", table.name, targetCount, latest)
	}
	log.Printf("migration completed")
}

func ensureTargetDatabase(target string) error {
	u, err := url.Parse(target)
	if err != nil {
		return err
	}
	dbName := strings.TrimPrefix(u.Path, "/")
	if dbName == "" {
		return fmt.Errorf("target DATABASE_URL must include a database name")
	}
	maintenance := *u
	maintenance.Path = "/postgres"
	db, err := dbplatform.OpenPostgres(maintenance.String())
	if err != nil {
		return err
	}
	defer db.Close()
	if err := dbplatform.Configure(db); err != nil {
		return err
	}
	var exists bool
	if err := db.QueryRow(`SELECT EXISTS(SELECT 1 FROM pg_database WHERE datname=?)`, dbName).Scan(&exists); err != nil {
		return err
	}
	if exists {
		return nil
	}
	_, err = db.Exec(`CREATE DATABASE ` + quoteIdent(dbName))
	return err
}

func migrateTable(ctx context.Context, src, dst *dbplatform.DB, pgxConn *pgx.Conn, table tableSpec) (int64, int64, error) {
	sourceCount, err := countRows(src, table.name)
	if err != nil {
		return 0, 0, err
	}
	if sourceCount == 0 {
		targetCount, err := countRows(dst, table.name)
		return 0, targetCount, err
	}
	selectSQL := `SELECT ` + identList(table.columns) + ` FROM ` + quoteIdent(table.name)
	rows, err := src.Query(selectSQL)
	if err != nil {
		return 0, 0, err
	}
	defer rows.Close()

	tx, err := pgxConn.Begin(ctx)
	if err != nil {
		return 0, 0, err
	}
	defer tx.Rollback(ctx)
	copied, err := tx.CopyFrom(ctx, pgx.Identifier{table.name}, table.columns, newSQLRowsCopySource(rows, len(table.columns)))
	if err != nil {
		return 0, 0, err
	}
	if copied != sourceCount {
		return sourceCount, copied, fmt.Errorf("%s copy row count mismatch before commit: sqlite=%d copied=%d", table.name, sourceCount, copied)
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, 0, err
	}
	targetCount, err := countRows(dst, table.name)
	return sourceCount, targetCount, err
}

type sqlRowsCopySource struct {
	rows   *sql.Rows
	values []any
	scan   []any
	err    error
}

func newSQLRowsCopySource(rows *sql.Rows, columnCount int) *sqlRowsCopySource {
	values := make([]any, columnCount)
	scan := make([]any, columnCount)
	for i := range values {
		scan[i] = &values[i]
	}
	return &sqlRowsCopySource{rows: rows, values: values, scan: scan}
}

func (s *sqlRowsCopySource) Next() bool {
	if s.err != nil {
		return false
	}
	if !s.rows.Next() {
		s.err = s.rows.Err()
		return false
	}
	for i := range s.values {
		s.values[i] = nil
	}
	if err := s.rows.Scan(s.scan...); err != nil {
		s.err = err
		return false
	}
	return true
}

func (s *sqlRowsCopySource) Values() ([]any, error) {
	return s.values, s.err
}

func (s *sqlRowsCopySource) Err() error {
	return s.err
}

func truncateTables(db *dbplatform.DB) error {
	for i := len(tables) - 1; i >= 0; i-- {
		if _, err := db.Exec(`DELETE FROM ` + quoteIdent(tables[i].name)); err != nil {
			return err
		}
	}
	return nil
}

func targetRowCount(db *dbplatform.DB) (int64, error) {
	var total int64
	for _, table := range tables {
		count, err := countRows(db, table.name)
		if err != nil {
			return 0, err
		}
		total += count
	}
	return total, nil
}

func countRows(db *dbplatform.DB, table string) (int64, error) {
	var count int64
	err := db.QueryRow(`SELECT COUNT(*) FROM ` + quoteIdent(table)).Scan(&count)
	return count, err
}

func resetIdentity(db *dbplatform.DB, table string) error {
	q := `SELECT setval(pg_get_serial_sequence(` + quoteLiteral(table) + `, 'id'), COALESCE((SELECT MAX(id) FROM ` + quoteIdent(table) + `), 1), COALESCE((SELECT MAX(id) FROM ` + quoteIdent(table) + `), 0) > 0)`
	_, err := db.Exec(q)
	return err
}

func latestSummary(src, dst *dbplatform.DB, table tableSpec) string {
	srcValue := nullableMax(src, table.name, table.latest)
	dstValue := nullableMax(dst, table.name, table.latest)
	return fmt.Sprintf(" latest[%s]=sqlite:%s postgres:%s", table.latest, srcValue, dstValue)
}

func nullableMax(db *dbplatform.DB, table, column string) string {
	var value sql.NullInt64
	err := db.QueryRow(`SELECT MAX(` + quoteIdent(column) + `) FROM ` + quoteIdent(table)).Scan(&value)
	if err != nil || !value.Valid {
		return "-"
	}
	return fmt.Sprint(value.Int64)
}

func quoteIdent(v string) string {
	return `"` + strings.ReplaceAll(v, `"`, `""`) + `"`
}

func identList(values []string) string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		out = append(out, quoteIdent(value))
	}
	return strings.Join(out, ", ")
}

func quoteLiteral(v string) string {
	return `'` + strings.ReplaceAll(v, `'`, `''`) + `'`
}

func getenv(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}
