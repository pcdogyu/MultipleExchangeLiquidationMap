package liqmap

import (
	"database/sql"
	"strings"
	"testing"
	"time"
)

func TestBuildLiquidationPatternQuestionAttachmentOmitsScreenshotPrefix(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()

	_, err = db.Exec(`CREATE TABLE liquidation_events (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		event_ts INTEGER NOT NULL,
		exchange TEXT,
		symbol TEXT,
		side TEXT,
		price REAL,
		qty REAL,
		notional_usd REAL
	)`)
	if err != nil {
		t.Fatalf("create liquidation_events: %v", err)
	}

	now := time.Now()
	rows := []struct {
		ts       int64
		side     string
		notional float64
	}{
		{ts: now.Add(-30 * time.Minute).UnixMilli(), side: "long", notional: 100},
		{ts: now.Add(-2 * time.Hour).UnixMilli(), side: "short", notional: 50},
		{ts: now.Add(-6 * time.Hour).UnixMilli(), side: "short", notional: 25},
		{ts: now.Add(-20 * time.Hour).UnixMilli(), side: "long", notional: 10},
	}
	for _, row := range rows {
		_, err = db.Exec(
			`INSERT INTO liquidation_events(event_ts, exchange, symbol, side, price, qty, notional_usd)
			 VALUES(?, 'binance', ?, ?, 3000, 1, ?)`,
			row.ts, defaultSymbol, row.side, row.notional,
		)
		if err != nil {
			t.Fatalf("insert liquidation row: %v", err)
		}
	}

	app := &App{db: db}
	text := app.buildLiquidationPatternQuestionAttachment()

	if strings.Contains(text, "已附截图：四周期多空清算结构") {
		t.Fatalf("expected attachment text to omit screenshot prefix, got:\n%s", text)
	}
	if !strings.Contains(text, "<b>四周期 16 种形态判断</b>") {
		t.Fatalf("expected attachment text to include pattern title, got:\n%s", text)
	}
	if !strings.Contains(text, "/") {
		t.Fatalf("expected attachment text to include pattern code and trend title, got:\n%s", text)
	}
}
