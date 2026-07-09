package db

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

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

func TestOpenWaitsForConcurrentSQLiteWriter(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "busy-wait.db")
	conn, err := Open(dbPath)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer conn.Close()

	if err := Configure(conn); err != nil {
		t.Fatalf("configure db: %v", err)
	}
	if _, err := conn.Exec(`CREATE TABLE writes(id INTEGER PRIMARY KEY, value TEXT)`); err != nil {
		t.Fatalf("create table: %v", err)
	}

	tx, err := conn.Begin()
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	if _, err := tx.Exec(`INSERT INTO writes(value) VALUES('hold')`); err != nil {
		t.Fatalf("hold write lock: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		_, execErr := conn.Exec(`INSERT INTO writes(value) VALUES('waited')`)
		done <- execErr
	}()

	time.Sleep(150 * time.Millisecond)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("concurrent insert failed before lock release: %v", err)
		}
		t.Fatal("concurrent insert completed before the write lock was released")
	default:
	}

	if err := tx.Commit(); err != nil {
		t.Fatalf("commit holding tx: %v", err)
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("concurrent insert after lock release: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("concurrent insert did not finish after lock release")
	}
}
