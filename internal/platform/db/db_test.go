package db

import (
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func TestInitIsIdempotent(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "idempotent.db")
	conn, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer conn.Close()

	if err := Configure(conn); err != nil {
		t.Fatalf("configure db: %v", err)
	}
	if err := Init(conn); err != nil {
		t.Fatalf("first init: %v", err)
	}
	if err := Init(conn); err != nil {
		t.Fatalf("second init: %v", err)
	}
}
